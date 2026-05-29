// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

// Package model defines GORM data models for the ConfigServer.
package model

import (
	"time"

	"gorm.io/gorm"
)

// ConfigType constants used in GroupConfigMapping and AgentConfigStatus.
const (
	ConfigTypePipeline = "pipeline"
	ConfigTypeInstance = "instance"
	ConfigTypeOnetime  = "onetime"
)

// DefaultGroupName is the reserved built-in group that matches every agent.
// Configs assigned to this group are delivered to all agents regardless of tags.
const DefaultGroupName = "default"

// AgentGroup represents a named group of agents.
type AgentGroup struct {
	Name        string `gorm:"primaryKey"`
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// AgentGroupTag is a key-value tag that defines group membership criteria.
// A group matches an agent when ANY of the group's tags appear in the agent's tags.
type AgentGroupTag struct {
	ID        uint64 `gorm:"primaryKey;autoIncrement"`
	GroupName string `gorm:"index;not null"`
	TagName   string `gorm:"not null"`
	TagValue  string `gorm:"not null"`
}

// PipelineConfig is a continuous pipeline configuration stored on the server.
type PipelineConfig struct {
	Name      string `gorm:"primaryKey"`
	Version   int64  `gorm:"not null"`
	Detail    []byte `gorm:"type:blob"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// InstanceConfig is an instance-level configuration stored on the server.
type InstanceConfig struct {
	Name      string `gorm:"primaryKey"`
	Version   int64  `gorm:"not null"`
	Detail    []byte `gorm:"type:blob"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// OnetimeCommand is a command that should be delivered once to matching agents.
type OnetimeCommand struct {
	Name       string `gorm:"primaryKey"`
	Detail     []byte `gorm:"type:blob"`
	ExpireTime int64
	CreatedAt  time.Time
}

// GroupConfigMapping associates a config with a group.
type GroupConfigMapping struct {
	GroupName  string `gorm:"primaryKey"`
	ConfigName string `gorm:"primaryKey"`
	ConfigType string `gorm:"primaryKey"` // pipeline | instance | onetime
}

// Agent stores the last-known state reported by an agent.
type Agent struct {
	InstanceID     string `gorm:"primaryKey"`
	AgentType      string
	IP             string
	Hostname       string
	Hostid         string
	Version        string
	AttributesJSON string `gorm:"type:text"`
	TagsJSON       string `gorm:"type:text"`
	RunningStatus  string
	StartupTime    int64
	SequenceNum    uint64
	Capabilities   uint64
	LastHeartbeat  time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// AgentConfigStatus records the application status of a single config on a single agent.
type AgentConfigStatus struct {
	InstanceID string `gorm:"primaryKey"`
	ConfigName string `gorm:"primaryKey"`
	ConfigType string `gorm:"primaryKey"` // pipeline | instance | onetime
	Status     int32
	Message    string
	UpdatedAt  time.Time
}

// ConfigHistory records every create/update/delete mutation on a config resource.
// For groups it captures description/tag changes.
type ConfigHistory struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement"`
	ResourceType string    `gorm:"not null;index"` // pipeline | instance | onetime | group
	ResourceName string    `gorm:"not null;index"`
	Version      int64     `gorm:"not null"`  // millisecond-epoch version; 0 for groups
	Action       string    `gorm:"not null"`  // create | update | delete | rollback
	Detail       []byte    `gorm:"type:blob"` // content snapshot (empty for delete)
	ChangedBy    string    `gorm:"not null"`
	ChangedAt    time.Time `gorm:"not null;index"`
}

// AuditLog records every write API call for compliance and traceability.
type AuditLog struct {
	ID           uint64 `gorm:"primaryKey;autoIncrement"`
	Username     string `gorm:"not null;index"`
	Action       string `gorm:"not null"` // create | update | delete | rollback | ...
	ResourceType string `gorm:"not null;index"`
	ResourceName string `gorm:"not null"`
	Detail       string `gorm:"type:text"` // optional human-readable summary
	ClientIP     string
	CreatedAt    time.Time `gorm:"not null;index"`
}

// User stores user account credentials and optional role assignment.
// The built-in "admin" account is marked with IsAdmin=true and bypasses all
// per-resource permission checks. Additional users are restricted to the
// permissions of their assigned Role; users without a role are denied by default.
type User struct {
	Username     string `gorm:"primaryKey"`
	PasswordHash string `gorm:"not null"`
	IsAdmin      bool   `gorm:"not null;default:false"`
	// RoleName is empty when the user has no role assigned.
	RoleName  string `gorm:"not null;default:''"`
	UpdatedAt time.Time
}

// Role is a named permission template that can be shared across many users.
type Role struct {
	Name        string `gorm:"primaryKey"`
	Description string
	UpdatedAt   time.Time
}

// RolePermission stores per-resource CRUD permissions for a role.
type RolePermission struct {
	RoleName  string `gorm:"primaryKey;index;not null" json:"role_name"`
	Resource  string `gorm:"primaryKey;not null"       json:"resource"`
	CanCreate bool   `json:"can_create"`
	CanRead   bool   `json:"can_read"`
	CanUpdate bool   `json:"can_update"`
	CanDelete bool   `json:"can_delete"`
}

// Resource constants used as RolePermission.Resource values.
const (
	ResourcePipelineConfigs = "pipeline_configs"
	ResourceInstanceConfigs = "instance_configs"
	ResourceOnetimeCommands = "onetime_commands"
	ResourceAgentGroups     = "agent_groups"
	ResourceAgents          = "agents"
)

// AllResources lists every permission resource in a stable order.
var AllResources = []string{
	ResourcePipelineConfigs,
	ResourceInstanceConfigs,
	ResourceOnetimeCommands,
	ResourceAgentGroups,
	ResourceAgents,
}

// MigrateAll runs AutoMigrate for all models.
func MigrateAll(db *gorm.DB) error {
	if err := db.AutoMigrate(
		&AgentGroup{},
		&AgentGroupTag{},
		&PipelineConfig{},
		&InstanceConfig{},
		&OnetimeCommand{},
		&GroupConfigMapping{},
		&Agent{},
		&AgentConfigStatus{},
		&User{},
		&Role{},
		&RolePermission{},
		&ConfigHistory{},
		&AuditLog{},
	); err != nil {
		return err
	}
	// Back-fill IsAdmin=true for any existing "admin" account after migration.
	return db.Model(&User{}).Where("username = ?", "admin").Update("is_admin", true).Error
}
