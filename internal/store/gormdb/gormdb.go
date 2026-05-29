// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

// Package gormdb implements store.Store using GORM.
// It supports both SQLite (via github.com/glebarez/sqlite, CGO_ENABLED=0)
// and MySQL (via gorm.io/driver/mysql).
package gormdb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alibaba/ilogtail/config_server/internal/model"
	"github.com/alibaba/ilogtail/config_server/internal/store"
	glebarezSQLite "github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Store is the GORM-backed implementation of store.Store.
type Store struct {
	db *gorm.DB
}

var _ store.Store = (*Store)(nil)

// PoolConfig holds database connection pool settings.
type PoolConfig struct {
	MaxOpenConns    int           // max concurrent connections (0 = unlimited)
	MaxIdleConns    int           // idle connections to keep open
	ConnMaxLifetime time.Duration // max time a connection may be reused (0 = unlimited)
	ConnMaxIdleTime time.Duration // max time a connection may be idle (0 = unlimited)
}

// New opens (or creates) the database and runs AutoMigrate.
//
//   - driver "sqlite"  → pure-Go SQLite via github.com/glebarez/sqlite
//   - driver "mysql"   → MySQL via gorm.io/driver/mysql
func New(driver, dsn string, pool PoolConfig) (*Store, error) {
	var dialector gorm.Dialector
	switch driver {
	case "sqlite":
		dialector = glebarezSQLite.Open(dsn)
	case "mysql":
		dialector = mysql.Open(dsn)
	default:
		return nil, fmt.Errorf("unsupported db driver: %s", driver)
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// Apply connection pool settings.
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(pool.MaxOpenConns)
	sqlDB.SetMaxIdleConns(pool.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(pool.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(pool.ConnMaxIdleTime)

	// For SQLite enable WAL mode for better concurrent read performance.
	if driver == "sqlite" {
		if err := db.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
			return nil, fmt.Errorf("enable WAL: %w", err)
		}
	}

	if err := model.MigrateAll(db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	// Ensure the built-in default group exists. It matches every agent and
	// cannot be deleted via the API.
	defaultGroup := &model.AgentGroup{
		Name:        model.DefaultGroupName,
		Description: "Built-in group: configs assigned here are delivered to every agent.",
	}
	if err := db.Where(model.AgentGroup{Name: model.DefaultGroupName}).FirstOrCreate(defaultGroup).Error; err != nil {
		return nil, fmt.Errorf("ensure default group: %w", err)
	}

	return &Store{db: db}, nil
}

// ── Agent groups ──────────────────────────────────────────────────────────────

func (s *Store) CreateGroup(ctx context.Context, group *model.AgentGroup) error {
	return s.db.WithContext(ctx).Create(group).Error
}

func (s *Store) GetGroup(ctx context.Context, name string) (*model.AgentGroup, error) {
	var g model.AgentGroup
	if err := s.db.WithContext(ctx).First(&g, "name = ?", name).Error; err != nil {
		return nil, err
	}
	return &g, nil
}

func (s *Store) ListGroups(ctx context.Context) ([]*model.AgentGroup, error) {
	var groups []*model.AgentGroup
	return groups, s.db.WithContext(ctx).Find(&groups).Error
}

func (s *Store) UpdateGroup(ctx context.Context, group *model.AgentGroup) error {
	return s.db.WithContext(ctx).Save(group).Error
}

func (s *Store) DeleteGroup(ctx context.Context, name string) error {
	if name == model.DefaultGroupName {
		return fmt.Errorf("the default group cannot be deleted")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&model.AgentGroup{}, "name = ?", name).Error; err != nil {
			return err
		}
		if err := tx.Delete(&model.AgentGroupTag{}, "group_name = ?", name).Error; err != nil {
			return err
		}
		return tx.Delete(&model.GroupConfigMapping{}, "group_name = ?", name).Error
	})
}

// ── Group tags ────────────────────────────────────────────────────────────────

func (s *Store) SetGroupTags(ctx context.Context, groupName string, tags []*model.AgentGroupTag) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&model.AgentGroupTag{}, "group_name = ?", groupName).Error; err != nil {
			return err
		}
		for _, t := range tags {
			t.GroupName = groupName
		}
		if len(tags) == 0 {
			return nil
		}
		return tx.Create(tags).Error
	})
}

func (s *Store) GetGroupTags(ctx context.Context, groupName string) ([]*model.AgentGroupTag, error) {
	var tags []*model.AgentGroupTag
	return tags, s.db.WithContext(ctx).Where("group_name = ?", groupName).Find(&tags).Error
}

// ── Group ↔ config associations ───────────────────────────────────────────────

func (s *Store) AddGroupConfig(ctx context.Context, mapping *model.GroupConfigMapping) error {
	return s.db.WithContext(ctx).
		Where(model.GroupConfigMapping{
			GroupName:  mapping.GroupName,
			ConfigName: mapping.ConfigName,
			ConfigType: mapping.ConfigType,
		}).
		FirstOrCreate(mapping).Error
}

func (s *Store) RemoveGroupConfig(ctx context.Context, groupName, configName, configType string) error {
	return s.db.WithContext(ctx).
		Delete(&model.GroupConfigMapping{}, "group_name = ? AND config_name = ? AND config_type = ?",
			groupName, configName, configType).Error
}

func (s *Store) GetGroupConfigs(ctx context.Context, groupName string) ([]*model.GroupConfigMapping, error) {
	var mappings []*model.GroupConfigMapping
	return mappings, s.db.WithContext(ctx).Where("group_name = ?", groupName).Find(&mappings).Error
}

// ── Pipeline configs ──────────────────────────────────────────────────────────

func (s *Store) CreatePipelineConfig(ctx context.Context, cfg *model.PipelineConfig) error {
	return s.db.WithContext(ctx).Create(cfg).Error
}

func (s *Store) GetPipelineConfig(ctx context.Context, name string) (*model.PipelineConfig, error) {
	var cfg model.PipelineConfig
	if err := s.db.WithContext(ctx).First(&cfg, "name = ?", name).Error; err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (s *Store) ListPipelineConfigs(ctx context.Context) ([]*model.PipelineConfig, error) {
	var cfgs []*model.PipelineConfig
	return cfgs, s.db.WithContext(ctx).Find(&cfgs).Error
}

func (s *Store) UpdatePipelineConfig(ctx context.Context, cfg *model.PipelineConfig) error {
	return s.db.WithContext(ctx).
		Model(&model.PipelineConfig{}).Where("name = ?", cfg.Name).
		Updates(map[string]interface{}{
			"version":    cfg.Version,
			"detail":     cfg.Detail,
			"updated_at": time.Now(),
		}).Error
}

func (s *Store) DeletePipelineConfig(ctx context.Context, name string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&model.PipelineConfig{}, "name = ?", name).Error; err != nil {
			return err
		}
		return tx.Delete(&model.GroupConfigMapping{}, "config_name = ? AND config_type = ?", name, model.ConfigTypePipeline).Error
	})
}

// ── Instance configs ──────────────────────────────────────────────────────────

func (s *Store) CreateInstanceConfig(ctx context.Context, cfg *model.InstanceConfig) error {
	return s.db.WithContext(ctx).Create(cfg).Error
}

func (s *Store) GetInstanceConfig(ctx context.Context, name string) (*model.InstanceConfig, error) {
	var cfg model.InstanceConfig
	if err := s.db.WithContext(ctx).First(&cfg, "name = ?", name).Error; err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (s *Store) ListInstanceConfigs(ctx context.Context) ([]*model.InstanceConfig, error) {
	var cfgs []*model.InstanceConfig
	return cfgs, s.db.WithContext(ctx).Find(&cfgs).Error
}

func (s *Store) UpdateInstanceConfig(ctx context.Context, cfg *model.InstanceConfig) error {
	return s.db.WithContext(ctx).
		Model(&model.InstanceConfig{}).Where("name = ?", cfg.Name).
		Updates(map[string]interface{}{
			"version":    cfg.Version,
			"detail":     cfg.Detail,
			"updated_at": time.Now(),
		}).Error
}

func (s *Store) DeleteInstanceConfig(ctx context.Context, name string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&model.InstanceConfig{}, "name = ?", name).Error; err != nil {
			return err
		}
		return tx.Delete(&model.GroupConfigMapping{}, "config_name = ? AND config_type = ?", name, model.ConfigTypeInstance).Error
	})
}

// ── Onetime commands ──────────────────────────────────────────────────────────

func (s *Store) CreateOnetimeCommand(ctx context.Context, cmd *model.OnetimeCommand) error {
	return s.db.WithContext(ctx).Create(cmd).Error
}

func (s *Store) GetOnetimeCommand(ctx context.Context, name string) (*model.OnetimeCommand, error) {
	var cmd model.OnetimeCommand
	if err := s.db.WithContext(ctx).First(&cmd, "name = ?", name).Error; err != nil {
		return nil, err
	}
	return &cmd, nil
}

func (s *Store) ListOnetimeCommands(ctx context.Context) ([]*model.OnetimeCommand, error) {
	var cmds []*model.OnetimeCommand
	return cmds, s.db.WithContext(ctx).Find(&cmds).Error
}

func (s *Store) DeleteOnetimeCommand(ctx context.Context, name string) error {
	return s.db.WithContext(ctx).Delete(&model.OnetimeCommand{}, "name = ?", name).Error
}

// ── Agents ────────────────────────────────────────────────────────────────────

func (s *Store) UpsertAgent(ctx context.Context, agent *model.Agent) error {
	agent.LastHeartbeat = time.Now()
	return s.db.WithContext(ctx).Save(agent).Error
}

func (s *Store) GetAgent(ctx context.Context, instanceID string) (*model.Agent, error) {
	var a model.Agent
	if err := s.db.WithContext(ctx).First(&a, "instance_id = ?", instanceID).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Store) ListAgents(ctx context.Context) ([]*model.Agent, error) {
	var agents []*model.Agent
	return agents, s.db.WithContext(ctx).Find(&agents).Error
}

// ── Agent config status ───────────────────────────────────────────────────────

func (s *Store) UpsertAgentConfigStatus(ctx context.Context, status *model.AgentConfigStatus) error {
	status.UpdatedAt = time.Now()
	return s.db.WithContext(ctx).Save(status).Error
}

func (s *Store) GetAgentConfigStatuses(ctx context.Context, instanceID string) ([]*model.AgentConfigStatus, error) {
	var statuses []*model.AgentConfigStatus
	return statuses, s.db.WithContext(ctx).Where("instance_id = ?", instanceID).Find(&statuses).Error
}

// ── Core: GetConfigsForAgent ──────────────────────────────────────────────────
//
// ALL-match semantics:
//   A group is "matched" if at least one tag defined for that group appears
//   in the agent's tag list (ANY-match / union semantics).  Concretely:
//
//     matched_count(group) >= 1
//
//   where matched_count counts how many of the group's tags appear in the
//   supplied agentTags slice.

func (s *Store) GetConfigsForAgent(
	ctx context.Context,
	agentTags []model.AgentGroupTag,
) ([]*model.PipelineConfig, []*model.InstanceConfig, []*model.OnetimeCommand, error) {

	// The default group always matches every agent regardless of tags.
	groupNames := []string{model.DefaultGroupName}

	if len(agentTags) > 0 {
		// Build OR conditions: (tag_name = ? AND tag_value = ?) OR ...
		conds := make([]string, len(agentTags))
		args := make([]interface{}, 0, len(agentTags)*2)
		for i, t := range agentTags {
			conds[i] = "(agt.tag_name = ? AND agt.tag_value = ?)"
			args = append(args, t.TagName, t.TagValue)
		}
		tagMatchCond := strings.Join(conds, " OR ")

		// Find groups where at least one tag matches (ANY-match).
		rawSQL := fmt.Sprintf(`
SELECT g.name
FROM agent_groups g
JOIN (
    SELECT agt.group_name, COUNT(*) AS matched_count
    FROM agent_group_tags agt
    WHERE %s
    GROUP BY agt.group_name
) matched ON matched.group_name = g.name
WHERE matched.matched_count >= 1
`, tagMatchCond)

		type nameRow struct{ Name string }
		var rows []nameRow
		if err := s.db.WithContext(ctx).Raw(rawSQL, args...).Scan(&rows).Error; err != nil {
			return nil, nil, nil, fmt.Errorf("GetConfigsForAgent group query: %w", err)
		}
		for _, r := range rows {
			if r.Name != model.DefaultGroupName {
				groupNames = append(groupNames, r.Name)
			}
		}
	}

	// Fetch config mappings for matched groups.
	var mappings []model.GroupConfigMapping
	if err := s.db.WithContext(ctx).
		Where("group_name IN ?", groupNames).
		Find(&mappings).Error; err != nil {
		return nil, nil, nil, fmt.Errorf("GetConfigsForAgent mapping query: %w", err)
	}

	// Collect unique config names by type.
	pipelineSet := make(map[string]struct{})
	instanceSet := make(map[string]struct{})
	onetimeSet := make(map[string]struct{})
	for _, m := range mappings {
		switch m.ConfigType {
		case model.ConfigTypePipeline:
			pipelineSet[m.ConfigName] = struct{}{}
		case model.ConfigTypeInstance:
			instanceSet[m.ConfigName] = struct{}{}
		case model.ConfigTypeOnetime:
			onetimeSet[m.ConfigName] = struct{}{}
		}
	}

	// Fetch pipeline configs.
	var pipelineConfigs []*model.PipelineConfig
	if len(pipelineSet) > 0 {
		names := setKeys(pipelineSet)
		if err := s.db.WithContext(ctx).Where("name IN ?", names).Find(&pipelineConfigs).Error; err != nil {
			return nil, nil, nil, fmt.Errorf("GetConfigsForAgent pipeline query: %w", err)
		}
	}

	// Fetch instance configs.
	var instanceConfigs []*model.InstanceConfig
	if len(instanceSet) > 0 {
		names := setKeys(instanceSet)
		if err := s.db.WithContext(ctx).Where("name IN ?", names).Find(&instanceConfigs).Error; err != nil {
			return nil, nil, nil, fmt.Errorf("GetConfigsForAgent instance query: %w", err)
		}
	}

	// Fetch onetime commands.
	var onetimeCommands []*model.OnetimeCommand
	if len(onetimeSet) > 0 {
		names := setKeys(onetimeSet)
		if err := s.db.WithContext(ctx).Where("name IN ?", names).Find(&onetimeCommands).Error; err != nil {
			return nil, nil, nil, fmt.Errorf("GetConfigsForAgent onetime query: %w", err)
		}
	}

	return pipelineConfigs, instanceConfigs, onetimeCommands, nil
}

func setKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// ── User management ───────────────────────────────────────────────────────────

func (s *Store) GetUser(ctx context.Context, username string) (*model.User, error) {
	var u model.User
	if err := s.db.WithContext(ctx).First(&u, "username = ?", username).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}

func (s *Store) ListUsers(ctx context.Context) ([]*model.User, error) {
	var users []*model.User
	err := s.db.WithContext(ctx).Find(&users).Error
	return users, err
}

func (s *Store) CreateUser(ctx context.Context, user *model.User) error {
	return s.db.WithContext(ctx).Create(user).Error
}

func (s *Store) UpdateUser(ctx context.Context, user *model.User) error {
	return s.db.WithContext(ctx).Save(user).Error
}

func (s *Store) DeleteUser(ctx context.Context, username string) error {
	return s.db.WithContext(ctx).Delete(&model.User{}, "username = ?", username).Error
}

func (s *Store) AdminExists(ctx context.Context) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&model.User{}).Where("is_admin = ?", true).Count(&count).Error
	return count > 0, err
}

// ── Role management ───────────────────────────────────────────────────────────

func (s *Store) GetRole(ctx context.Context, name string) (*model.Role, error) {
	var r model.Role
	if err := s.db.WithContext(ctx).First(&r, "name = ?", name).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

func (s *Store) ListRoles(ctx context.Context) ([]*model.Role, error) {
	var roles []*model.Role
	err := s.db.WithContext(ctx).Find(&roles).Error
	return roles, err
}

func (s *Store) CreateRole(ctx context.Context, role *model.Role) error {
	return s.db.WithContext(ctx).Create(role).Error
}

func (s *Store) UpdateRole(ctx context.Context, role *model.Role) error {
	return s.db.WithContext(ctx).Save(role).Error
}

func (s *Store) DeleteRole(ctx context.Context, name string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Clear role_name for users assigned to this role.
		if err := tx.WithContext(ctx).Model(&model.User{}).Where("role_name = ?", name).
			Update("role_name", "").Error; err != nil {
			return err
		}
		// Delete role permissions.
		if err := tx.WithContext(ctx).Delete(&model.RolePermission{}, "role_name = ?", name).Error; err != nil {
			return err
		}
		// Delete the role itself.
		return tx.WithContext(ctx).Delete(&model.Role{}, "name = ?", name).Error
	})
}

func (s *Store) GetRolePermissions(ctx context.Context, roleName string) ([]*model.RolePermission, error) {
	var perms []*model.RolePermission
	err := s.db.WithContext(ctx).Where("role_name = ?", roleName).Find(&perms).Error
	return perms, err
}

func (s *Store) SetRolePermissions(ctx context.Context, roleName string, perms []*model.RolePermission) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).Where("role_name = ?", roleName).Delete(&model.RolePermission{}).Error; err != nil {
			return err
		}
		if len(perms) > 0 {
			return tx.WithContext(ctx).Create(&perms).Error
		}
		return nil
	})
}

// ── Config history ────────────────────────────────────────────────────────────

func (s *Store) SaveConfigHistory(ctx context.Context, h *model.ConfigHistory) error {
	return s.db.WithContext(ctx).Create(h).Error
}

func (s *Store) ListConfigHistory(ctx context.Context, resourceType, resourceName string) ([]*model.ConfigHistory, error) {
	var history []*model.ConfigHistory
	return history, s.db.WithContext(ctx).
		Where("resource_type = ? AND resource_name = ?", resourceType, resourceName).
		Order("changed_at DESC").
		Limit(200).
		Find(&history).Error
}

func (s *Store) ListDeletedConfigs(ctx context.Context, resourceType string) ([]*model.ConfigHistory, error) {
	var history []*model.ConfigHistory
	// For each resource_name, select the row with the highest id among delete-action entries.
	subQuery := s.db.Model(&model.ConfigHistory{}).
		Select("MAX(id)").
		Where("resource_type = ? AND action = ?", resourceType, "delete").
		Group("resource_name")
	return history, s.db.WithContext(ctx).
		Where("id IN (?)", subQuery).
		Order("changed_at DESC").
		Find(&history).Error
}

func (s *Store) GetConfigHistoryByID(ctx context.Context, id uint64) (*model.ConfigHistory, error) {
	var h model.ConfigHistory
	if err := s.db.WithContext(ctx).First(&h, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &h, nil
}

// ── Audit logs ────────────────────────────────────────────────────────────────

func (s *Store) CreateAuditLog(ctx context.Context, entry *model.AuditLog) error {
	return s.db.WithContext(ctx).Create(entry).Error
}

func (s *Store) ListAuditLogs(ctx context.Context, limit, offset int) ([]*model.AuditLog, int64, error) {
	var total int64
	if err := s.db.WithContext(ctx).Model(&model.AuditLog{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var logs []*model.AuditLog
	err := s.db.WithContext(ctx).Order("created_at DESC").Limit(limit).Offset(offset).Find(&logs).Error
	return logs, total, err
}
