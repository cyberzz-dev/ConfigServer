// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

// Package admin implements the REST API consumed by the admin WebUI and operators.
package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"

	"github.com/alibaba/ilogtail/config_server/internal/cache"
	"github.com/alibaba/ilogtail/config_server/internal/config"
	"github.com/alibaba/ilogtail/config_server/internal/model"
)

// AdminHandler provides the REST API consumed by the admin WebUI and operators.
type AdminHandler struct {
	mgr  *cache.Manager
	smtp config.SMTPConfig
}

// groupSnapshot is stored as history Detail when a group is deleted,
// enabling full restoration via rollback.
type groupSnapshot struct {
	Description       string                      `json:"description"`
	IPSelectorJSON    string                      `json:"ip_selector_json"`
	VersionConstraint string                      `json:"version_constraint"`
	Tags              []*model.AgentGroupTag      `json:"tags"`
	Configs           []*model.GroupConfigMapping `json:"configs"`
}

// NewAdminHandler creates an AdminHandler backed by the given cache manager.
func NewAdminHandler(mgr *cache.Manager, smtp config.SMTPConfig) *AdminHandler {
	return &AdminHandler{mgr: mgr, smtp: smtp}
}

// RegisterAdminRoutes wires all admin REST endpoints into mux.
func RegisterAdminRoutes(mux *http.ServeMux, h *AdminHandler) {
	// Pipeline configs
	mux.HandleFunc("GET /api/v1/pipeline-configs", h.ListPipelineConfigs)
	mux.HandleFunc("POST /api/v1/pipeline-configs", h.CreatePipelineConfig)
	mux.HandleFunc("GET /api/v1/pipeline-configs/{name}", h.GetPipelineConfig)
	mux.HandleFunc("PUT /api/v1/pipeline-configs/{name}", h.UpdatePipelineConfig)
	mux.HandleFunc("DELETE /api/v1/pipeline-configs/{name}", h.DeletePipelineConfig)

	// Instance configs
	mux.HandleFunc("GET /api/v1/instance-configs", h.ListInstanceConfigs)
	mux.HandleFunc("POST /api/v1/instance-configs", h.CreateInstanceConfig)
	mux.HandleFunc("GET /api/v1/instance-configs/{name}", h.GetInstanceConfig)
	mux.HandleFunc("PUT /api/v1/instance-configs/{name}", h.UpdateInstanceConfig)
	mux.HandleFunc("DELETE /api/v1/instance-configs/{name}", h.DeleteInstanceConfig)

	// Groups
	mux.HandleFunc("GET /api/v1/groups", h.ListGroups)
	mux.HandleFunc("POST /api/v1/groups", h.CreateGroup)
	mux.HandleFunc("GET /api/v1/groups/{name}", h.GetGroup)
	mux.HandleFunc("PUT /api/v1/groups/{name}", h.UpdateGroup)
	mux.HandleFunc("DELETE /api/v1/groups/{name}", h.DeleteGroup)

	// Group tags
	mux.HandleFunc("GET /api/v1/groups/{name}/tags", h.GetGroupTags)
	mux.HandleFunc("PUT /api/v1/groups/{name}/tags", h.SetGroupTags)
	mux.HandleFunc("GET /api/v1/groups/{name}/ip-selector", h.GetGroupIPSelector)
	mux.HandleFunc("PUT /api/v1/groups/{name}/ip-selector", h.SetGroupIPSelector)
	mux.HandleFunc("DELETE /api/v1/groups/{name}/ip-selector", h.DeleteGroupIPSelector)
	mux.HandleFunc("PUT /api/v1/groups/{name}/version-constraint", h.SetGroupVersionConstraint)

	// Group ↔ config associations
	mux.HandleFunc("GET /api/v1/groups/{name}/configs", h.GetGroupConfigs)
	mux.HandleFunc("PUT /api/v1/groups/{name}/configs/{type}/{configName}", h.AddGroupConfig)
	mux.HandleFunc("DELETE /api/v1/groups/{name}/configs/{type}/{configName}", h.RemoveGroupConfig)

	// Agents (read-only)
	mux.HandleFunc("GET /api/v1/agents", h.ListAgents)
	mux.HandleFunc("GET /api/v1/agents/{instanceID}", h.GetAgent)

	// Onetime commands
	mux.HandleFunc("GET /api/v1/onetime-commands", h.ListOnetimeCommands)
	mux.HandleFunc("POST /api/v1/onetime-commands", h.CreateOnetimeCommand)
	mux.HandleFunc("GET /api/v1/onetime-commands/{name}", h.GetOnetimeCommand)
	mux.HandleFunc("DELETE /api/v1/onetime-commands/{name}", h.DeleteOnetimeCommand)

	// Auth (public — no session required)
	mux.HandleFunc("GET /api/v1/auth/status", h.AuthStatus)
	mux.HandleFunc("POST /api/v1/auth/init", h.AuthInit)
	mux.HandleFunc("POST /api/v1/auth/login", h.AuthLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", h.AuthLogout)
	mux.HandleFunc("POST /api/v1/auth/forgot-password", h.ForgotPassword)
	mux.HandleFunc("POST /api/v1/auth/reset-password", h.ResetPassword)
	mux.HandleFunc("POST /api/v1/auth/login/otp", h.OTPLoginStep)

	// Auth (requires session — protected by requireAuth middleware)
	mux.HandleFunc("POST /api/v1/auth/change-password", h.AuthChangePassword)
	mux.HandleFunc("POST /api/v1/auth/otp/setup", h.OTPSetup)
	mux.HandleFunc("POST /api/v1/auth/otp/enable", h.OTPEnable)
	mux.HandleFunc("POST /api/v1/auth/otp/disable", h.OTPDisable)

	// Current user info (any authenticated user)
	mux.HandleFunc("GET /api/v1/me", h.GetMe)
	mux.HandleFunc("PUT /api/v1/me/email", h.SetMyEmail)

	// User management (admin only)
	mux.Handle("GET /api/v1/users", requireAdmin(http.HandlerFunc(h.ListUsers)))
	mux.Handle("POST /api/v1/users", requireAdmin(http.HandlerFunc(h.CreateUser)))
	mux.Handle("DELETE /api/v1/users/{username}", requireAdmin(http.HandlerFunc(h.DeleteUser)))
	mux.Handle("PUT /api/v1/users/{username}/password", requireAdmin(http.HandlerFunc(h.ResetUserPassword)))
	mux.Handle("PUT /api/v1/users/{username}/role", requireAdmin(http.HandlerFunc(h.AssignUserRole)))
	mux.Handle("PUT /api/v1/users/{username}/email", requireAdmin(http.HandlerFunc(h.SetUserEmail)))

	// Role management (admin only)
	mux.Handle("GET /api/v1/roles", requireAdmin(http.HandlerFunc(h.ListRoles)))
	mux.Handle("POST /api/v1/roles", requireAdmin(http.HandlerFunc(h.CreateRole)))
	mux.Handle("DELETE /api/v1/roles/{name}", requireAdmin(http.HandlerFunc(h.DeleteRole)))
	mux.Handle("GET /api/v1/roles/{name}/permissions", requireAdmin(http.HandlerFunc(h.GetRolePermissions)))
	mux.Handle("PUT /api/v1/roles/{name}/permissions", requireAdmin(http.HandlerFunc(h.SetRolePermissions)))

	// Config history & rollback
	mux.HandleFunc("GET /api/v1/history/{type}/{name}", h.ListConfigHistory)
	mux.HandleFunc("GET /api/v1/deleted-history/{type}", h.ListDeletedHistory)
	mux.HandleFunc("POST /api/v1/history/{type}/{name}/{id}/rollback", h.RollbackConfig)

	// Audit logs
	mux.HandleFunc("GET /api/v1/audit-logs", h.ListAuditLogs)

	// Canary releases
	mux.HandleFunc("GET /api/v1/canaries", h.ListCanaries)
	mux.HandleFunc("POST /api/v1/configs/{type}/{name}/canary", h.CreateCanary)
	mux.HandleFunc("PUT /api/v1/configs/{type}/{name}/canary", h.UpdateCanary)
	mux.HandleFunc("GET /api/v1/configs/{type}/{name}/canary", h.GetCanary)
	mux.HandleFunc("PUT /api/v1/configs/{type}/{name}/canary/percent", h.SetCanaryPercent)
	mux.HandleFunc("POST /api/v1/configs/{type}/{name}/canary/pause", h.PauseCanary)
	mux.HandleFunc("POST /api/v1/configs/{type}/{name}/canary/resume", h.ResumeCanary)
	mux.HandleFunc("POST /api/v1/configs/{type}/{name}/canary/promote", h.PromoteCanary)
	mux.HandleFunc("POST /api/v1/configs/{type}/{name}/canary/abort", h.AbortCanary)
	mux.HandleFunc("GET /api/v1/configs/{type}/{name}/canary/stats", h.GetCanaryStats)
	mux.HandleFunc("GET /api/v1/configs/{type}/{name}/canary/agents", h.GetCanaryAgents)
}

// ── JSON envelope ─────────────────────────────────────────────────────────────

type apiResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, code int, msg string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiResp{Code: code, Message: msg, Data: data})
}

func ok(w http.ResponseWriter, data any) { writeJSON(w, http.StatusOK, 0, "ok", data) }

func badRequest(w http.ResponseWriter, msg string) { writeJSON(w, http.StatusBadRequest, 1, msg, nil) }

func conflict(w http.ResponseWriter, msg string) { writeJSON(w, http.StatusConflict, 1, msg, nil) }

func notFound(w http.ResponseWriter) { writeJSON(w, http.StatusNotFound, 1, "not found", nil) }

func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

func internalError(w http.ResponseWriter, err error) {
	log.Printf("ERROR: %v", err)
	writeJSON(w, http.StatusInternalServerError, 1, "internal error", nil)
}

func isNotFound(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }

// configResp is the JSON representation of a stored config.
// Detail is returned as a plain string (not base64) to simplify WebUI rendering.
type configResp struct {
	Name      string `json:"name"`
	Version   int64  `json:"version"`
	Detail    string `json:"detail"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func pipelineToResp(c *model.PipelineConfig) configResp {
	return configResp{
		Name:      c.Name,
		Version:   c.Version,
		Detail:    string(c.Detail),
		CreatedAt: c.CreatedAt.Format(time.RFC3339),
		UpdatedAt: c.UpdatedAt.Format(time.RFC3339),
	}
}

func instanceToResp(c *model.InstanceConfig) configResp {
	return configResp{
		Name:      c.Name,
		Version:   c.Version,
		Detail:    string(c.Detail),
		CreatedAt: c.CreatedAt.Format(time.RFC3339),
		UpdatedAt: c.UpdatedAt.Format(time.RFC3339),
	}
}

// validateYAML checks that content is non-empty and valid YAML.
func validateYAML(content []byte) error {
	if len(content) == 0 {
		return fmt.Errorf("detail must not be empty")
	}
	var v any
	if err := yaml.Unmarshal(content, &v); err != nil {
		return fmt.Errorf("invalid YAML: %w", err)
	}
	if v == nil {
		return fmt.Errorf("detail must not be empty")
	}
	return nil
}

// ── Pipeline config handlers ───────────────────────────────────────────────────

func (h *AdminHandler) ListPipelineConfigs(w http.ResponseWriter, r *http.Request) {
	cfgs, err := h.mgr.ListPipelineConfigs(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	resps := make([]configResp, len(cfgs))
	for i, c := range cfgs {
		resps[i] = pipelineToResp(c)
	}
	ok(w, resps)
}

func (h *AdminHandler) CreatePipelineConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Detail string `json:"detail"`
	}
	if err := readJSON(r, &req); err != nil {
		badRequest(w, err.Error())
		return
	}
	if req.Name == "" {
		badRequest(w, "name is required")
		return
	}
	if err := validateYAML([]byte(req.Detail)); err != nil {
		badRequest(w, err.Error())
		return
	}
	if existing, err := h.mgr.GetPipelineConfig(r.Context(), req.Name); err == nil {
		if bytes.Equal(existing.Detail, []byte(req.Detail)) {
			ok(w, pipelineToResp(existing))
			return
		}
		conflict(w, fmt.Sprintf("pipeline config %q already exists", req.Name))
		return
	} else if !isNotFound(err) {
		internalError(w, err)
		return
	}
	cfg := &model.PipelineConfig{
		Name:    req.Name,
		Version: time.Now().UnixMilli(),
		Detail:  []byte(req.Detail),
	}
	if err := h.mgr.CreatePipelineConfig(r.Context(), cfg); err != nil {
		if existing, err2 := h.mgr.GetPipelineConfig(r.Context(), req.Name); err2 == nil {
			if bytes.Equal(existing.Detail, []byte(req.Detail)) {
				ok(w, pipelineToResp(existing))
				return
			}
			conflict(w, fmt.Sprintf("pipeline config %q already exists", req.Name))
			return
		}
		internalError(w, err)
		return
	}
	h.saveHistory(r.Context(), "pipeline", cfg.Name, "create", cfg.Version, cfg.Detail)
	h.logAudit(r, http.StatusCreated, "create", "pipeline", cfg.Name, req.Detail)
	writeJSON(w, http.StatusCreated, 0, "created", pipelineToResp(cfg))
}

func (h *AdminHandler) GetPipelineConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfg, err := h.mgr.GetPipelineConfig(r.Context(), name)
	if err != nil {
		if isNotFound(err) {
			notFound(w)
			return
		}
		internalError(w, err)
		return
	}
	ok(w, pipelineToResp(cfg))
}

func (h *AdminHandler) UpdatePipelineConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		Detail string `json:"detail"`
	}
	if err := readJSON(r, &req); err != nil {
		badRequest(w, err.Error())
		return
	}
	if err := validateYAML([]byte(req.Detail)); err != nil {
		badRequest(w, err.Error())
		return
	}
	cfg, err := h.mgr.GetPipelineConfig(r.Context(), name)
	if err != nil {
		if isNotFound(err) {
			notFound(w)
			return
		}
		internalError(w, err)
		return
	}
	oldDetail := cfg.Detail
	if bytes.Equal(oldDetail, []byte(req.Detail)) {
		ok(w, pipelineToResp(cfg))
		return
	}
	cfg.Detail = []byte(req.Detail)
	cfg.Version = time.Now().UnixMilli()
	if err := h.mgr.UpdatePipelineConfig(r.Context(), cfg); err != nil {
		internalError(w, err)
		return
	}
	h.saveHistory(r.Context(), "pipeline", cfg.Name, "update", cfg.Version, oldDetail)
	h.logAudit(r, http.StatusOK, "update", "pipeline", cfg.Name, req.Detail)
	ok(w, pipelineToResp(cfg))
}

func (h *AdminHandler) DeletePipelineConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	// Snapshot content before deletion so it can be restored via rollback.
	cfg, err := h.mgr.GetPipelineConfig(r.Context(), name)
	if err != nil {
		if isNotFound(err) {
			ok(w, nil)
			return
		}
		internalError(w, err)
		return
	}
	snapDetail := cfg.Detail
	if err := h.mgr.DeletePipelineConfig(r.Context(), name); err != nil {
		internalError(w, err)
		return
	}
	h.saveHistory(r.Context(), "pipeline", name, "delete", 0, snapDetail)
	h.logAudit(r, http.StatusOK, "delete", "pipeline", name, "")
	ok(w, nil)
}

// ── Instance config handlers ───────────────────────────────────────────────────

func (h *AdminHandler) ListInstanceConfigs(w http.ResponseWriter, r *http.Request) {
	cfgs, err := h.mgr.ListInstanceConfigs(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	resps := make([]configResp, len(cfgs))
	for i, c := range cfgs {
		resps[i] = instanceToResp(c)
	}
	ok(w, resps)
}

func (h *AdminHandler) CreateInstanceConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Detail string `json:"detail"`
	}
	if err := readJSON(r, &req); err != nil {
		badRequest(w, err.Error())
		return
	}
	if req.Name == "" {
		badRequest(w, "name is required")
		return
	}
	if err := validateYAML([]byte(req.Detail)); err != nil {
		badRequest(w, err.Error())
		return
	}
	if existing, err := h.mgr.GetInstanceConfig(r.Context(), req.Name); err == nil {
		if bytes.Equal(existing.Detail, []byte(req.Detail)) {
			ok(w, instanceToResp(existing))
			return
		}
		conflict(w, fmt.Sprintf("instance config %q already exists", req.Name))
		return
	} else if !isNotFound(err) {
		internalError(w, err)
		return
	}
	cfg := &model.InstanceConfig{
		Name:    req.Name,
		Version: time.Now().UnixMilli(),
		Detail:  []byte(req.Detail),
	}
	if err := h.mgr.CreateInstanceConfig(r.Context(), cfg); err != nil {
		if existing, err2 := h.mgr.GetInstanceConfig(r.Context(), req.Name); err2 == nil {
			if bytes.Equal(existing.Detail, []byte(req.Detail)) {
				ok(w, instanceToResp(existing))
				return
			}
			conflict(w, fmt.Sprintf("instance config %q already exists", req.Name))
			return
		}
		internalError(w, err)
		return
	}
	h.saveHistory(r.Context(), "instance", cfg.Name, "create", cfg.Version, cfg.Detail)
	h.logAudit(r, http.StatusCreated, "create", "instance", cfg.Name, req.Detail)
	writeJSON(w, http.StatusCreated, 0, "created", instanceToResp(cfg))
}

func (h *AdminHandler) GetInstanceConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfg, err := h.mgr.GetInstanceConfig(r.Context(), name)
	if err != nil {
		if isNotFound(err) {
			notFound(w)
			return
		}
		internalError(w, err)
		return
	}
	ok(w, instanceToResp(cfg))
}

func (h *AdminHandler) UpdateInstanceConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		Detail string `json:"detail"`
	}
	if err := readJSON(r, &req); err != nil {
		badRequest(w, err.Error())
		return
	}
	if err := validateYAML([]byte(req.Detail)); err != nil {
		badRequest(w, err.Error())
		return
	}
	cfg, err := h.mgr.GetInstanceConfig(r.Context(), name)
	if err != nil {
		if isNotFound(err) {
			notFound(w)
			return
		}
		internalError(w, err)
		return
	}
	oldDetail := cfg.Detail
	if bytes.Equal(oldDetail, []byte(req.Detail)) {
		ok(w, instanceToResp(cfg))
		return
	}
	cfg.Detail = []byte(req.Detail)
	cfg.Version = time.Now().UnixMilli()
	if err := h.mgr.UpdateInstanceConfig(r.Context(), cfg); err != nil {
		internalError(w, err)
		return
	}
	h.saveHistory(r.Context(), "instance", cfg.Name, "update", cfg.Version, oldDetail)
	h.logAudit(r, http.StatusOK, "update", "instance", cfg.Name, req.Detail)
	ok(w, instanceToResp(cfg))
}

func (h *AdminHandler) DeleteInstanceConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	// Snapshot content before deletion so it can be restored via rollback.
	cfg, err := h.mgr.GetInstanceConfig(r.Context(), name)
	if err != nil {
		if isNotFound(err) {
			ok(w, nil)
			return
		}
		internalError(w, err)
		return
	}
	snapDetail := cfg.Detail
	if err := h.mgr.DeleteInstanceConfig(r.Context(), name); err != nil {
		internalError(w, err)
		return
	}
	h.saveHistory(r.Context(), "instance", name, "delete", 0, snapDetail)
	h.logAudit(r, http.StatusOK, "delete", "instance", name, "")
	ok(w, nil)
}

// ── Group handlers ────────────────────────────────────────────────────────────

func (h *AdminHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.mgr.ListGroups(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	ok(w, groups)
}

func (h *AdminHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	var g model.AgentGroup
	if err := readJSON(r, &g); err != nil {
		badRequest(w, err.Error())
		return
	}
	if g.Name == "" {
		badRequest(w, "name is required")
		return
	}
	if g.Name == model.DefaultGroupName {
		badRequest(w, fmt.Sprintf("%q is a reserved built-in group", model.DefaultGroupName))
		return
	}
	if g.IPSelectorJSON != "" {
		selector, err := model.ParseIPSelectorJSON(g.IPSelectorJSON)
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		g.IPSelectorJSON, _ = model.MarshalIPSelector(selector)
	}
	if err := model.ValidateVersionConstraint(g.VersionConstraint); err != nil {
		badRequest(w, "invalid version_constraint: "+err.Error())
		return
	}
	if err := h.mgr.CreateGroup(r.Context(), &g); err != nil {
		internalError(w, err)
		return
	}
	h.saveHistory(r.Context(), "group", g.Name, "create", 0, nil)
	h.logAudit(r, http.StatusCreated, "create", "group", g.Name, fmt.Sprintf("description=%q", g.Description))
	writeJSON(w, http.StatusCreated, 0, "created", g)
}

func (h *AdminHandler) GetGroup(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	g, err := h.mgr.GetGroup(r.Context(), name)
	if err != nil {
		if isNotFound(err) {
			notFound(w)
			return
		}
		internalError(w, err)
		return
	}
	ok(w, g)
}

func (h *AdminHandler) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		Description string `json:"description"`
	}
	if err := readJSON(r, &req); err != nil {
		badRequest(w, err.Error())
		return
	}
	g, err := h.mgr.GetGroup(r.Context(), name)
	if err != nil {
		if isNotFound(err) {
			notFound(w)
			return
		}
		internalError(w, err)
		return
	}
	g.Description = req.Description
	if err := h.mgr.UpdateGroup(r.Context(), g); err != nil {
		internalError(w, err)
		return
	}
	h.saveHistory(r.Context(), "group", name, "update", 0, nil)
	h.logAudit(r, http.StatusOK, "update", "group", name, fmt.Sprintf("description=%q", req.Description))
	ok(w, g)
}

func (h *AdminHandler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == model.DefaultGroupName {
		badRequest(w, fmt.Sprintf("%q is a built-in group and cannot be deleted", model.DefaultGroupName))
		return
	}
	// Snapshot the full group state before deletion so rollback can recreate it.
	var snap groupSnapshot
	if g, err := h.mgr.GetGroup(r.Context(), name); err == nil {
		snap.Description = g.Description
		snap.IPSelectorJSON = g.IPSelectorJSON
		snap.VersionConstraint = g.VersionConstraint
	}
	snap.Tags, _ = h.mgr.GetGroupTags(r.Context(), name)
	snap.Configs, _ = h.mgr.GetGroupConfigs(r.Context(), name)
	snapJSON, _ := json.Marshal(snap)

	if err := h.mgr.DeleteGroup(r.Context(), name); err != nil {
		internalError(w, err)
		return
	}
	h.saveHistory(r.Context(), "group", name, "delete", 0, snapJSON)
	h.logAudit(r, http.StatusOK, "delete", "group", name, "")
	ok(w, nil)
}

// ── Group tag handlers ────────────────────────────────────────────────────────

func (h *AdminHandler) GetGroupIPSelector(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	group, err := h.mgr.GetGroup(r.Context(), name)
	if err != nil {
		if isNotFound(err) {
			notFound(w)
			return
		}
		internalError(w, err)
		return
	}
	selector, err := model.ParseIPSelectorJSON(group.IPSelectorJSON)
	if err != nil {
		internalError(w, err)
		return
	}
	ok(w, selector)
}

func (h *AdminHandler) SetGroupIPSelector(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var selector model.AgentGroupIPSelector
	if err := readJSON(r, &selector); err != nil {
		badRequest(w, err.Error())
		return
	}
	selectorJSON, err := model.MarshalIPSelector(selector)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	group, err := h.mgr.GetGroup(r.Context(), name)
	if err != nil {
		if isNotFound(err) {
			notFound(w)
			return
		}
		internalError(w, err)
		return
	}
	oldJSON := []byte(group.IPSelectorJSON)
	group.IPSelectorJSON = selectorJSON
	if err := h.mgr.UpdateGroup(r.Context(), group); err != nil {
		internalError(w, err)
		return
	}
	summary := selectorJSON
	if summary == "" {
		summary = "(cleared)"
	}
	h.saveHistory(r.Context(), "group", name, "set_ip_selector", 0, oldJSON)
	h.logAudit(r, http.StatusOK, "set_ip_selector", "group", name, summary)
	ok(w, selector)
}

func (h *AdminHandler) DeleteGroupIPSelector(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	group, err := h.mgr.GetGroup(r.Context(), name)
	if err != nil {
		if isNotFound(err) {
			notFound(w)
			return
		}
		internalError(w, err)
		return
	}
	oldJSON := []byte(group.IPSelectorJSON)
	group.IPSelectorJSON = ""
	if err := h.mgr.UpdateGroup(r.Context(), group); err != nil {
		internalError(w, err)
		return
	}
	h.saveHistory(r.Context(), "group", name, "set_ip_selector", 0, oldJSON)
	h.logAudit(r, http.StatusOK, "set_ip_selector", "group", name, "(cleared)")
	ok(w, model.AgentGroupIPSelector{IPs: []string{}})
}

func (h *AdminHandler) SetGroupVersionConstraint(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		VersionConstraint string `json:"version_constraint"`
	}
	if err := readJSON(r, &req); err != nil {
		badRequest(w, err.Error())
		return
	}
	if err := model.ValidateVersionConstraint(req.VersionConstraint); err != nil {
		badRequest(w, "invalid version_constraint: "+err.Error())
		return
	}
	g, err := h.mgr.GetGroup(r.Context(), name)
	if err != nil {
		if isNotFound(err) {
			notFound(w)
			return
		}
		internalError(w, err)
		return
	}
	old := g.VersionConstraint
	g.VersionConstraint = req.VersionConstraint
	if err := h.mgr.UpdateGroup(r.Context(), g); err != nil {
		internalError(w, err)
		return
	}
	h.saveHistory(r.Context(), "group", name, "set_version_constraint", 0, []byte(old))
	h.logAudit(r, http.StatusOK, "set_version_constraint", "group", name, req.VersionConstraint)
	ok(w, g)
}

func (h *AdminHandler) GetGroupTags(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	tags, err := h.mgr.GetGroupTags(r.Context(), name)
	if err != nil {
		internalError(w, err)
		return
	}
	ok(w, tags)
}

func (h *AdminHandler) SetGroupTags(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var tags []*model.AgentGroupTag
	if err := readJSON(r, &tags); err != nil {
		badRequest(w, err.Error())
		return
	}
	// Capture old tags before overwriting so they can be restored via rollback.
	oldTags, _ := h.mgr.GetGroupTags(r.Context(), name)
	oldJSON, _ := json.Marshal(oldTags)

	if err := h.mgr.SetGroupTags(r.Context(), name, tags); err != nil {
		internalError(w, err)
		return
	}
	parts := make([]string, 0, len(tags))
	for _, t := range tags {
		parts = append(parts, t.TagName+"="+t.TagValue)
	}
	newSummary := strings.Join(parts, ", ")
	if newSummary == "" {
		newSummary = "(cleared)"
	}
	h.saveHistory(r.Context(), "group", name, "set_tags", 0, oldJSON)
	h.logAudit(r, http.StatusOK, "set_tags", "group", name, newSummary)
	ok(w, tags)
}

// ── Group ↔ config association handlers ───────────────────────────────────────

func (h *AdminHandler) GetGroupConfigs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfgs, err := h.mgr.GetGroupConfigs(r.Context(), name)
	if err != nil {
		internalError(w, err)
		return
	}
	ok(w, cfgs)
}

func (h *AdminHandler) AddGroupConfig(w http.ResponseWriter, r *http.Request) {
	groupName := r.PathValue("name")
	configType := r.PathValue("type")
	configName := r.PathValue("configName")

	if configType != model.ConfigTypePipeline && configType != model.ConfigTypeInstance && configType != model.ConfigTypeOnetime {
		badRequest(w, "type must be pipeline, instance, or onetime")
		return
	}

	if err := h.mgr.AddGroupConfig(r.Context(), &model.GroupConfigMapping{
		GroupName:  groupName,
		ConfigName: configName,
		ConfigType: configType,
	}); err != nil {
		internalError(w, err)
		return
	}
	detailRef := configType + "/" + configName
	h.saveHistory(r.Context(), "group", groupName, "add_config", 0, []byte(detailRef))
	h.logAudit(r, http.StatusOK, "add_config", "group", groupName, detailRef)
	ok(w, nil)
}

func (h *AdminHandler) RemoveGroupConfig(w http.ResponseWriter, r *http.Request) {
	groupName := r.PathValue("name")
	configType := r.PathValue("type")
	configName := r.PathValue("configName")

	if err := h.mgr.RemoveGroupConfig(r.Context(), groupName, configName, configType); err != nil {
		internalError(w, err)
		return
	}
	detailRef := configType + "/" + configName
	h.saveHistory(r.Context(), "group", groupName, "remove_config", 0, []byte(detailRef))
	h.logAudit(r, http.StatusOK, "remove_config", "group", groupName, detailRef)
	ok(w, nil)
}

// ── Agent handlers ────────────────────────────────────────────────────────────

// listAgentsResp is the paginated response for GET /api/v1/agents.
type listAgentsResp struct {
	Agents   []*model.Agent `json:"agents"`
	Total    int            `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
}

func (h *AdminHandler) ListAgents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	group := q.Get("group")
	search := q.Get("search")

	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(q.Get("page_size"))
	if pageSize < 1 || pageSize > 500 {
		pageSize = 50
	}

	agents, total, err := h.mgr.ListAgentsPaged(r.Context(), group, page, pageSize, search)
	if err != nil {
		internalError(w, err)
		return
	}
	if agents == nil {
		agents = []*model.Agent{}
	}
	ok(w, listAgentsResp{
		Agents:   agents,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	})
}

func (h *AdminHandler) GetAgent(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("instanceID")
	agent, err := h.mgr.GetAgent(r.Context(), instanceID)
	if err != nil {
		if isNotFound(err) {
			notFound(w)
			return
		}
		internalError(w, err)
		return
	}
	statuses, _ := h.mgr.GetAgentConfigStatuses(r.Context(), instanceID)
	ok(w, map[string]any{"agent": agent, "config_statuses": statuses})
}

// ── Onetime command handlers ──────────────────────────────────────────────────

type onetimeResp struct {
	Name       string `json:"name"`
	Version    int64  `json:"version"`
	Detail     string `json:"detail"`
	ExpireTime int64  `json:"expire_time"`
	CreatedAt  string `json:"created_at"`
}

func onetimeToResp(c *model.OnetimeCommand) onetimeResp {
	return onetimeResp{
		Name:       c.Name,
		Version:    c.Version,
		Detail:     string(c.Detail),
		ExpireTime: c.ExpireTime,
		CreatedAt:  c.CreatedAt.Format(time.RFC3339),
	}
}

func (h *AdminHandler) ListOnetimeCommands(w http.ResponseWriter, r *http.Request) {
	cmds, err := h.mgr.ListOnetimeCommands(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	resps := make([]onetimeResp, len(cmds))
	for i, c := range cmds {
		resps[i] = onetimeToResp(c)
	}
	ok(w, resps)
}

func (h *AdminHandler) CreateOnetimeCommand(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"name"`
		Detail     string `json:"detail"`
		ExpireTime int64  `json:"expire_time"`
	}
	if err := readJSON(r, &req); err != nil {
		badRequest(w, err.Error())
		return
	}
	if req.Name == "" {
		badRequest(w, "name is required")
		return
	}
	if existing, err := h.mgr.GetOnetimeCommand(r.Context(), req.Name); err == nil {
		if bytes.Equal(existing.Detail, []byte(req.Detail)) && existing.ExpireTime == req.ExpireTime {
			ok(w, onetimeToResp(existing))
			return
		}
		conflict(w, fmt.Sprintf("onetime command %q already exists", req.Name))
		return
	} else if !isNotFound(err) {
		internalError(w, err)
		return
	}
	cmd := &model.OnetimeCommand{
		Name:       req.Name,
		Version:    time.Now().UnixMilli(),
		Detail:     []byte(req.Detail),
		ExpireTime: req.ExpireTime,
	}
	if err := h.mgr.CreateOnetimeCommand(r.Context(), cmd); err != nil {
		if existing, err2 := h.mgr.GetOnetimeCommand(r.Context(), req.Name); err2 == nil {
			if bytes.Equal(existing.Detail, []byte(req.Detail)) && existing.ExpireTime == req.ExpireTime {
				ok(w, onetimeToResp(existing))
				return
			}
			conflict(w, fmt.Sprintf("onetime command %q already exists", req.Name))
			return
		}
		internalError(w, err)
		return
	}
	h.saveHistory(r.Context(), "onetime", cmd.Name, "create", 0, cmd.Detail)
	h.logAudit(r, http.StatusCreated, "create", "onetime", cmd.Name, req.Detail)
	writeJSON(w, http.StatusCreated, 0, "created", onetimeToResp(cmd))
}

func (h *AdminHandler) GetOnetimeCommand(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cmd, err := h.mgr.GetOnetimeCommand(r.Context(), name)
	if err != nil {
		if isNotFound(err) {
			notFound(w)
			return
		}
		internalError(w, err)
		return
	}
	ok(w, onetimeToResp(cmd))
}

func (h *AdminHandler) DeleteOnetimeCommand(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	// Snapshot content before deletion so it can be restored via rollback.
	cmd, err := h.mgr.GetOnetimeCommand(r.Context(), name)
	if err != nil {
		if isNotFound(err) {
			ok(w, nil)
			return
		}
		internalError(w, err)
		return
	}
	snapDetail, _ := json.Marshal(struct {
		Detail     string `json:"detail"`
		ExpireTime int64  `json:"expire_time"`
	}{Detail: string(cmd.Detail), ExpireTime: cmd.ExpireTime})
	if err := h.mgr.DeleteOnetimeCommand(r.Context(), name); err != nil {
		internalError(w, err)
		return
	}
	h.saveHistory(r.Context(), "onetime", name, "delete", 0, snapDetail)
	h.logAudit(r, http.StatusOK, "delete", "onetime", name, "")
	ok(w, nil)
}

// ── User management handlers ──────────────────────────────────────────────────

// GetMe returns the current user's info and effective role permissions.
//
//	GET /api/v1/me
func (h *AdminHandler) GetMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uc, _ := userFromCtx(ctx)

	type meResp struct {
		Username    string                  `json:"username"`
		IsAdmin     bool                    `json:"is_admin"`
		RoleName    string                  `json:"role_name"`
		Permissions []*model.RolePermission `json:"permissions"`
	}

	var perms []*model.RolePermission
	var roleName string
	if !uc.IsAdmin {
		user, err := h.mgr.GetUser(ctx, uc.Username)
		if err != nil {
			internalError(w, err)
			return
		}
		if user != nil && user.RoleName != "" {
			roleName = user.RoleName
			perms, err = h.mgr.GetRolePermissions(ctx, roleName)
			if err != nil {
				internalError(w, err)
				return
			}
		}
	}
	ok(w, meResp{Username: uc.Username, IsAdmin: uc.IsAdmin, RoleName: roleName, Permissions: perms})
}

// ListUsers returns all user accounts (admin only).
//
//	GET /api/v1/users
func (h *AdminHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.mgr.ListUsers(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	type userResp struct {
		Username  string `json:"username"`
		IsAdmin   bool   `json:"is_admin"`
		RoleName  string `json:"role_name"`
		UpdatedAt string `json:"updated_at"`
	}
	out := make([]userResp, 0, len(users))
	for _, u := range users {
		out = append(out, userResp{
			Username:  u.Username,
			IsAdmin:   u.IsAdmin,
			RoleName:  u.RoleName,
			UpdatedAt: u.UpdatedAt.Format(time.RFC3339),
		})
	}
	ok(w, out)
}

// CreateUser creates a new non-admin user (admin only).
//
//	POST /api/v1/users
func (h *AdminHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		RoleName string `json:"role_name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, err.Error())
		return
	}
	if body.Username == "" {
		badRequest(w, "username is required")
		return
	}
	if body.Username == "admin" {
		badRequest(w, "reserved username")
		return
	}
	if len(body.Password) < 8 {
		badRequest(w, "password must be at least 8 characters")
		return
	}

	existing, err := h.mgr.GetUser(ctx, body.Username)
	if err != nil {
		internalError(w, err)
		return
	}
	if existing != nil {
		writeJSON(w, http.StatusConflict, 1, "username already exists", nil)
		return
	}

	// Validate role name if provided.
	if body.RoleName != "" {
		role, err := h.mgr.GetRole(ctx, body.RoleName)
		if err != nil {
			internalError(w, err)
			return
		}
		if role == nil {
			badRequest(w, "role not found")
			return
		}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcryptCost)
	if err != nil {
		internalError(w, err)
		return
	}

	user := &model.User{
		Username:     body.Username,
		PasswordHash: string(hash),
		IsAdmin:      false,
		RoleName:     body.RoleName,
	}
	if err := h.mgr.CreateUser(ctx, user); err != nil {
		internalError(w, err)
		return
	}
	ok(w, map[string]string{"username": body.Username})
}

// DeleteUser deletes a non-admin user (admin only). The "admin" account cannot be deleted.
//
//	DELETE /api/v1/users/{username}
func (h *AdminHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	if username == "admin" {
		badRequest(w, "cannot delete the admin account")
		return
	}
	if err := h.mgr.DeleteUser(r.Context(), username); err != nil {
		internalError(w, err)
		return
	}
	ok(w, nil)
}

// ResetUserPassword resets a user's password (admin only).
// An admin may not reset their own password via this endpoint; they must use
// POST /api/v1/auth/change-password which requires the current password.
//
//	PUT /api/v1/users/{username}/password
func (h *AdminHandler) ResetUserPassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	username := r.PathValue("username")

	// Block self-reset: the caller must use the change-password flow instead.
	if caller, ok := userFromCtx(ctx); ok && caller.Username == username {
		writeJSON(w, http.StatusForbidden, 403,
			"cannot reset your own password via this endpoint; use Change Password instead", nil)
		return
	}

	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, err.Error())
		return
	}
	if len(body.Password) < 8 {
		badRequest(w, "password must be at least 8 characters")
		return
	}

	user, err := h.mgr.GetUser(ctx, username)
	if err != nil {
		internalError(w, err)
		return
	}
	if user == nil {
		notFound(w)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcryptCost)
	if err != nil {
		internalError(w, err)
		return
	}
	user.PasswordHash = string(hash)
	if err := h.mgr.UpdateUser(ctx, user); err != nil {
		internalError(w, err)
		return
	}
	ok(w, nil)
}

// AssignUserRole assigns (or removes) a role from a user (admin only).
//
//	PUT /api/v1/users/{username}/role
func (h *AdminHandler) AssignUserRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	username := r.PathValue("username")
	if username == "admin" {
		badRequest(w, "cannot change role for the admin account")
		return
	}

	var body struct {
		RoleName string `json:"role_name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, err.Error())
		return
	}

	user, err := h.mgr.GetUser(ctx, username)
	if err != nil {
		internalError(w, err)
		return
	}
	if user == nil {
		notFound(w)
		return
	}

	// Validate role name if non-empty.
	if body.RoleName != "" {
		role, err := h.mgr.GetRole(ctx, body.RoleName)
		if err != nil {
			internalError(w, err)
			return
		}
		if role == nil {
			badRequest(w, "role not found")
			return
		}
	}

	user.RoleName = body.RoleName
	if err := h.mgr.UpdateUser(ctx, user); err != nil {
		internalError(w, err)
		return
	}
	ok(w, nil)
}

// ── Role management handlers ──────────────────────────────────────────────────

// ListRoles returns all roles (admin only).
//
//	GET /api/v1/roles
func (h *AdminHandler) ListRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := h.mgr.ListRoles(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	type roleResp struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		UpdatedAt   string `json:"updated_at"`
	}
	out := make([]roleResp, 0, len(roles))
	for _, ro := range roles {
		out = append(out, roleResp{
			Name:        ro.Name,
			Description: ro.Description,
			UpdatedAt:   ro.UpdatedAt.Format(time.RFC3339),
		})
	}
	ok(w, out)
}

// CreateRole creates a new role (admin only).
//
//	POST /api/v1/roles
func (h *AdminHandler) CreateRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decodeJSON(r, &body); err != nil {
		badRequest(w, err.Error())
		return
	}
	if body.Name == "" {
		badRequest(w, "name is required")
		return
	}

	existing, err := h.mgr.GetRole(ctx, body.Name)
	if err != nil {
		internalError(w, err)
		return
	}
	if existing != nil {
		writeJSON(w, http.StatusConflict, 1, "role already exists", nil)
		return
	}

	role := &model.Role{Name: body.Name, Description: body.Description}
	if err := h.mgr.CreateRole(ctx, role); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, 0, "created", map[string]string{"name": body.Name})
}

// DeleteRole deletes a role and clears it from all assigned users (admin only).
//
//	DELETE /api/v1/roles/{name}
func (h *AdminHandler) DeleteRole(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.mgr.DeleteRole(r.Context(), name); err != nil {
		internalError(w, err)
		return
	}
	ok(w, nil)
}

// GetRolePermissions returns the resource permissions for a role (admin only).
//
//	GET /api/v1/roles/{name}/permissions
func (h *AdminHandler) GetRolePermissions(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	perms, err := h.mgr.GetRolePermissions(r.Context(), name)
	if err != nil {
		internalError(w, err)
		return
	}
	ok(w, perms)
}

// SetRolePermissions replaces all resource permissions for a role (admin only).
//
//	PUT /api/v1/roles/{name}/permissions
func (h *AdminHandler) SetRolePermissions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := r.PathValue("name")

	var perms []*model.RolePermission
	if err := decodeJSON(r, &perms); err != nil {
		badRequest(w, err.Error())
		return
	}
	for i := range perms {
		perms[i].RoleName = name
	}

	if err := h.mgr.SetRolePermissions(ctx, name, perms); err != nil {
		internalError(w, err)
		return
	}
	ok(w, nil)
}

// ── Utility ───────────────────────────────────────────────────────────────────

func readJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

// saveHistory writes a config-history snapshot asynchronously (best-effort).
func (h *AdminHandler) saveHistory(ctx context.Context, resourceType, resourceName, action string, version int64, detail []byte) {
	uc, _ := userFromCtx(ctx)
	entry := &model.ConfigHistory{
		ResourceType: resourceType,
		ResourceName: resourceName,
		Version:      version,
		Action:       action,
		Detail:       detail,
		ChangedBy:    uc.Username,
		ChangedAt:    time.Now(),
	}
	if err := h.mgr.SaveConfigHistory(ctx, entry); err != nil {
		log.Printf("WARN saveHistory: %v", err)
	}
}

// realClientIP extracts the best-effort client IP from an HTTP request.
// It checks X-Forwarded-For (first non-empty element) and X-Real-IP before
// falling back to r.RemoteAddr, stripping the port when present.
func realClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For may be "client, proxy1, proxy2"; use the first entry.
		if idx := strings.IndexByte(xff, ','); idx != -1 {
			xff = xff[:idx]
		}
		if ip := strings.TrimSpace(xff); ip != "" {
			return ip
		}
	}
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		return xri
	}
	// Strip port from RemoteAddr (host:port or [::1]:port).
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// logAudit records an audit log entry (best-effort).
// detail is the operation-specific body/content to append after the request line.
func (h *AdminHandler) logAudit(r *http.Request, statusCode int, action, resourceType, resourceName, detail string) {
	uc, _ := userFromCtx(r.Context())
	fullDetail := fmt.Sprintf("%s %s  status=%d", r.Method, r.RequestURI, statusCode)
	if detail != "" {
		fullDetail += "\n" + detail
	}
	entry := &model.AuditLog{
		Username:     uc.Username,
		Action:       action,
		ResourceType: resourceType,
		ResourceName: resourceName,
		Detail:       fullDetail,
		ClientIP:     realClientIP(r),
		CreatedAt:    time.Now(),
	}
	if err := h.mgr.CreateAuditLog(r.Context(), entry); err != nil {
		log.Printf("WARN logAudit: %v", err)
	}
}

// ── Config history handlers ───────────────────────────────────────────────────

// ListConfigHistory returns all history entries for a config.
//
//	GET /api/v1/history/{type}/{name}
func (h *AdminHandler) ListConfigHistory(w http.ResponseWriter, r *http.Request) {
	resourceType := r.PathValue("type")
	name := r.PathValue("name")
	history, err := h.mgr.ListConfigHistory(r.Context(), resourceType, name)
	if err != nil {
		internalError(w, err)
		return
	}
	type histResp struct {
		ID           uint64 `json:"id"`
		ResourceType string `json:"resource_type"`
		ResourceName string `json:"resource_name"`
		Version      int64  `json:"version"`
		Action       string `json:"action"`
		Detail       string `json:"detail"`
		ChangedBy    string `json:"changed_by"`
		ChangedAt    string `json:"changed_at"`
	}
	resps := make([]histResp, len(history))
	for i, h := range history {
		resps[i] = histResp{
			ID:           h.ID,
			ResourceType: h.ResourceType,
			ResourceName: h.ResourceName,
			Version:      h.Version,
			Action:       h.Action,
			Detail:       string(h.Detail),
			ChangedBy:    h.ChangedBy,
			ChangedAt:    h.ChangedAt.Format(time.RFC3339),
		}
	}
	ok(w, resps)
}

// ListDeletedHistory returns the most recent "delete" history entry per distinct
// resource name for the given type — used to populate the recycle bin UI.
//
//	GET /api/v1/deleted-history/{type}
func (h *AdminHandler) ListDeletedHistory(w http.ResponseWriter, r *http.Request) {
	resourceType := r.PathValue("type")
	entries, err := h.mgr.ListDeletedConfigs(r.Context(), resourceType)
	if err != nil {
		internalError(w, err)
		return
	}
	type histResp struct {
		ID           uint64 `json:"id"`
		ResourceType string `json:"resource_type"`
		ResourceName string `json:"resource_name"`
		Version      int64  `json:"version"`
		Action       string `json:"action"`
		Detail       string `json:"detail"`
		ChangedBy    string `json:"changed_by"`
		ChangedAt    string `json:"changed_at"`
	}
	resps := make([]histResp, len(entries))
	for i, e := range entries {
		resps[i] = histResp{
			ID:           e.ID,
			ResourceType: e.ResourceType,
			ResourceName: e.ResourceName,
			Version:      e.Version,
			Action:       e.Action,
			Detail:       string(e.Detail),
			ChangedBy:    e.ChangedBy,
			ChangedAt:    e.ChangedAt.Format(time.RFC3339),
		}
	}
	ok(w, resps)
}

// RollbackConfig restores a config to a previous history snapshot.
//
//	POST /api/v1/history/{type}/{name}/{id}/rollback
func (h *AdminHandler) RollbackConfig(w http.ResponseWriter, r *http.Request) {
	resourceType := r.PathValue("type")
	name := r.PathValue("name")
	idStr := r.PathValue("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		badRequest(w, "invalid id")
		return
	}

	entry, err := h.mgr.GetConfigHistoryByID(r.Context(), id)
	if err != nil {
		internalError(w, err)
		return
	}
	if entry == nil || entry.ResourceType != resourceType || entry.ResourceName != name {
		notFound(w)
		return
	}
	if len(entry.Detail) == 0 {
		badRequest(w, "cannot rollback to a deleted version")
		return
	}

	newVersion := time.Now().UnixMilli()
	var oldDetail []byte // content before the rollback (saved in history)
	switch resourceType {
	case "pipeline":
		cfg, err2 := h.mgr.GetPipelineConfig(r.Context(), name)
		if err2 != nil && !isNotFound(err2) {
			internalError(w, err2)
			return
		}
		if cfg == nil || isNotFound(err2) {
			cfg = &model.PipelineConfig{Name: name, Version: newVersion, Detail: entry.Detail}
			if err2 = h.mgr.CreatePipelineConfig(r.Context(), cfg); err2 != nil {
				internalError(w, err2)
				return
			}
		} else {
			oldDetail = cfg.Detail
			cfg.Detail = entry.Detail
			cfg.Version = newVersion
			if err2 = h.mgr.UpdatePipelineConfig(r.Context(), cfg); err2 != nil {
				internalError(w, err2)
				return
			}
		}
	case "instance":
		cfg, err2 := h.mgr.GetInstanceConfig(r.Context(), name)
		if err2 != nil && !isNotFound(err2) {
			internalError(w, err2)
			return
		}
		if cfg == nil || isNotFound(err2) {
			cfg = &model.InstanceConfig{Name: name, Version: newVersion, Detail: entry.Detail}
			if err2 = h.mgr.CreateInstanceConfig(r.Context(), cfg); err2 != nil {
				internalError(w, err2)
				return
			}
		} else {
			oldDetail = cfg.Detail
			cfg.Detail = entry.Detail
			cfg.Version = newVersion
			if err2 = h.mgr.UpdateInstanceConfig(r.Context(), cfg); err2 != nil {
				internalError(w, err2)
				return
			}
		}
	case "group":
		switch entry.Action {
		case "set_version_constraint":
			group, err2 := h.mgr.GetGroup(r.Context(), name)
			if err2 != nil {
				if isNotFound(err2) {
					notFound(w)
					return
				}
				internalError(w, err2)
				return
			}
			oldDetail = []byte(group.VersionConstraint)
			group.VersionConstraint = string(entry.Detail)
			if err2 := h.mgr.UpdateGroup(r.Context(), group); err2 != nil {
				internalError(w, err2)
				return
			}
		case "set_ip_selector":
			group, err2 := h.mgr.GetGroup(r.Context(), name)
			if err2 != nil {
				if isNotFound(err2) {
					notFound(w)
					return
				}
				internalError(w, err2)
				return
			}
			oldDetail = []byte(group.IPSelectorJSON)
			if len(entry.Detail) > 0 {
				if _, err2 := model.ParseIPSelectorJSON(string(entry.Detail)); err2 != nil {
					badRequest(w, "cannot parse stored ip selector: "+err2.Error())
					return
				}
			}
			group.IPSelectorJSON = string(entry.Detail)
			if err2 := h.mgr.UpdateGroup(r.Context(), group); err2 != nil {
				internalError(w, err2)
				return
			}
		case "set_tags":
			// Restore old tags stored as JSON in history Detail.
			var oldTags []*model.AgentGroupTag
			if err2 := json.Unmarshal(entry.Detail, &oldTags); err2 != nil {
				badRequest(w, "cannot parse stored tags: "+err2.Error())
				return
			}
			// Snapshot current tags as oldDetail for rollback history.
			currentTags, _ := h.mgr.GetGroupTags(r.Context(), name)
			oldDetail, _ = json.Marshal(currentTags)
			if err2 := h.mgr.SetGroupTags(r.Context(), name, oldTags); err2 != nil {
				internalError(w, err2)
				return
			}
		case "add_config":
			// Rollback an add_config = remove that config.
			parts := strings.SplitN(string(entry.Detail), "/", 2)
			if len(parts) != 2 {
				badRequest(w, "invalid add_config detail")
				return
			}
			oldDetail = entry.Detail
			if err2 := h.mgr.RemoveGroupConfig(r.Context(), name, parts[1], parts[0]); err2 != nil {
				internalError(w, err2)
				return
			}
		case "remove_config":
			// Rollback a remove_config = add that config back.
			parts := strings.SplitN(string(entry.Detail), "/", 2)
			if len(parts) != 2 {
				badRequest(w, "invalid remove_config detail")
				return
			}
			oldDetail = entry.Detail
			if err2 := h.mgr.AddGroupConfig(r.Context(), &model.GroupConfigMapping{
				GroupName:  name,
				ConfigName: parts[1],
				ConfigType: parts[0],
			}); err2 != nil {
				internalError(w, err2)
				return
			}
		case "delete":
			// Rollback a group deletion = recreate (or override) the group with its original state.
			var snap groupSnapshot
			if err2 := json.Unmarshal(entry.Detail, &snap); err2 != nil {
				badRequest(w, "cannot parse group snapshot: "+err2.Error())
				return
			}
			existingGroup, err2 := h.mgr.GetGroup(r.Context(), name)
			if err2 != nil && !isNotFound(err2) {
				internalError(w, err2)
				return
			}
			if existingGroup != nil {
				// Override: update description, clear configs, then re-add from snapshot.
				existingGroup.Description = snap.Description
				existingGroup.IPSelectorJSON = snap.IPSelectorJSON
				existingGroup.VersionConstraint = snap.VersionConstraint
				if err2 = h.mgr.UpdateGroup(r.Context(), existingGroup); err2 != nil {
					internalError(w, err2)
					return
				}
				if existingConfigs, err3 := h.mgr.GetGroupConfigs(r.Context(), name); err3 == nil {
					for _, c := range existingConfigs {
						_ = h.mgr.RemoveGroupConfig(r.Context(), name, c.ConfigName, c.ConfigType)
					}
				}
			} else {
				if err2 = h.mgr.CreateGroup(r.Context(), &model.AgentGroup{
					Name:              name,
					Description:       snap.Description,
					IPSelectorJSON:    snap.IPSelectorJSON,
					VersionConstraint: snap.VersionConstraint,
				}); err2 != nil {
					internalError(w, err2)
					return
				}
			}
			if len(snap.Tags) > 0 {
				if err2 := h.mgr.SetGroupTags(r.Context(), name, snap.Tags); err2 != nil {
					internalError(w, err2)
					return
				}
			}
			for _, c := range snap.Configs {
				c.GroupName = name
				_ = h.mgr.AddGroupConfig(r.Context(), c) // best-effort; referenced config may be gone
			}
			oldDetail = entry.Detail
		default:
			badRequest(w, "rollback not supported for group action: "+entry.Action)
			return
		}
	case "onetime":
		if entry.Action != "delete" {
			badRequest(w, "rollback not supported for onetime action: "+entry.Action)
			return
		}
		var snap struct {
			Detail     string `json:"detail"`
			ExpireTime int64  `json:"expire_time"`
		}
		if err2 := json.Unmarshal(entry.Detail, &snap); err2 != nil {
			badRequest(w, "cannot parse onetime snapshot: "+err2.Error())
			return
		}
		// Delete existing command with the same name if present (override semantics).
		if existing, err2 := h.mgr.GetOnetimeCommand(r.Context(), name); err2 == nil && existing != nil {
			if err2 = h.mgr.DeleteOnetimeCommand(r.Context(), name); err2 != nil {
				internalError(w, err2)
				return
			}
		}
		cmd := &model.OnetimeCommand{
			Name:       name,
			Version:    newVersion,
			Detail:     []byte(snap.Detail),
			ExpireTime: snap.ExpireTime,
		}
		if err2 := h.mgr.CreateOnetimeCommand(r.Context(), cmd); err2 != nil {
			internalError(w, err2)
			return
		}
	default:
		badRequest(w, "rollback not supported for type: "+resourceType)
		return
	}

	h.saveHistory(r.Context(), resourceType, name, "rollback", newVersion, oldDetail)
	h.logAudit(r, http.StatusOK, "rollback", resourceType, name, fmt.Sprintf("to_history_id=%d\n%s", id, string(entry.Detail)))
	ok(w, nil)
}

// ── Audit log handlers ────────────────────────────────────────────────────────

// ListAuditLogs returns paginated audit log entries.
//
//	GET /api/v1/audit-logs?page=1&page_size=50
func (h *AdminHandler) ListAuditLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(q.Get("page_size"))
	if pageSize < 1 || pageSize > 500 {
		pageSize = 50
	}
	offset := (page - 1) * pageSize

	logs, total, err := h.mgr.ListAuditLogs(r.Context(), pageSize, offset)
	if err != nil {
		internalError(w, err)
		return
	}
	type auditResp struct {
		ID           uint64 `json:"id"`
		Username     string `json:"username"`
		Action       string `json:"action"`
		ResourceType string `json:"resource_type"`
		ResourceName string `json:"resource_name"`
		Detail       string `json:"detail"`
		ClientIP     string `json:"client_ip"`
		CreatedAt    string `json:"created_at"`
	}
	resps := make([]auditResp, len(logs))
	for i, l := range logs {
		resps[i] = auditResp{
			ID:           l.ID,
			Username:     l.Username,
			Action:       l.Action,
			ResourceType: l.ResourceType,
			ResourceName: l.ResourceName,
			Detail:       l.Detail,
			ClientIP:     l.ClientIP,
			CreatedAt:    l.CreatedAt.Format(time.RFC3339),
		}
	}
	ok(w, map[string]any{
		"logs":      resps,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// ── Canary releases ───────────────────────────────────────────────────────────

// canaryResp is the JSON representation of a CanaryRelease returned by the API.
type canaryResp struct {
	ConfigName        string            `json:"config_name"`
	ConfigType        string            `json:"config_type"`
	CanaryDetail      string            `json:"canary_detail"`
	CanaryVersion     int64             `json:"canary_version"`
	RolloutPercent    int               `json:"rollout_percent"`
	VersionConstraint string            `json:"version_constraint,omitempty"`
	IPSelector        []string          `json:"ip_selector,omitempty"`
	TagSelector       []model.CanaryTag `json:"tag_selector,omitempty"`
	Status            string            `json:"status"`
	CreatedBy         string            `json:"created_by,omitempty"`
	CreatedAt         string            `json:"created_at"`
	UpdatedAt         string            `json:"updated_at"`
}

func canaryToResp(cr *model.CanaryRelease) canaryResp {
	resp := canaryResp{
		ConfigName:        cr.ConfigName,
		ConfigType:        cr.ConfigType,
		CanaryDetail:      string(cr.CanaryDetail),
		CanaryVersion:     cr.CanaryVersion,
		RolloutPercent:    cr.RolloutPercent,
		VersionConstraint: cr.VersionConstraint,
		Status:            cr.Status,
		CreatedBy:         cr.CreatedBy,
		CreatedAt:         cr.CreatedAt.Format(time.RFC3339),
		UpdatedAt:         cr.UpdatedAt.Format(time.RFC3339),
	}
	if cr.IPSelectorJSON != "" {
		if sel, err := model.ParseIPSelectorJSON(cr.IPSelectorJSON); err == nil {
			resp.IPSelector = sel.IPs
		}
	}
	if cr.TagSelectorJSON != "" {
		var sel model.CanaryTagSelector
		if err := json.Unmarshal([]byte(cr.TagSelectorJSON), &sel); err == nil {
			resp.TagSelector = sel.Tags
		}
	}
	return resp
}

func sameCanaryPayload(cr *model.CanaryRelease, detail string, rolloutPercent int, versionConstraint, ipSelectorJSON, tagSelectorJSON string) bool {
	return bytes.Equal(cr.CanaryDetail, []byte(detail)) &&
		cr.RolloutPercent == rolloutPercent &&
		cr.VersionConstraint == versionConstraint &&
		cr.IPSelectorJSON == ipSelectorJSON &&
		cr.TagSelectorJSON == tagSelectorJSON
}

// configTypeFromPath maps the URL {type} path value to model config-type constants.
// Valid values: "pipeline" → model.ConfigTypePipeline, "instance" → model.ConfigTypeInstance.
func configTypeFromPath(raw string) (string, bool) {
	switch raw {
	case "pipeline":
		return model.ConfigTypePipeline, true
	case "instance":
		return model.ConfigTypeInstance, true
	}
	return "", false
}

// ListCanaries returns all canary release records.
func (h *AdminHandler) ListCanaries(w http.ResponseWriter, r *http.Request) {
	crs, err := h.mgr.ListCanaries(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	resps := make([]canaryResp, len(crs))
	for i, cr := range crs {
		resps[i] = canaryToResp(cr)
	}
	ok(w, resps)
}

// CreateCanary starts a new canary release for a config.
//
// POST /api/v1/configs/{type}/{name}/canary
// Body: { "canary_detail": "<yaml>", "rollout_percent": 0-100, "version_constraint": ">=1.2" }
func (h *AdminHandler) CreateCanary(w http.ResponseWriter, r *http.Request) {
	cfgType, ok2 := configTypeFromPath(r.PathValue("type"))
	if !ok2 {
		badRequest(w, "type must be 'pipeline' or 'instance'")
		return
	}
	name := r.PathValue("name")

	var req struct {
		CanaryDetail      string            `json:"canary_detail"`
		RolloutPercent    int               `json:"rollout_percent"`
		VersionConstraint string            `json:"version_constraint"`
		IPSelector        []string          `json:"ip_selector"`
		TagSelector       []model.CanaryTag `json:"tag_selector"`
	}
	if err := readJSON(r, &req); err != nil {
		badRequest(w, err.Error())
		return
	}
	if req.RolloutPercent < 0 || req.RolloutPercent > 100 {
		badRequest(w, "rollout_percent must be between 0 and 100")
		return
	}
	if err := validateYAML([]byte(req.CanaryDetail)); err != nil {
		badRequest(w, fmt.Sprintf("canary_detail: %v", err))
		return
	}

	// Validate and serialize the optional IP selector.
	ipSelectorJSON := ""
	if len(req.IPSelector) > 0 {
		normalized, err := model.NormalizeIPSelector(model.AgentGroupIPSelector{IPs: req.IPSelector})
		if err != nil {
			badRequest(w, fmt.Sprintf("ip_selector: %v", err))
			return
		}
		ipSelectorJSON, err = model.MarshalIPSelector(normalized)
		if err != nil {
			internalError(w, err)
			return
		}
	}

	// Validate and serialize the optional tag selector.
	tagSelectorJSON := ""
	if len(req.TagSelector) > 0 {
		for _, t := range req.TagSelector {
			if t.Name == "" {
				badRequest(w, "tag_selector: tag name must not be empty")
				return
			}
		}
		raw, err := json.Marshal(model.CanaryTagSelector{Tags: req.TagSelector})
		if err != nil {
			internalError(w, err)
			return
		}
		tagSelectorJSON = string(raw)
	}

	createdBy := ""
	if u, ok := userFromCtx(r.Context()); ok {
		createdBy = u.Username
	}

	cr := &model.CanaryRelease{
		ConfigName:        name,
		ConfigType:        cfgType,
		CanaryDetail:      []byte(req.CanaryDetail),
		CanaryVersion:     time.Now().UnixMilli(),
		RolloutPercent:    req.RolloutPercent,
		VersionConstraint: req.VersionConstraint,
		IPSelectorJSON:    ipSelectorJSON,
		TagSelectorJSON:   tagSelectorJSON,
		Status:            model.CanaryStatusRolling,
		CreatedBy:         createdBy,
	}

	// Check whether a canary release already exists for this config.
	if existing, err := h.mgr.GetCanary(r.Context(), name, cfgType); err != nil {
		internalError(w, err)
		return
	} else if existing != nil {
		if existing.Status == model.CanaryStatusRolling &&
			sameCanaryPayload(existing, req.CanaryDetail, req.RolloutPercent, req.VersionConstraint, ipSelectorJSON, tagSelectorJSON) {
			ok(w, canaryToResp(existing))
			return
		}
		conflict(w, fmt.Sprintf("a canary release for '%s' already exists (status: %s); abort or promote it before creating a new one", name, existing.Status))
		return
	}

	if err := h.mgr.CreateCanary(r.Context(), cr); err != nil {
		if existing, err2 := h.mgr.GetCanary(r.Context(), name, cfgType); err2 == nil && existing != nil {
			if existing.Status == model.CanaryStatusRolling &&
				sameCanaryPayload(existing, req.CanaryDetail, req.RolloutPercent, req.VersionConstraint, ipSelectorJSON, tagSelectorJSON) {
				ok(w, canaryToResp(existing))
				return
			}
			conflict(w, fmt.Sprintf("a canary release for '%s' already exists (status: %s); abort or promote it before creating a new one", name, existing.Status))
			return
		}
		internalError(w, err)
		return
	}
	h.logAudit(r, http.StatusCreated, "canary-create", cfgType, name, req.CanaryDetail)
	writeJSON(w, http.StatusCreated, 0, "created", canaryToResp(cr))
}

// UpdateCanary updates the canary_detail, rollout_percent, version_constraint, and
// selector fields of an active (rolling or paused) canary release.
//
// PUT /api/v1/configs/{type}/{name}/canary
// Body: { "canary_detail": "<yaml>", "rollout_percent": 0-100, "version_constraint": ">=1.2",
//
//	"ip_selector": [...], "tag_selector": [...] }
func (h *AdminHandler) UpdateCanary(w http.ResponseWriter, r *http.Request) {
	cfgType, ok2 := configTypeFromPath(r.PathValue("type"))
	if !ok2 {
		badRequest(w, "type must be 'pipeline' or 'instance'")
		return
	}
	name := r.PathValue("name")

	var req struct {
		CanaryDetail      string            `json:"canary_detail"`
		RolloutPercent    int               `json:"rollout_percent"`
		VersionConstraint string            `json:"version_constraint"`
		IPSelector        []string          `json:"ip_selector"`
		TagSelector       []model.CanaryTag `json:"tag_selector"`
	}
	if err := readJSON(r, &req); err != nil {
		badRequest(w, err.Error())
		return
	}
	if req.RolloutPercent < 0 || req.RolloutPercent > 100 {
		badRequest(w, "rollout_percent must be between 0 and 100")
		return
	}
	if err := validateYAML([]byte(req.CanaryDetail)); err != nil {
		badRequest(w, fmt.Sprintf("canary_detail: %v", err))
		return
	}

	// Load existing canary to ensure it exists and is in an editable state.
	cr, err := h.mgr.GetCanary(r.Context(), name, string(cfgType))
	if err != nil {
		internalError(w, err)
		return
	}
	if cr == nil {
		notFound(w)
		return
	}
	if cr.Status != model.CanaryStatusRolling && cr.Status != model.CanaryStatusPaused {
		badRequest(w, "only rolling or paused canaries can be updated")
		return
	}

	// Validate and serialize the optional IP selector.
	ipSelectorJSON := ""
	if len(req.IPSelector) > 0 {
		normalized, err := model.NormalizeIPSelector(model.AgentGroupIPSelector{IPs: req.IPSelector})
		if err != nil {
			badRequest(w, fmt.Sprintf("ip_selector: %v", err))
			return
		}
		ipSelectorJSON, err = model.MarshalIPSelector(normalized)
		if err != nil {
			internalError(w, err)
			return
		}
	}

	// Validate and serialize the optional tag selector.
	tagSelectorJSON := ""
	if len(req.TagSelector) > 0 {
		for _, t := range req.TagSelector {
			if t.Name == "" {
				badRequest(w, "tag_selector: tag name must not be empty")
				return
			}
		}
		raw, err := json.Marshal(model.CanaryTagSelector{Tags: req.TagSelector})
		if err != nil {
			internalError(w, err)
			return
		}
		tagSelectorJSON = string(raw)
	}

	if sameCanaryPayload(cr, req.CanaryDetail, req.RolloutPercent, req.VersionConstraint, ipSelectorJSON, tagSelectorJSON) {
		ok(w, canaryToResp(cr))
		return
	}

	if !bytes.Equal(cr.CanaryDetail, []byte(req.CanaryDetail)) {
		cr.CanaryVersion = time.Now().UnixMilli()
	}
	cr.CanaryDetail = []byte(req.CanaryDetail)
	cr.RolloutPercent = req.RolloutPercent
	cr.VersionConstraint = req.VersionConstraint
	cr.IPSelectorJSON = ipSelectorJSON
	cr.TagSelectorJSON = tagSelectorJSON

	if err := h.mgr.UpdateCanary(r.Context(), cr); err != nil {
		internalError(w, err)
		return
	}
	h.logAudit(r, http.StatusOK, "canary-update", cfgType, name, req.CanaryDetail)
	ok(w, canaryToResp(cr))
}

// GetCanary returns the active canary release for a config.
//
// GET /api/v1/configs/{type}/{name}/canary
func (h *AdminHandler) GetCanary(w http.ResponseWriter, r *http.Request) {
	cfgType, ok2 := configTypeFromPath(r.PathValue("type"))
	if !ok2 {
		badRequest(w, "type must be 'pipeline' or 'instance'")
		return
	}
	name := r.PathValue("name")
	cr, err := h.mgr.GetCanary(r.Context(), name, cfgType)
	if err != nil {
		internalError(w, err)
		return
	}
	if cr == nil {
		ok(w, map[string]string{"status": "promoted"})
		return
	}
	ok(w, canaryToResp(cr))
}

// SetCanaryPercent adjusts the rollout percentage of an active canary.
//
// PUT /api/v1/configs/{type}/{name}/canary/percent
// Body: { "rollout_percent": 0-100 }
func (h *AdminHandler) SetCanaryPercent(w http.ResponseWriter, r *http.Request) {
	cfgType, ok2 := configTypeFromPath(r.PathValue("type"))
	if !ok2 {
		badRequest(w, "type must be 'pipeline' or 'instance'")
		return
	}
	name := r.PathValue("name")

	var req struct {
		RolloutPercent int `json:"rollout_percent"`
	}
	if err := readJSON(r, &req); err != nil {
		badRequest(w, err.Error())
		return
	}
	if req.RolloutPercent < 0 || req.RolloutPercent > 100 {
		badRequest(w, "rollout_percent must be between 0 and 100")
		return
	}

	cr, err := h.mgr.GetCanary(r.Context(), name, cfgType)
	if err != nil {
		internalError(w, err)
		return
	}
	if cr == nil {
		notFound(w)
		return
	}
	if cr.Status != model.CanaryStatusRolling {
		badRequest(w, fmt.Sprintf("canary is not in rolling status (current: %s)", cr.Status))
		return
	}
	if cr.RolloutPercent == req.RolloutPercent {
		ok(w, canaryToResp(cr))
		return
	}
	cr.RolloutPercent = req.RolloutPercent
	if err := h.mgr.UpdateCanary(r.Context(), cr); err != nil {
		internalError(w, err)
		return
	}
	h.logAudit(r, http.StatusOK, "canary-set-percent", cfgType, name, strconv.Itoa(req.RolloutPercent))
	ok(w, canaryToResp(cr))
}

// PauseCanary halts delivery of the canary version; agents will receive the stable version.
//
// POST /api/v1/configs/{type}/{name}/canary/pause
func (h *AdminHandler) PauseCanary(w http.ResponseWriter, r *http.Request) {
	h.setCanaryStatus(w, r, model.CanaryStatusRolling, model.CanaryStatusPaused, "canary-pause")
}

// ResumeCanary resumes a paused canary.
//
// POST /api/v1/configs/{type}/{name}/canary/resume
func (h *AdminHandler) ResumeCanary(w http.ResponseWriter, r *http.Request) {
	h.setCanaryStatus(w, r, model.CanaryStatusPaused, model.CanaryStatusRolling, "canary-resume")
}

// setCanaryStatus is shared logic for pause/resume transitions.
func (h *AdminHandler) setCanaryStatus(w http.ResponseWriter, r *http.Request, requiredCurrent, newStatus, auditAction string) {
	cfgType, ok2 := configTypeFromPath(r.PathValue("type"))
	if !ok2 {
		badRequest(w, "type must be 'pipeline' or 'instance'")
		return
	}
	name := r.PathValue("name")
	cr, err := h.mgr.GetCanary(r.Context(), name, cfgType)
	if err != nil {
		internalError(w, err)
		return
	}
	if cr == nil {
		notFound(w)
		return
	}
	if cr.Status == newStatus {
		ok(w, canaryToResp(cr))
		return
	}
	if cr.Status != requiredCurrent {
		badRequest(w, fmt.Sprintf("canary must be in '%s' status to perform this action (current: %s)", requiredCurrent, cr.Status))
		return
	}
	cr.Status = newStatus
	if err := h.mgr.UpdateCanary(r.Context(), cr); err != nil {
		internalError(w, err)
		return
	}
	h.logAudit(r, http.StatusOK, auditAction, cfgType, name, "")
	ok(w, canaryToResp(cr))
}

// PromoteCanary copies the canary detail into the stable config table, then
// deletes the canary record. All agents will receive the promoted version on
// their next heartbeat.
//
// POST /api/v1/configs/{type}/{name}/canary/promote
func (h *AdminHandler) PromoteCanary(w http.ResponseWriter, r *http.Request) {
	cfgType, ok2 := configTypeFromPath(r.PathValue("type"))
	if !ok2 {
		badRequest(w, "type must be 'pipeline' or 'instance'")
		return
	}
	name := r.PathValue("name")
	ctx := r.Context()

	cr, err := h.mgr.GetCanary(ctx, name, cfgType)
	if err != nil {
		internalError(w, err)
		return
	}
	if cr == nil {
		ok(w, map[string]string{"status": "promoted"})
		return
	}

	switch cfgType {
	case model.ConfigTypePipeline:
		cfg, err2 := h.mgr.GetPipelineConfig(ctx, name)
		if err2 != nil {
			internalError(w, err2)
			return
		}
		if cfg == nil {
			notFound(w)
			return
		}
		oldDetail := cfg.Detail
		if !bytes.Equal(oldDetail, cr.CanaryDetail) {
			cfg.Detail = cr.CanaryDetail
			cfg.Version = time.Now().UnixMilli()
			if err2 := h.mgr.UpdatePipelineConfig(ctx, cfg); err2 != nil {
				internalError(w, err2)
				return
			}
			h.saveHistory(ctx, "pipeline", name, "promote", cfg.Version, oldDetail)
		}

	case model.ConfigTypeInstance:
		cfg, err2 := h.mgr.GetInstanceConfig(ctx, name)
		if err2 != nil {
			internalError(w, err2)
			return
		}
		if cfg == nil {
			notFound(w)
			return
		}
		oldDetail := cfg.Detail
		if !bytes.Equal(oldDetail, cr.CanaryDetail) {
			cfg.Detail = cr.CanaryDetail
			cfg.Version = time.Now().UnixMilli()
			if err2 := h.mgr.UpdateInstanceConfig(ctx, cfg); err2 != nil {
				internalError(w, err2)
				return
			}
			h.saveHistory(ctx, "instance", name, "promote", cfg.Version, oldDetail)
		}
	}

	if err := h.mgr.DeleteCanary(ctx, name, cfgType); err != nil {
		// Non-fatal: stable config was already updated; log and continue.
		log.Printf("WARN: PromoteCanary delete canary record failed: %v", err)
	}
	h.logAudit(r, http.StatusOK, "canary-promote", cfgType, name, string(cr.CanaryDetail))
	ok(w, map[string]string{"status": "promoted"})
}

// AbortCanary removes the canary record; all agents fall back to the stable
// version on their next heartbeat.
//
// POST /api/v1/configs/{type}/{name}/canary/abort
func (h *AdminHandler) AbortCanary(w http.ResponseWriter, r *http.Request) {
	cfgType, ok2 := configTypeFromPath(r.PathValue("type"))
	if !ok2 {
		badRequest(w, "type must be 'pipeline' or 'instance'")
		return
	}
	name := r.PathValue("name")
	cr, err := h.mgr.GetCanary(r.Context(), name, cfgType)
	if err != nil {
		internalError(w, err)
		return
	}
	if cr == nil {
		ok(w, map[string]string{"status": "aborted"})
		return
	}
	if err := h.mgr.DeleteCanary(r.Context(), name, cfgType); err != nil {
		internalError(w, err)
		return
	}
	h.logAudit(r, http.StatusOK, "canary-abort", cfgType, name, "")
	ok(w, map[string]string{"status": "aborted"})
}

// GetCanaryStats returns a summary of which hosts are receiving the canary version.
// Hosts are identified by Hostid (falling back to Hostname when Hostid is empty)
// to avoid double-counting the same machine after an agent process restart.
//
// GET /api/v1/configs/{type}/{name}/canary/stats
func (h *AdminHandler) GetCanaryStats(w http.ResponseWriter, r *http.Request) {
	cfgType, ok2 := configTypeFromPath(r.PathValue("type"))
	if !ok2 {
		badRequest(w, "type must be 'pipeline' or 'instance'")
		return
	}
	name := r.PathValue("name")
	ctx := r.Context()

	cr, err := h.mgr.GetCanary(ctx, name, cfgType)
	if err != nil {
		internalError(w, err)
		return
	}
	if cr == nil {
		notFound(w)
		return
	}

	agents, err := h.mgr.ListAgents(ctx)
	if err != nil {
		internalError(w, err)
		return
	}

	// Each InstanceID uniquely identifies a running agent process, so no
	// deduplication is needed — two agents on the same host are counted separately.
	//
	// Categorisation:
	//   canary      — passes all filters AND bucket < RolloutPercent
	//   stable      — passes all filters AND bucket >= RolloutPercent (truly in the stable bucket)
	//   not_targeted — fails IP / tag / version filter; outside this canary's scope
	//   unknown     — no stable identity; cannot compute bucket
	var canaryCount, stableCount, notTargetedCount, unknownCount int

	for _, agent := range agents {
		// Parse the agent's reported tags so IP/tag canary filters are honored.
		var tagEntries []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		var tags []model.AgentGroupTag
		if agent.TagsJSON != "" {
			if err := json.Unmarshal([]byte(agent.TagsJSON), &tagEntries); err == nil {
				tags = make([]model.AgentGroupTag, 0, len(tagEntries))
				for _, e := range tagEntries {
					tags = append(tags, model.AgentGroupTag{TagName: e.Name, TagValue: e.Value})
				}
			}
		}
		match := model.AgentMatchContext{
			InstanceID: agent.InstanceID,
			IP:         agent.IP,
			Hostid:     agent.Hostid,
			Hostname:   agent.Hostname,
			Version:    agent.Version,
			Tags:       tags,
		}
		bucket, hasBucket := model.CanaryBucket(match, name)
		if !hasBucket {
			unknownCount++
			continue
		}

		targeted, err := model.CanaryTargeted(cr, match)
		if err != nil {
			internalError(w, err)
			return
		}
		if !targeted {
			notTargetedCount++
			continue
		}

		if bucket < cr.RolloutPercent {
			canaryCount++
		} else {
			stableCount++
		}
	}

	ok(w, map[string]any{
		"config_name":     name,
		"config_type":     cfgType,
		"rollout_percent": cr.RolloutPercent,
		"status":          cr.Status,
		"canary_agents":   canaryCount,
		"stable_agents":   stableCount,
		"not_targeted":    notTargetedCount, // fail IP/tag/version filter — outside canary scope
		"unknown_agents":  unknownCount,     // no stable identity — cannot bucket
		"total_agents":    canaryCount + stableCount + notTargetedCount + unknownCount,
	})
}

// canaryAgentResp is a single agent entry returned by GetCanaryAgents.
type canaryAgentResp struct {
	InstanceID string `json:"instance_id"`
	AgentType  string `json:"agent_type"`
	IP         string `json:"ip"`
	Hostname   string `json:"hostname"`
	Version    string `json:"version"`
	Tags       []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"tags"`
	Bucket string `json:"bucket"` // "canary" | "stable" | "not_targeted" | "unknown"
}

// GetCanaryAgents lists every agent with its canary bucket assignment for a
// specific canary config.
//
// GET /api/v1/configs/{type}/{name}/canary/agents
func (h *AdminHandler) GetCanaryAgents(w http.ResponseWriter, r *http.Request) {
	cfgType, ok2 := configTypeFromPath(r.PathValue("type"))
	if !ok2 {
		badRequest(w, "type must be 'pipeline' or 'instance'")
		return
	}
	name := r.PathValue("name")
	ctx := r.Context()

	cr, err := h.mgr.GetCanary(ctx, name, cfgType)
	if err != nil {
		internalError(w, err)
		return
	}
	if cr == nil {
		notFound(w)
		return
	}

	agents, err := h.mgr.ListAgents(ctx)
	if err != nil {
		internalError(w, err)
		return
	}

	// Each InstanceID uniquely identifies a running agent process — no deduplication needed.
	result := make([]canaryAgentResp, 0, len(agents))

	for _, agent := range agents {
		var tagEntries []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		var tags []model.AgentGroupTag
		if agent.TagsJSON != "" {
			if err2 := json.Unmarshal([]byte(agent.TagsJSON), &tagEntries); err2 == nil {
				tags = make([]model.AgentGroupTag, 0, len(tagEntries))
				for _, e := range tagEntries {
					tags = append(tags, model.AgentGroupTag{TagName: e.Name, TagValue: e.Value})
				}
			}
		}

		match := model.AgentMatchContext{
			InstanceID: agent.InstanceID,
			IP:         agent.IP,
			Hostid:     agent.Hostid,
			Hostname:   agent.Hostname,
			Version:    agent.Version,
			Tags:       tags,
		}

		bucket := "unknown"
		bkt, hasBucket := model.CanaryBucket(match, name)
		if hasBucket {
			targeted, err2 := model.CanaryTargeted(cr, match)
			if err2 != nil {
				internalError(w, err2)
				return
			}
			if !targeted {
				bucket = "not_targeted"
			} else if bkt < cr.RolloutPercent {
				bucket = "canary"
			} else {
				bucket = "stable"
			}
		}

		entry := canaryAgentResp{
			InstanceID: agent.InstanceID,
			AgentType:  agent.AgentType,
			IP:         agent.IP,
			Hostname:   agent.Hostname,
			Version:    agent.Version,
			Tags:       tagEntries,
			Bucket:     bucket,
		}
		result = append(result, entry)
	}

	ok(w, result)
}
