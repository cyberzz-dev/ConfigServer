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
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"github.com/alibaba/ilogtail/config_server/internal/config"
	"github.com/redis/go-redis/v9"
)

const (
	resetTokenTTL         = 30 * time.Minute
	resetTokenRedisPrefix = "cs_reset:"
)

// ── Reset-token store ─────────────────────────────────────────────────────────

// resetTokenStoreIface abstracts short-lived password-reset token storage.
// Tokens are single-use: consume() deletes the token and returns the username.
type resetTokenStoreIface interface {
	issue(ctx context.Context, username string) string
	consume(ctx context.Context, token string) (username string, ok bool)
}

// ── In-memory reset token store (allinone / no Redis) ────────────────────────

type resetEntry struct {
	username  string
	expiresAt time.Time
}

type memResetStore struct {
	mu   sync.Mutex
	data map[string]resetEntry
}

func (s *memResetStore) issue(_ context.Context, username string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	s.mu.Lock()
	s.data[token] = resetEntry{username: username, expiresAt: time.Now().Add(resetTokenTTL)}
	s.mu.Unlock()
	return token
}

func (s *memResetStore) consume(_ context.Context, token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[token]
	if !ok {
		return "", false
	}
	delete(s.data, token) // single-use
	if time.Now().After(e.expiresAt) {
		return "", false
	}
	return e.username, true
}

// ── Redis reset token store ───────────────────────────────────────────────────

type redisResetStore struct {
	rdb redis.UniversalClient
}

func (s *redisResetStore) issue(ctx context.Context, username string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	_ = s.rdb.Set(ctx, resetTokenRedisPrefix+token, username, resetTokenTTL).Err()
	return token
}

func (s *redisResetStore) consume(ctx context.Context, token string) (string, bool) {
	key := resetTokenRedisPrefix + token
	// Atomic GET+DEL: use a pipeline so the token cannot be reused concurrently.
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

// resetTokens is the active reset token store. Initialised in initResetStore.
var resetTokens resetTokenStoreIface = &memResetStore{data: make(map[string]resetEntry)}

// initResetStore switches to Redis-backed storage when a Redis client is available.
func initResetStore(rdb redis.UniversalClient) {
	if rdb != nil {
		resetTokens = &redisResetStore{rdb: rdb}
	}
}

// ── SMTP mail sender ──────────────────────────────────────────────────────────

// sendMail sends a plain-text email using the given SMTP config.
// Returns a non-nil error when SMTP is not configured (Host is empty).
func sendMail(cfg config.SMTPConfig, to, subject, body string) error {
	if cfg.Host == "" {
		return fmt.Errorf("SMTP not configured (smtp.host is empty)")
	}
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	msg := buildMessage(cfg.From, to, subject, body)

	var auth smtp.Auth
	if cfg.Username != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}

	if cfg.TLS {
		// Implicit TLS (port 465)
		tlsCfg := &tls.Config{ServerName: cfg.Host}
		conn, err := tls.Dial("tcp", addr, tlsCfg)
		if err != nil {
			return fmt.Errorf("smtp tls dial %s: %w", addr, err)
		}
		defer conn.Close()
		c, err := smtp.NewClient(conn, cfg.Host)
		if err != nil {
			return fmt.Errorf("smtp new client: %w", err)
		}
		defer c.Quit() //nolint:errcheck
		if auth != nil {
			if err := c.Auth(auth); err != nil {
				return fmt.Errorf("smtp auth: %w", err)
			}
		}
		if err := c.Mail(cfg.From); err != nil {
			return fmt.Errorf("smtp MAIL FROM: %w", err)
		}
		if err := c.Rcpt(to); err != nil {
			return fmt.Errorf("smtp RCPT TO: %w", err)
		}
		wc, err := c.Data()
		if err != nil {
			return fmt.Errorf("smtp DATA: %w", err)
		}
		if _, err = wc.Write(msg); err != nil {
			return fmt.Errorf("smtp write body: %w", err)
		}
		return wc.Close()
	}

	// STARTTLS / plain (port 587 / 25)
	return smtp.SendMail(addr, auth, cfg.From, []string{to}, msg)
}

func buildMessage(from, to, subject, body string) []byte {
	var sb strings.Builder
	sb.WriteString("From: " + from + "\r\n")
	sb.WriteString("To: " + to + "\r\n")
	sb.WriteString("Subject: " + subject + "\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(body)
	return []byte(sb.String())
}
