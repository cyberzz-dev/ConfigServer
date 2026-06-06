// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	"github.com/alibaba/ilogtail/config_server/internal/cache"
	"github.com/alibaba/ilogtail/config_server/internal/model"
)

const (
	sessionCookieName  = "cs_session"
	csrfCookieName     = "cs_csrf"
	sessionTTL         = 8 * time.Hour
	bcryptCost         = 12
	sessionRedisPrefix = "cs_session:"
)

// ── Session store ─────────────────────────────────────────────────────────────

// sessionStoreIface abstracts session storage: in-memory (allinone) or Redis
// (distributed mode). All methods accept a context so Redis operations can be
// cancelled when the HTTP request is done.
type sessionStoreIface interface {
	// create issues a new session token for the given username.
	create(ctx context.Context, username string) string
	// get returns the username for a valid, non-expired token; ok is false otherwise.
	get(ctx context.Context, token string) (username string, ok bool)
	delete(ctx context.Context, token string)
}

// ── In-memory store (allinone / no Redis) ─────────────────────────────────────

type sessionEntry struct {
	username  string
	expiresAt time.Time
}

type memSessionStore struct {
	mu   sync.Mutex
	data map[string]sessionEntry
}

func (s *memSessionStore) create(_ context.Context, username string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	s.mu.Lock()
	s.data[token] = sessionEntry{username: username, expiresAt: time.Now().Add(sessionTTL)}
	s.mu.Unlock()
	return token
}

func (s *memSessionStore) get(_ context.Context, token string) (string, bool) {
	s.mu.Lock()
	e, ok := s.data[token]
	s.mu.Unlock()
	if !ok || time.Now().After(e.expiresAt) {
		return "", false
	}
	return e.username, true
}

func (s *memSessionStore) delete(_ context.Context, token string) {
	s.mu.Lock()
	delete(s.data, token)
	s.mu.Unlock()
}

// ── Redis store (distributed mode) ────────────────────────────────────────────

type redisSessionStore struct {
	rdb redis.UniversalClient
}

func (s *redisSessionStore) create(ctx context.Context, username string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	_ = s.rdb.Set(ctx, sessionRedisPrefix+token, username, sessionTTL).Err()
	return token
}

func (s *redisSessionStore) get(ctx context.Context, token string) (string, bool) {
	v, err := s.rdb.Get(ctx, sessionRedisPrefix+token).Result()
	if err != nil || v == "" {
		return "", false
	}
	return v, true
}

func (s *redisSessionStore) delete(ctx context.Context, token string) {
	_ = s.rdb.Del(ctx, sessionRedisPrefix+token).Err()
}

// ── User context helpers ──────────────────────────────────────────────────────

type contextKey int

const ctxKeyUser contextKey = iota

// userCtx carries the authenticated user's identity into request handlers.
type userCtx struct {
	Username string
	IsAdmin  bool
}

func userFromCtx(ctx context.Context) (userCtx, bool) {
	v, ok := ctx.Value(ctxKeyUser).(userCtx)
	return v, ok
}

// routeToPermission maps an HTTP method + path to a (resource, action) pair
// used for per-resource permission checks. Returns ("", "") when no
// per-resource check is needed (e.g. auth or read-only system endpoints).
func routeToPermission(method, path string) (resource, action string) {
	trimmed := strings.TrimPrefix(path, "/api/v1/")

	var res string
	switch {
	case strings.HasPrefix(trimmed, "pipeline-configs"):
		res = model.ResourcePipelineConfigs
	case strings.HasPrefix(trimmed, "instance-configs"):
		res = model.ResourceInstanceConfigs
	case strings.HasPrefix(trimmed, "onetime-commands"):
		res = model.ResourceOnetimeCommands
	case strings.HasPrefix(trimmed, "groups"):
		res = model.ResourceAgentGroups
	case strings.HasPrefix(trimmed, "agents"):
		res = model.ResourceAgents
	default:
		return "", ""
	}

	var act string
	switch method {
	case http.MethodGet, http.MethodHead:
		act = "read"
	case http.MethodPost:
		act = "create"
	case http.MethodPut, http.MethodPatch:
		act = "update"
	case http.MethodDelete:
		act = "delete"
	default:
		return "", ""
	}
	return res, act
}

// hasPermission checks whether username has the given action on resource.
// Permission is resolved via the user's assigned Role; users without a role
// are denied by default.
func hasPermission(ctx context.Context, mgr *cache.Manager, username, resource, action string) (bool, error) {
	user, err := mgr.GetUser(ctx, username)
	if err != nil {
		return false, err
	}
	if user == nil || user.RoleName == "" {
		return false, nil
	}
	perms, err := mgr.GetRolePermissions(ctx, user.RoleName)
	if err != nil {
		return false, err
	}
	for _, p := range perms {
		if p.Resource == resource {
			switch action {
			case "create":
				return p.CanCreate, nil
			case "read":
				return p.CanRead, nil
			case "update":
				return p.CanUpdate, nil
			case "delete":
				return p.CanDelete, nil
			}
		}
	}
	return false, nil
}

// sessions defaults to in-memory; initSessionStore switches to Redis when
// a Redis client is available (non-allinone deployments).
var sessions sessionStoreIface = &memSessionStore{data: make(map[string]sessionEntry)}

// initSessionStore must be called once at server startup.
// When rdb is nil (allinone mode) the in-memory store is kept.
func initSessionStore(rdb redis.UniversalClient) {
	if rdb != nil {
		sessions = &redisSessionStore{rdb: rdb}
	}
	initPendingOTPStore(rdb)
	initResetStore(rdb)
}

// ── Auth middleware ───────────────────────────────────────────────────────────

// authPublicPaths lists exact /api/v1 paths that do NOT require a session.
// All other /api/v1/... paths (including /api/v1/auth/change-password) are protected.
var authPublicPaths = map[string]bool{
	"/api/v1/auth/status":          true,
	"/api/v1/auth/init":            true,
	"/api/v1/auth/login":           true,
	"/api/v1/auth/login/otp":       true,
	"/api/v1/auth/logout":          true,
	"/api/v1/auth/forgot-password": true,
	"/api/v1/auth/reset-password":  true,
}

// csrfSafeMethods are HTTP methods that do not mutate state and therefore do
// not require CSRF validation.
var csrfSafeMethods = map[string]bool{
	http.MethodGet:     true,
	http.MethodHead:    true,
	http.MethodOptions: true,
}

// requireAuth returns middleware that protects /api/v1/ endpoints.
// It validates the session cookie, runs the CSRF double-submit check,
// looks up the authenticated user, injects a userCtx into the request
// context, and enforces per-resource permissions for non-admin users.
//
// Agent gRPC paths (/Agent/...) are served by a completely separate
// AgentServer on a different port and are never intercepted here.
func requireAuth(mgr *cache.Manager, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		ctx := r.Context()

		// Only protect /api/v1/... paths.
		if !strings.HasPrefix(path, "/api/v1/") {
			next.ServeHTTP(w, r)
			return
		}

		// Allow specific public auth endpoints through without a session.
		if authPublicPaths[path] {
			next.ServeHTTP(w, r)
			return
		}

		// Validate session cookie → get username.
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, 401, "unauthorized", nil)
			return
		}
		username, ok := sessions.get(ctx, cookie.Value)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, 401, "unauthorized", nil)
			return
		}

		// CSRF protection (double-submit cookie) for state-changing methods.
		if !csrfSafeMethods[r.Method] {
			csrfHeader := r.Header.Get("X-CSRF-Token")
			csrfCookie, csrfErr := r.Cookie(csrfCookieName)
			if csrfHeader == "" || csrfErr != nil || csrfHeader != csrfCookie.Value {
				writeJSON(w, http.StatusForbidden, 403, "CSRF validation failed", nil)
				return
			}
		}

		// Load user info from DB and inject into context.
		user, dbErr := mgr.GetUser(ctx, username)
		if dbErr != nil || user == nil {
			writeJSON(w, http.StatusUnauthorized, 401, "unauthorized", nil)
			return
		}
		uc := userCtx{Username: username, IsAdmin: user.IsAdmin}
		ctx = context.WithValue(ctx, ctxKeyUser, uc)

		// For non-admin users, enforce per-resource permissions.
		if !uc.IsAdmin {
			resource, action := routeToPermission(r.Method, r.URL.Path)
			if resource != "" {
				allowed, permErr := hasPermission(ctx, mgr, username, resource, action)
				if permErr != nil || !allowed {
					writeJSON(w, http.StatusForbidden, 403, "permission denied", nil)
					return
				}
			}
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAdmin is middleware that allows only admin users (IsAdmin=true).
// It must be applied after requireAuth (which injects the userCtx).
func requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uc, ok := userFromCtx(r.Context())
		if !ok || !uc.IsAdmin {
			writeJSON(w, http.StatusForbidden, 403, "admin access required", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── Auth handlers ─────────────────────────────────────────────────────────────

// AuthStatus responds with whether any admin account exists and whether the
// current request carries a valid session.
//
//	GET /api/v1/auth/status
func (h *AdminHandler) AuthStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	initialized, err := h.mgr.AdminExists(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}

	loggedIn := false
	if cookie, err2 := r.Cookie(sessionCookieName); err2 == nil {
		_, loggedIn = sessions.get(r.Context(), cookie.Value)
	}

	ok(w, map[string]bool{
		"initialized": initialized,
		"logged_in":   loggedIn,
	})
}

// AuthInit sets the admin password for the first time (requires 2-step
// confirmation). Rejected once any admin account exists.
//
//	POST /api/v1/auth/init
func (h *AdminHandler) AuthInit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Reject if an admin account already exists.
	alreadyInit, err := h.mgr.AdminExists(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if alreadyInit {
		badRequest(w, "admin account already initialized")
		return
	}

	var body struct {
		Password        string `json:"password"`
		ConfirmPassword string `json:"confirm_password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, err.Error())
		return
	}
	if body.Password == "" {
		badRequest(w, "password is required")
		return
	}
	if body.Password != body.ConfirmPassword {
		badRequest(w, "passwords do not match")
		return
	}
	if len(body.Password) < 8 {
		badRequest(w, "password must be at least 8 characters")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcryptCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "hash password: "+err.Error(), nil)
		return
	}

	user := &model.User{
		Username:     "admin",
		PasswordHash: string(hash),
		IsAdmin:      true,
	}
	if err := h.mgr.CreateUser(ctx, user); err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}

	ok(w, map[string]string{"message": "admin account initialized"})
}

// AuthLogin validates credentials and issues a session cookie.
// Accepts {username, password}; username defaults to "admin" when omitted.
// When TOTP is enabled for the user, instead of a session cookie the response
// carries {otp_required: true, pending_token: "..."} and the client must
// complete the login via POST /api/v1/auth/login/otp.
//
//	POST /api/v1/auth/login
func (h *AdminHandler) AuthLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, err.Error())
		return
	}
	if body.Username == "" {
		body.Username = "admin"
	}

	user, err := h.mgr.GetUser(ctx, body.Username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, 401, "invalid credentials", nil)
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(body.Password)); err != nil {
		writeJSON(w, http.StatusUnauthorized, 401, "invalid credentials", nil)
		return
	}

	// TOTP second factor: if enabled, do not issue a full session yet.
	if user.TOTPEnabled {
		pendingToken := pendingOTP.issue(ctx, body.Username)
		ok(w, map[string]any{
			"otp_required":  true,
			"pending_token": pendingToken,
		})
		return
	}

	token := sessions.create(ctx, body.Username)
	http.SetCookie(w, newSessionCookie(token))
	// Set the non-HttpOnly CSRF cookie so JS can read and echo it as
	// X-CSRF-Token on state-changing requests (double-submit cookie pattern).
	http.SetCookie(w, newCSRFCookie(token))
	ok(w, map[string]any{
		"message":  "login successful",
		"username": body.Username,
		"is_admin": user.IsAdmin,
	})
}

// AuthLogout invalidates the current session.
//
//	POST /api/v1/auth/logout
func (h *AdminHandler) AuthLogout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		sessions.delete(ctx, cookie.Value)
	}
	// Expire both the session cookie and the CSRF cookie immediately.
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		SameSite: http.SameSiteStrictMode,
	})
	ok(w, map[string]string{"message": "logged out"})
}

// AuthChangePassword changes the current user's password. Requires a valid session
// (enforced by requireAuth middleware).
//
//	POST /api/v1/auth/change-password
func (h *AdminHandler) AuthChangePassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
		ConfirmPassword string `json:"confirm_password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, err.Error())
		return
	}
	if body.NewPassword == "" {
		badRequest(w, "new password is required")
		return
	}
	if len(body.NewPassword) < 8 {
		badRequest(w, "password must be at least 8 characters")
		return
	}
	if body.NewPassword != body.ConfirmPassword {
		badRequest(w, "passwords do not match")
		return
	}

	uc, _ := userFromCtx(ctx)
	user, err := h.mgr.GetUser(ctx, uc.Username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, 401, "user not found", nil)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(body.CurrentPassword)); err != nil {
		writeJSON(w, http.StatusUnauthorized, 401, "current password is incorrect", nil)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(body.NewPassword), bcryptCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, "hash password: "+err.Error(), nil)
		return
	}

	user.PasswordHash = string(hash)
	if err := h.mgr.UpdateUser(ctx, user); err != nil {
		writeJSON(w, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}

	ok(w, map[string]string{"message": "password changed"})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func newSessionCookie(token string) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
}

// newCSRFCookie returns a non-HttpOnly cookie so that JavaScript can read the
// token and include it in the X-CSRF-Token request header.
func newCSRFCookie(token string) *http.Cookie {
	return &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: false, // must be readable by JS
		SameSite: http.SameSiteStrictMode,
	}
}

// bcryptCompare wraps bcrypt.CompareHashAndPassword as a named function so
// otp.go can call it without importing golang.org/x/crypto/bcrypt directly.
func bcryptCompare(hash, password []byte) error {
	return bcrypt.CompareHashAndPassword(hash, password)
}

// hashPassword bcrypt-hashes a plain-text password.
func hashPassword(plain string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}
