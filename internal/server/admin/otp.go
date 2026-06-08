// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

// Package admin — TOTP (RFC 6238) second-factor authentication handlers.
//
// Flow
//
//  1. User calls POST /api/v1/auth/otp/setup  → receives provisioning URI +
//     base32 secret (for manual entry).  Secret is stored in User.TOTPSecret
//     but TOTPEnabled remains false until the user confirms.
//
//  2. User calls POST /api/v1/auth/otp/enable with a valid TOTP code → server
//     sets TOTPEnabled=true.  From this point all logins require a second step.
//
//  3. User calls POST /api/v1/auth/otp/disable with their current password →
//     server clears TOTPSecret and sets TOTPEnabled=false.
//
// Login two-step flow (when TOTPEnabled=true):
//
//	Step A: POST /api/v1/auth/login  {username, password}
//	        → 200 { otp_required: true, pending_token: "<token>" }
//	Step B: POST /api/v1/auth/login/otp  {pending_token, otp_code}
//	        → 200 + session cookie (same as normal login)

package admin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"image/color"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/alibaba/ilogtail/config_server/internal/model"
	"github.com/pquerna/otp/totp"
	"github.com/redis/go-redis/v9"
	"github.com/skip2/go-qrcode"
)

const (
	otpIssuer        = "LoongCollector ConfigServer"
	pendingOTPTTL    = 5 * time.Minute
	pendingOTPPrefix = "cs_otp_pending:"
)

// ── Pending-OTP token store ───────────────────────────────────────────────────

// pendingOTPStoreIface stores short-lived tokens issued after a successful
// password check when the user has TOTP enabled.  Tokens are single-use.
type pendingOTPStoreIface interface {
	issue(ctx context.Context, username string) string
	consume(ctx context.Context, token string) (username string, ok bool)
}

type pendingEntry struct {
	username  string
	expiresAt time.Time
}

type memPendingOTPStore struct {
	mu   sync.Mutex
	data map[string]pendingEntry
}

func (s *memPendingOTPStore) issue(_ context.Context, username string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	s.mu.Lock()
	s.data[token] = pendingEntry{username: username, expiresAt: time.Now().Add(pendingOTPTTL)}
	s.mu.Unlock()
	return token
}

func (s *memPendingOTPStore) consume(_ context.Context, token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[token]
	if !ok {
		return "", false
	}
	delete(s.data, token)
	if time.Now().After(e.expiresAt) {
		return "", false
	}
	return e.username, true
}

type redisPendingOTPStore struct {
	rdb redis.UniversalClient
}

func (s *redisPendingOTPStore) issue(ctx context.Context, username string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	_ = s.rdb.Set(ctx, pendingOTPPrefix+token, username, pendingOTPTTL).Err()
	return token
}

func (s *redisPendingOTPStore) consume(ctx context.Context, token string) (string, bool) {
	key := pendingOTPPrefix + token
	var getCmd *redis.StringCmd
	_, err := s.rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		getCmd = pipe.Get(ctx, key)
		pipe.Del(ctx, key)
		return nil
	})
	if err != nil && err != redis.Nil {
		return "", false
	}
	username, err := getCmd.Result()
	if err != nil || username == "" {
		return "", false
	}
	return username, true
}

var pendingOTP pendingOTPStoreIface = &memPendingOTPStore{data: make(map[string]pendingEntry)}

func initPendingOTPStore(rdb redis.UniversalClient) {
	if rdb != nil {
		pendingOTP = &redisPendingOTPStore{rdb: rdb}
	}
}

// ── OTP handlers ──────────────────────────────────────────────────────────────

// OTPSetup generates a new TOTP secret for the authenticated user and stores it
// (without enabling it yet).  The client should display the provisioning URI as
// a QR code and ask the user to confirm with a valid code before calling OTPEnable.
//
//	POST /api/v1/auth/otp/setup
func (h *AdminHandler) OTPSetup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uc, _ := userFromCtx(ctx)

	user, err := h.mgr.GetUser(ctx, uc.Username)
	if err != nil || user == nil {
		internalError(w, err)
		return
	}
	if user.TOTPEnabled {
		badRequest(w, "OTP is already enabled; disable it first")
		return
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      otpIssuer,
		AccountName: uc.Username,
	})
	if err != nil {
		internalError(w, err)
		return
	}

	user.TOTPSecret = key.Secret()
	user.TOTPEnabled = false
	if err := h.mgr.UpdateUser(ctx, user); err != nil {
		internalError(w, err)
		return
	}

	ok(w, map[string]string{
		"provisioning_uri": key.URL(),
		"secret":           key.Secret(), // for manual entry in authenticator apps
		"qr_code":          otpQRCodeBase64(key.URL()),
	})
}

// otpQRCodeBase64 encodes the otpauth:// provisioning URI as a PNG QR code and
// returns it as a data URI string ("data:image/png;base64,...").
// On error it returns an empty string so the client can fall back to the raw URI.
func otpQRCodeBase64(uri string) string {
	qr, err := qrcode.New(uri, qrcode.Medium)
	if err != nil {
		return ""
	}
	qr.ForegroundColor = color.RGBA{R: 0x06, G: 0x4e, B: 0x5a, A: 0xff}
	qr.BackgroundColor = color.RGBA{R: 0xf7, G: 0xfb, B: 0xfc, A: 0xff}

	png, err := qr.PNG(256)
	if err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}

// OTPEnable verifies a TOTP code against the stored (not-yet-enabled) secret and,
// on success, marks TOTPEnabled=true for the user.
//
//	POST /api/v1/auth/otp/enable
//	Body: { "otp_code": "123456" }
func (h *AdminHandler) OTPEnable(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uc, _ := userFromCtx(ctx)

	var body struct {
		OTPCode string `json:"otp_code"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, err.Error())
		return
	}
	if body.OTPCode == "" {
		badRequest(w, "otp_code is required")
		return
	}

	user, err := h.mgr.GetUser(ctx, uc.Username)
	if err != nil || user == nil {
		internalError(w, err)
		return
	}
	if user.TOTPSecret == "" {
		badRequest(w, "call /api/v1/auth/otp/setup first")
		return
	}
	if user.TOTPEnabled {
		badRequest(w, "OTP is already enabled")
		return
	}

	if !totp.Validate(body.OTPCode, user.TOTPSecret) {
		writeJSON(w, http.StatusUnauthorized, 401, "invalid OTP code", nil)
		return
	}

	user.TOTPEnabled = true
	if err := h.mgr.UpdateUser(ctx, user); err != nil {
		internalError(w, err)
		return
	}
	log.Printf("OTP enabled for user %q", uc.Username)
	ok(w, map[string]string{"message": "OTP enabled"})
}

// OTPDisable disables TOTP for the authenticated user.
// Requires the current password to prevent an attacker who has stolen a
// session cookie from locking out MFA.
//
//	POST /api/v1/auth/otp/disable
//	Body: { "current_password": "..." }
func (h *AdminHandler) OTPDisable(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uc, _ := userFromCtx(ctx)

	var body struct {
		CurrentPassword string `json:"current_password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, err.Error())
		return
	}
	if body.CurrentPassword == "" {
		badRequest(w, "current_password is required")
		return
	}

	user, err := h.mgr.GetUser(ctx, uc.Username)
	if err != nil || user == nil {
		internalError(w, err)
		return
	}

	if err := verifyPassword(user, body.CurrentPassword); err != nil {
		writeJSON(w, http.StatusUnauthorized, 401, "current password is incorrect", nil)
		return
	}

	user.TOTPSecret = ""
	user.TOTPEnabled = false
	if err := h.mgr.UpdateUser(ctx, user); err != nil {
		internalError(w, err)
		return
	}
	log.Printf("OTP disabled for user %q", uc.Username)
	ok(w, map[string]string{"message": "OTP disabled"})
}

// OTPLoginStep completes the second step of a two-factor login by verifying the
// TOTP code against the pending-OTP token issued by AuthLogin.
//
//	POST /api/v1/auth/login/otp
//	Body: { "pending_token": "...", "otp_code": "123456" }
func (h *AdminHandler) OTPLoginStep(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body struct {
		PendingToken string `json:"pending_token"`
		OTPCode      string `json:"otp_code"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, err.Error())
		return
	}
	if body.PendingToken == "" || body.OTPCode == "" {
		badRequest(w, "pending_token and otp_code are required")
		return
	}

	username, valid := pendingOTP.consume(ctx, body.PendingToken)
	if !valid {
		writeJSON(w, http.StatusUnauthorized, 401, "pending token is invalid or has expired", nil)
		return
	}

	user, err := h.mgr.GetUser(ctx, username)
	if err != nil || user == nil {
		writeJSON(w, http.StatusUnauthorized, 401, "user not found", nil)
		return
	}

	if !totp.Validate(body.OTPCode, user.TOTPSecret) {
		writeJSON(w, http.StatusUnauthorized, 401, "invalid OTP code", nil)
		return
	}

	token := sessions.create(ctx, username)
	http.SetCookie(w, newSessionCookie(token))
	http.SetCookie(w, newCSRFCookie(token))
	ok(w, map[string]any{
		"message":  "login successful",
		"username": username,
		"is_admin": user.IsAdmin,
	})
}

// verifyPassword checks bcrypt hash; returns nil on success.
func verifyPassword(user *model.User, password string) error {
	return bcryptCompare([]byte(user.PasswordHash), []byte(password))
}
