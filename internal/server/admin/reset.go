// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

package admin

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/alibaba/ilogtail/config_server/internal/config"
)

// ForgotPassword initiates a password-reset flow.
// It looks up the user by username, checks that an email is registered, issues
// a short-lived (30-min) single-use reset token, and sends it via SMTP.
// To prevent username enumeration the response is always 200 OK.
//
//	POST /api/v1/auth/forgot-password
//	Body: { "username": "alice" }
func (h *AdminHandler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body struct {
		Username string `json:"username"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, err.Error())
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	if body.Username == "" {
		badRequest(w, "username is required")
		return
	}

	// Generic response prevents username enumeration.
	const generic = "If that account exists and has an email address on file, a reset link has been sent."

	user, err := h.mgr.GetUser(ctx, body.Username)
	if err != nil || user == nil || user.Email == "" {
		ok(w, map[string]string{"message": generic})
		return
	}

	if h.smtp.Host == "" {
		// SMTP not configured: fall through so the response is always the same,
		// but log a warning so operators know the feature is incomplete.
		log.Printf("WARN: forgot-password for %q: SMTP not configured, reset email not sent", body.Username)
		ok(w, map[string]string{"message": generic})
		return
	}

	token := resetTokens.issue(ctx, body.Username)
	subject := "ConfigServer — password reset request"
	bodyText := buildResetEmailBody(body.Username, token, h.smtp.PublicURL)
	if err := sendMail(h.smtp, user.Email, subject, bodyText); err != nil {
		log.Printf("ERROR: forgot-password send mail to %q: %v", user.Email, err)
		// Still respond OK to avoid enumeration; token was issued but email failed.
		// The operator must investigate SMTP settings.
	}

	ok(w, map[string]string{"message": generic})
}

// ResetPassword applies a reset token and sets a new password.
//
//	POST /api/v1/auth/reset-password
//	Body: { "token": "...", "new_password": "...", "confirm_password": "..." }
func (h *AdminHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body struct {
		Token           string `json:"token"`
		NewPassword     string `json:"new_password"`
		ConfirmPassword string `json:"confirm_password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, err.Error())
		return
	}
	if body.Token == "" {
		badRequest(w, "token is required")
		return
	}
	if body.NewPassword == "" {
		badRequest(w, "new_password is required")
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

	username, valid := resetTokens.consume(ctx, body.Token)
	if !valid {
		writeJSON(w, http.StatusUnauthorized, 401, "reset token is invalid or has expired", nil)
		return
	}

	user, err := h.mgr.GetUser(ctx, username)
	if err != nil || user == nil {
		writeJSON(w, http.StatusInternalServerError, 500, "user not found", nil)
		return
	}

	hash, err := hashPassword(body.NewPassword)
	if err != nil {
		internalError(w, err)
		return
	}
	user.PasswordHash = hash
	if err := h.mgr.UpdateUser(ctx, user); err != nil {
		internalError(w, err)
		return
	}

	ok(w, map[string]string{"message": "password has been reset"})
}

// SetMyEmail lets an authenticated user update their own email address.
//
//	PUT /api/v1/me/email
//	Body: { "email": "alice@example.com" }
func (h *AdminHandler) SetMyEmail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uc, _ := userFromCtx(ctx)

	var body struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, err.Error())
		return
	}

	user, err := h.mgr.GetUser(ctx, uc.Username)
	if err != nil || user == nil {
		internalError(w, fmt.Errorf("get user: %w", err))
		return
	}
	user.Email = strings.TrimSpace(body.Email)
	if err := h.mgr.UpdateUser(ctx, user); err != nil {
		internalError(w, err)
		return
	}
	ok(w, map[string]string{"message": "email updated"})
}

// SetUserEmail lets an admin set the email for any user.
//
//	PUT /api/v1/users/{username}/email   (admin only)
//	Body: { "email": "..." }
func (h *AdminHandler) SetUserEmail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	username := r.PathValue("username")

	var body struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, err.Error())
		return
	}

	user, err := h.mgr.GetUser(ctx, username)
	if err != nil {
		if isNotFound(err) {
			notFound(w)
			return
		}
		internalError(w, err)
		return
	}
	user.Email = strings.TrimSpace(body.Email)
	if err := h.mgr.UpdateUser(ctx, user); err != nil {
		internalError(w, err)
		return
	}
	ok(w, map[string]string{"message": "email updated"})
}

// buildResetEmailBody composes the plain-text password-reset email.
func buildResetEmailBody(username, token, publicURL string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Hello %s,\r\n\r\n", username))
	sb.WriteString("We received a request to reset your ConfigServer password.\r\n\r\n")
	if publicURL != "" {
		link := strings.TrimRight(publicURL, "/") + "/reset-password?token=" + token
		sb.WriteString("Click the link below to set a new password (valid for 30 minutes):\r\n")
		sb.WriteString(link + "\r\n\r\n")
		sb.WriteString("Or copy-paste the reset token into the password-reset form:\r\n")
	} else {
		sb.WriteString("Use the following reset token in the password-reset form (valid for 30 minutes):\r\n")
	}
	sb.WriteString(token + "\r\n\r\n")
	sb.WriteString("If you did not request this, please ignore this email.\r\n")
	return sb.String()
}

// smtpFromConfig is a package-level accessor set by NewAdminHandler.
// Kept as a package variable so it can be injected once rather than threaded
// through every helper function.
var _ config.SMTPConfig // compile-time import reference
