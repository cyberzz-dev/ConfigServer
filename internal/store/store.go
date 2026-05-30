// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

// Package store defines the storage abstraction used by the ConfigServer.
package store

import (
	"context"

	"github.com/alibaba/ilogtail/config_server/internal/model"
)

// Store is the unified data-access interface for the ConfigServer.
// All persistence operations go through this interface; callers never
// touch the database driver directly.
type Store interface {
	// ── Agent groups ─────────────────────────────────────────────────────

	CreateGroup(ctx context.Context, group *model.AgentGroup) error
	GetGroup(ctx context.Context, name string) (*model.AgentGroup, error)
	ListGroups(ctx context.Context) ([]*model.AgentGroup, error)
	UpdateGroup(ctx context.Context, group *model.AgentGroup) error
	DeleteGroup(ctx context.Context, name string) error

	// ── Group tags ────────────────────────────────────────────────────────

	// SetGroupTags replaces all tags for the given group atomically.
	SetGroupTags(ctx context.Context, groupName string, tags []*model.AgentGroupTag) error
	GetGroupTags(ctx context.Context, groupName string) ([]*model.AgentGroupTag, error)

	// ── Group ↔ config associations ───────────────────────────────────────

	AddGroupConfig(ctx context.Context, mapping *model.GroupConfigMapping) error
	RemoveGroupConfig(ctx context.Context, groupName, configName, configType string) error
	GetGroupConfigs(ctx context.Context, groupName string) ([]*model.GroupConfigMapping, error)

	// ── Pipeline configs ──────────────────────────────────────────────────

	CreatePipelineConfig(ctx context.Context, cfg *model.PipelineConfig) error
	GetPipelineConfig(ctx context.Context, name string) (*model.PipelineConfig, error)
	ListPipelineConfigs(ctx context.Context) ([]*model.PipelineConfig, error)
	UpdatePipelineConfig(ctx context.Context, cfg *model.PipelineConfig) error
	DeletePipelineConfig(ctx context.Context, name string) error

	// ── Instance configs ──────────────────────────────────────────────────

	CreateInstanceConfig(ctx context.Context, cfg *model.InstanceConfig) error
	GetInstanceConfig(ctx context.Context, name string) (*model.InstanceConfig, error)
	ListInstanceConfigs(ctx context.Context) ([]*model.InstanceConfig, error)
	UpdateInstanceConfig(ctx context.Context, cfg *model.InstanceConfig) error
	DeleteInstanceConfig(ctx context.Context, name string) error

	// ── Onetime commands ──────────────────────────────────────────────────

	CreateOnetimeCommand(ctx context.Context, cmd *model.OnetimeCommand) error
	GetOnetimeCommand(ctx context.Context, name string) (*model.OnetimeCommand, error)
	ListOnetimeCommands(ctx context.Context) ([]*model.OnetimeCommand, error)
	DeleteOnetimeCommand(ctx context.Context, name string) error

	// ── Agents ────────────────────────────────────────────────────────────

	UpsertAgent(ctx context.Context, agent *model.Agent) error
	GetAgent(ctx context.Context, instanceID string) (*model.Agent, error)
	ListAgents(ctx context.Context) ([]*model.Agent, error)

	// ── Agent config status ───────────────────────────────────────────────

	UpsertAgentConfigStatus(ctx context.Context, status *model.AgentConfigStatus) error
	GetAgentConfigStatuses(ctx context.Context, instanceID string) ([]*model.AgentConfigStatus, error)

	// ── Core: agent-based config resolution ──────────────────────────────
	//
	// GetConfigsForAgent returns all pipeline, instance and onetime configs that
	// should be delivered to an agent with the given match context.
	//
	// Matching semantics: default group OR any matching tag OR any matching IP
	// selector. The result is the deduplicated union of configs from every
	// matched group.
	GetConfigsForAgent(ctx context.Context, match model.AgentMatchContext) ([]*model.PipelineConfig, []*model.InstanceConfig, []*model.OnetimeCommand, error)

	// ── User management ───────────────────────────────────────────────────

	// GetUser returns the user with the given username, or nil if not found.
	GetUser(ctx context.Context, username string) (*model.User, error)
	// ListUsers returns all user accounts.
	ListUsers(ctx context.Context) ([]*model.User, error)
	// CreateUser inserts a new user; returns an error if the username already exists.
	CreateUser(ctx context.Context, user *model.User) error
	// UpdateUser saves changes to an existing user (password hash, role name, etc.).
	UpdateUser(ctx context.Context, user *model.User) error
	// DeleteUser removes the user record.
	DeleteUser(ctx context.Context, username string) error
	// AdminExists reports whether at least one admin user account exists.
	AdminExists(ctx context.Context) (bool, error)

	// ── Role management ───────────────────────────────────────────────────

	// GetRole returns the role with the given name, or nil if not found.
	GetRole(ctx context.Context, name string) (*model.Role, error)
	// ListRoles returns all roles.
	ListRoles(ctx context.Context) ([]*model.Role, error)
	// CreateRole inserts a new role.
	CreateRole(ctx context.Context, role *model.Role) error
	// UpdateRole saves changes to an existing role.
	UpdateRole(ctx context.Context, role *model.Role) error
	// DeleteRole removes the role, its permissions, and clears role_name on
	// any users that were assigned to it.
	DeleteRole(ctx context.Context, name string) error
	// GetRolePermissions returns the resource permissions for a role.
	GetRolePermissions(ctx context.Context, roleName string) ([]*model.RolePermission, error)
	// SetRolePermissions replaces all permissions for a role atomically.
	SetRolePermissions(ctx context.Context, roleName string, perms []*model.RolePermission) error

	// ── Config history ────────────────────────────────────────────────────

	// SaveConfigHistory appends a mutation snapshot for a config or group.
	SaveConfigHistory(ctx context.Context, h *model.ConfigHistory) error
	// ListConfigHistory returns all history entries for a resource, newest first.
	ListConfigHistory(ctx context.Context, resourceType, resourceName string) ([]*model.ConfigHistory, error)
	// ListDeletedConfigs returns the most recent delete-action entry per resource_name for the given type.
	ListDeletedConfigs(ctx context.Context, resourceType string) ([]*model.ConfigHistory, error)
	// GetConfigHistoryByID returns a single history entry by primary key.
	GetConfigHistoryByID(ctx context.Context, id uint64) (*model.ConfigHistory, error)

	// ── Audit logs ────────────────────────────────────────────────────────

	// CreateAuditLog persists one audit entry.
	CreateAuditLog(ctx context.Context, entry *model.AuditLog) error
	// ListAuditLogs returns paginated audit entries, newest first.
	ListAuditLogs(ctx context.Context, limit, offset int) ([]*model.AuditLog, int64, error)
}
