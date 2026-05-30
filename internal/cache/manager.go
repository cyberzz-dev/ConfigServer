// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

// Package cache implements a three-tier cache in front of store.Store.
//
// Tier layout
//
//	L1  freecache  – in-process memory, TTL 5 min (default)
//	L2  Redis      – shared across instances, TTL 30 days (optional)
//	L3  DB         – always the source of truth
//
// When Redis is disabled (All-in-One mode) the manager falls back to L1 + L3
// only; no Pub/Sub goroutine is started.
//
// Write policy: writes go directly to L3, then invalidate L2 + L1.
// Read policy:  L1 hit → return; L2 hit → backfill L1, return;
//
//	DB hit → backfill L2 + L1, return.
//
// Thundering-herd protection is provided by singleflight at the L3 level.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alibaba/ilogtail/config_server/internal/model"
	"github.com/alibaba/ilogtail/config_server/internal/store"
	"github.com/coocood/freecache"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

const (
	prefixPipeline = "cfg:pipeline:"
	prefixInstance = "cfg:instance:"
	prefixAgent    = "agent:"
	prefixStatus   = "agent_status:"
	pubSubChannel  = "configserver:invalidate"

	// agentRedisTTL is the TTL for agent entries stored in Redis (distributed mode).
	// Each heartbeat refreshes the TTL, so entries expire 7 days after the last
	// heartbeat, effectively acting as a "last-seen" registry across all replicas.
	agentRedisTTL = 7 * 24 * time.Hour

	// Agent in-memory cache limits.
	// Designed for fleets up to 50 000 agents (≈ 200 MB at ~4 KB/agent).
	// Entries whose last heartbeat is older than agentTTL are removed by the
	// background GC goroutine launched by StartGC.
	maxAgents    = 50_000
	agentTTL     = 30 * time.Minute
	agentGCEvery = 5 * time.Minute
)

// IsNotFound reports whether err represents a "record not found" store error.
func IsNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

// Manager wraps a store.Store with a two or three-tier cache. Agent data
// (heartbeats and config statuses) is kept in memory in all-in-one mode and is
// additionally mirrored to Redis in distributed mode so the admin process can
// expose a fleet-wide view.
type Manager struct {
	st    store.Store
	l1    *freecache.Cache
	rdb   redis.UniversalClient // nil ⇒ All-in-One (no Redis)
	l1TTL time.Duration
	l2TTL time.Duration
	sf    singleflight.Group

	// In-memory agent registry — never persisted to DB.
	agentsMu  sync.RWMutex
	agentsMap map[string]*model.Agent // key: instanceID

	// In-memory agent config status — key: instanceID + "\x00" + configName + "\x00" + configType
	statusesMu  sync.RWMutex
	statusesMap map[string]*model.AgentConfigStatus
}

// New creates a Manager.
//   - rdb == nil  → All-in-One mode (L1 + L3 only)
//   - rdb != nil  → full three-tier mode with Pub/Sub cache invalidation
//
// Call StartGC(ctx) after New to enable periodic eviction of stale agents.
func New(st store.Store, rdb redis.UniversalClient, l1MaxMB int, l1TTL, l2TTL time.Duration) *Manager {
	m := &Manager{
		st:          st,
		l1:          freecache.NewCache(l1MaxMB * 1024 * 1024),
		rdb:         rdb,
		l1TTL:       l1TTL,
		l2TTL:       l2TTL,
		agentsMap:   make(map[string]*model.Agent, maxAgents),
		statusesMap: make(map[string]*model.AgentConfigStatus),
	}
	if rdb != nil {
		ready := make(chan struct{})
		go m.subscribeInvalidations(ready)
		<-ready // wait until Redis confirms the SUBSCRIBE before HTTP opens
	}
	return m
}

// StartGC launches a background goroutine that evicts agents whose last
// heartbeat is older than agentTTL (30 min). It runs every agentGCEvery
// (5 min) and exits when ctx is cancelled.
func (m *Manager) StartGC(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(agentGCEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.evictStaleAgents()
			}
		}
	}()
}

// ── Read helpers ──────────────────────────────────────────────────────────────

// GetPipelineConfig fetches a pipeline config through the cache tiers.
func (m *Manager) GetPipelineConfig(ctx context.Context, name string) (*model.PipelineConfig, error) {
	key := prefixPipeline + name

	// L1 hit?
	if raw, err := m.l1.Get([]byte(key)); err == nil {
		var cfg model.PipelineConfig
		if err := json.Unmarshal(raw, &cfg); err == nil {
			return &cfg, nil
		}
	}

	// L2 hit?
	if m.rdb != nil {
		if raw, err := m.rdb.Get(ctx, key).Bytes(); err == nil {
			var cfg model.PipelineConfig
			if err := json.Unmarshal(raw, &cfg); err == nil {
				m.l1Set(key, raw)
				return &cfg, nil
			}
		}
	}

	// L3 (DB) with singleflight.
	v, err, _ := m.sf.Do(key, func() (interface{}, error) {
		return m.st.GetPipelineConfig(ctx, name)
	})
	if err != nil {
		return nil, err
	}
	cfg := v.(*model.PipelineConfig)
	m.backfill(ctx, key, cfg, cfg.Version)
	return cfg, nil
}

// GetInstanceConfig fetches an instance config through the cache tiers.
func (m *Manager) GetInstanceConfig(ctx context.Context, name string) (*model.InstanceConfig, error) {
	key := prefixInstance + name

	// L1 hit?
	if raw, err := m.l1.Get([]byte(key)); err == nil {
		var cfg model.InstanceConfig
		if err := json.Unmarshal(raw, &cfg); err == nil {
			return &cfg, nil
		}
	}

	// L2 hit?
	if m.rdb != nil {
		if raw, err := m.rdb.Get(ctx, key).Bytes(); err == nil {
			var cfg model.InstanceConfig
			if err := json.Unmarshal(raw, &cfg); err == nil {
				m.l1Set(key, raw)
				return &cfg, nil
			}
		}
	}

	// L3 with singleflight.
	v, err, _ := m.sf.Do(key, func() (interface{}, error) {
		return m.st.GetInstanceConfig(ctx, name)
	})
	if err != nil {
		return nil, err
	}
	cfg := v.(*model.InstanceConfig)
	m.backfill(ctx, key, cfg, cfg.Version)
	return cfg, nil
}

// ── Write helpers (invalidation) ──────────────────────────────────────────────

// InvalidatePipelineConfig removes a pipeline config from all cache tiers.
func (m *Manager) InvalidatePipelineConfig(ctx context.Context, name string) {
	m.invalidate(ctx, prefixPipeline+name)
}

// InvalidateInstanceConfig removes an instance config from all cache tiers.
func (m *Manager) InvalidateInstanceConfig(ctx context.Context, name string) {
	m.invalidate(ctx, prefixInstance+name)
}

func (m *Manager) invalidate(ctx context.Context, key string) {
	m.l1.Del([]byte(key))
	if m.rdb != nil {
		if err := m.rdb.Del(ctx, key).Err(); err != nil {
			log.Printf("WARN: cache invalidate redis del %q: %v", key, err)
		}
		// Publish invalidation to all other instances.
		if err := m.rdb.Publish(ctx, pubSubChannel, key).Err(); err != nil {
			log.Printf("WARN: cache invalidate publish %q: %v", key, err)
		}
	}
}

// ── Store pass-through (all other methods delegate to the inner store) ─────────

func (m *Manager) Store() store.Store { return m.st }

// Redis returns the Redis client used by this Manager, or nil in allinone mode.
func (m *Manager) Redis() redis.UniversalClient { return m.rdb }

// ── Internal helpers ──────────────────────────────────────────────────────────

func (m *Manager) l1Set(key string, raw []byte) {
	ttlSec := int(m.l1TTL.Seconds())
	if ttlSec <= 0 {
		ttlSec = 300
	}
	_ = m.l1.Set([]byte(key), raw, ttlSec)
}

// backfillScript atomically writes to Redis only when the stored version is
// strictly older than the incoming value (or the key does not yet exist).
// This prevents a slow L3 read from overwriting a newer value that was written
// to Redis by another instance after the original key was invalidated (ABA race).
//
// KEYS[1] = cache key
// ARGV[1] = serialised JSON value
// ARGV[2] = TTL in seconds
// ARGV[3] = new version (int); 0 means "always write" (no version field)
var backfillScript = redis.NewScript(`
local ver = tonumber(ARGV[3])
if ver and ver > 0 then
    local cur = redis.call('GET', KEYS[1])
    if cur ~= false then
        local ok, obj = pcall(cjson.decode, cur)
        if ok and type(obj) == 'table' and type(obj.Version) == 'number' and obj.Version >= ver then
            return 0
        end
    end
end
redis.call('SET', KEYS[1], ARGV[1], 'EX', tonumber(ARGV[2]))
return 1
`)

// backfill populates L1 and (when Redis is available) L2 after an L3 read.
// version must be the model's Version field so the Lua CAS script can reject
// writes that would overwrite a newer value; pass 0 for models without a
// version (e.g. OnetimeCommand), which falls back to an unconditional SET.
func (m *Manager) backfill(ctx context.Context, key string, v interface{}, version int64) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	m.l1Set(key, raw)
	if m.rdb != nil {
		ttlSec := int64(m.l2TTL.Seconds())
		if err := backfillScript.Run(ctx, m.rdb, []string{key}, raw, ttlSec, version).Err(); err != nil {
			log.Printf("WARN: cache backfill redis set %q: %v", key, err)
		}
	}
}

// subscribeInvalidations listens for Pub/Sub invalidation messages from peer
// instances and evicts the corresponding L1 entries.
// It signals ready after Redis acknowledges the SUBSCRIBE command, ensuring
// the subscription is live before New() returns and the HTTP server opens.
func (m *Manager) subscribeInvalidations(ready chan struct{}) {
	ctx := context.Background()
	sub := m.rdb.Subscribe(ctx, pubSubChannel)
	// Receive() blocks until Redis returns the subscription-confirmation message.
	// On error we still close ready so startup is never deadlocked; the L1 TTL
	// (5 min) acts as the backstop until connectivity is restored.
	if _, err := sub.Receive(ctx); err != nil {
		log.Printf("WARN: cache pubsub subscribe confirmation: %v", err)
	}
	close(ready)
	for msg := range sub.Channel() {
		m.l1.Del([]byte(msg.Payload))
	}
}

// ── Ensure Manager exposes the helper needed by agent_handler ──────────────────

// GetConfigsForAgent is a direct pass-through to the underlying store;
// the result set depends on the agent match context and cannot be meaningfully
// cached at this layer without complex cache-key design.
func (m *Manager) GetConfigsForAgent(ctx context.Context, match model.AgentMatchContext) ([]*model.PipelineConfig, []*model.InstanceConfig, []*model.OnetimeCommand, error) {
	return m.st.GetConfigsForAgent(ctx, match)
}

// Convenience wrappers so the rest of the server only holds a *Manager.

func (m *Manager) CreateGroup(ctx context.Context, g *model.AgentGroup) error {
	return m.st.CreateGroup(ctx, g)
}
func (m *Manager) GetGroup(ctx context.Context, name string) (*model.AgentGroup, error) {
	return m.st.GetGroup(ctx, name)
}
func (m *Manager) ListGroups(ctx context.Context) ([]*model.AgentGroup, error) {
	return m.st.ListGroups(ctx)
}
func (m *Manager) UpdateGroup(ctx context.Context, g *model.AgentGroup) error {
	return m.st.UpdateGroup(ctx, g)
}
func (m *Manager) DeleteGroup(ctx context.Context, name string) error {
	return m.st.DeleteGroup(ctx, name)
}
func (m *Manager) SetGroupTags(ctx context.Context, groupName string, tags []*model.AgentGroupTag) error {
	return m.st.SetGroupTags(ctx, groupName, tags)
}
func (m *Manager) GetGroupTags(ctx context.Context, groupName string) ([]*model.AgentGroupTag, error) {
	return m.st.GetGroupTags(ctx, groupName)
}
func (m *Manager) AddGroupConfig(ctx context.Context, mapping *model.GroupConfigMapping) error {
	return m.st.AddGroupConfig(ctx, mapping)
}
func (m *Manager) RemoveGroupConfig(ctx context.Context, groupName, configName, configType string) error {
	return m.st.RemoveGroupConfig(ctx, groupName, configName, configType)
}
func (m *Manager) GetGroupConfigs(ctx context.Context, groupName string) ([]*model.GroupConfigMapping, error) {
	return m.st.GetGroupConfigs(ctx, groupName)
}

func (m *Manager) CreatePipelineConfig(ctx context.Context, cfg *model.PipelineConfig) error {
	if err := m.st.CreatePipelineConfig(ctx, cfg); err != nil {
		return err
	}
	m.InvalidatePipelineConfig(ctx, cfg.Name)
	return nil
}
func (m *Manager) ListPipelineConfigs(ctx context.Context) ([]*model.PipelineConfig, error) {
	return m.st.ListPipelineConfigs(ctx)
}
func (m *Manager) UpdatePipelineConfig(ctx context.Context, cfg *model.PipelineConfig) error {
	if err := m.st.UpdatePipelineConfig(ctx, cfg); err != nil {
		return err
	}
	m.InvalidatePipelineConfig(ctx, cfg.Name)
	return nil
}
func (m *Manager) DeletePipelineConfig(ctx context.Context, name string) error {
	if err := m.st.DeletePipelineConfig(ctx, name); err != nil {
		return err
	}
	m.InvalidatePipelineConfig(ctx, name)
	return nil
}

func (m *Manager) CreateInstanceConfig(ctx context.Context, cfg *model.InstanceConfig) error {
	if err := m.st.CreateInstanceConfig(ctx, cfg); err != nil {
		return err
	}
	m.InvalidateInstanceConfig(ctx, cfg.Name)
	return nil
}
func (m *Manager) ListInstanceConfigs(ctx context.Context) ([]*model.InstanceConfig, error) {
	return m.st.ListInstanceConfigs(ctx)
}
func (m *Manager) UpdateInstanceConfig(ctx context.Context, cfg *model.InstanceConfig) error {
	if err := m.st.UpdateInstanceConfig(ctx, cfg); err != nil {
		return err
	}
	m.InvalidateInstanceConfig(ctx, cfg.Name)
	return nil
}
func (m *Manager) DeleteInstanceConfig(ctx context.Context, name string) error {
	if err := m.st.DeleteInstanceConfig(ctx, name); err != nil {
		return err
	}
	m.InvalidateInstanceConfig(ctx, name)
	return nil
}

func (m *Manager) CreateOnetimeCommand(ctx context.Context, cmd *model.OnetimeCommand) error {
	return m.st.CreateOnetimeCommand(ctx, cmd)
}
func (m *Manager) GetOnetimeCommand(ctx context.Context, name string) (*model.OnetimeCommand, error) {
	return m.st.GetOnetimeCommand(ctx, name)
}
func (m *Manager) ListOnetimeCommands(ctx context.Context) ([]*model.OnetimeCommand, error) {
	return m.st.ListOnetimeCommands(ctx)
}
func (m *Manager) DeleteOnetimeCommand(ctx context.Context, name string) error {
	return m.st.DeleteOnetimeCommand(ctx, name)
}

// ── In-memory agent storage (resets on restart) ─────────────────────────────

func agentStatusKey(instanceID, configName, configType string) string {
	return instanceID + "\x00" + configName + "\x00" + configType
}

func (m *Manager) UpsertAgent(ctx context.Context, agent *model.Agent) error {
	m.agentsMu.Lock()
	// Enforce capacity cap: if the map is full and this is a new agent (not a
	// heartbeat update), evict the entry with the oldest heartbeat to make room.
	if _, exists := m.agentsMap[agent.InstanceID]; !exists && len(m.agentsMap) >= maxAgents {
		m.evictOldestAgentLocked()
	}
	m.agentsMap[agent.InstanceID] = agent
	m.agentsMu.Unlock()

	// In distributed mode, write to Redis so every admin/configserver replica
	// can see all agents. TTL is refreshed on every heartbeat.
	if m.rdb != nil {
		if raw, err := json.Marshal(agent); err == nil {
			key := prefixAgent + agent.InstanceID
			if err := m.rdb.Set(ctx, key, raw, agentRedisTTL).Err(); err != nil {
				log.Printf("WARN: write agent to redis %s: %v", agent.InstanceID, err)
			}
		}
	}
	return nil
}

// evictOldestAgentLocked removes the agent with the earliest LastHeartbeat from
// agentsMap and purges its entries from statusesMap.
// Caller MUST hold agentsMu (write lock).
func (m *Manager) evictOldestAgentLocked() {
	var oldestID string
	var oldestTime time.Time
	for id, a := range m.agentsMap {
		if oldestID == "" || a.LastHeartbeat.Before(oldestTime) {
			oldestID = id
			oldestTime = a.LastHeartbeat
		}
	}
	if oldestID == "" {
		return
	}
	delete(m.agentsMap, oldestID)
	log.Printf("WARN: agent cache full (%d); evicted oldest agent %q (last heartbeat: %s)",
		maxAgents, oldestID, oldestTime.Format(time.RFC3339))
	prefix := oldestID + "\x00"
	m.statusesMu.Lock()
	for k := range m.statusesMap {
		if strings.HasPrefix(k, prefix) {
			delete(m.statusesMap, k)
		}
	}
	m.statusesMu.Unlock()
}

// evictStaleAgents removes all agents whose LastHeartbeat is older than agentTTL
// and purges their config-status entries.
func (m *Manager) evictStaleAgents() {
	cutoff := time.Now().Add(-agentTTL)

	m.agentsMu.Lock()
	var stale []string
	for id, a := range m.agentsMap {
		if a.LastHeartbeat.Before(cutoff) {
			stale = append(stale, id)
			delete(m.agentsMap, id)
		}
	}
	m.agentsMu.Unlock()

	if len(stale) == 0 {
		return
	}

	m.statusesMu.Lock()
	for _, id := range stale {
		prefix := id + "\x00"
		for k := range m.statusesMap {
			if strings.HasPrefix(k, prefix) {
				delete(m.statusesMap, k)
			}
		}
	}
	m.statusesMu.Unlock()

	log.Printf("agent GC: evicted %d stale agents (TTL=%s)", len(stale), agentTTL)
}

func (m *Manager) GetAgent(ctx context.Context, instanceID string) (*model.Agent, error) {
	m.agentsMu.RLock()
	a := m.agentsMap[instanceID]
	m.agentsMu.RUnlock()
	if a != nil {
		return a, nil
	}
	// In distributed mode fall back to Redis; the agent may have registered
	// with a different configserver replica.
	if m.rdb != nil {
		raw, err := m.rdb.Get(ctx, prefixAgent+instanceID).Bytes()
		if err == nil {
			var agent model.Agent
			if err := json.Unmarshal(raw, &agent); err == nil {
				return &agent, nil
			}
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (m *Manager) ListAgents(ctx context.Context) ([]*model.Agent, error) {
	if m.rdb != nil {
		return m.listAgentsFromRedis(ctx)
	}
	m.agentsMu.RLock()
	out := make([]*model.Agent, 0, len(m.agentsMap))
	for _, a := range m.agentsMap {
		out = append(out, a)
	}
	m.agentsMu.RUnlock()
	return out, nil
}

// listAgentsFromRedis fetches all agent entries stored in Redis by scanning
// keys matching the "agent:*" pattern. Used in distributed mode.
func (m *Manager) listAgentsFromRedis(ctx context.Context) ([]*model.Agent, error) {
	var agents []*model.Agent
	var cursor uint64
	for {
		keys, nextCursor, err := m.rdb.Scan(ctx, cursor, prefixAgent+"*", 200).Result()
		if err != nil {
			return nil, err
		}
		if len(keys) > 0 {
			vals, err := m.rdb.MGet(ctx, keys...).Result()
			if err != nil {
				return nil, err
			}
			for _, v := range vals {
				if v == nil {
					continue
				}
				var a model.Agent
				if err := json.Unmarshal([]byte(v.(string)), &a); err != nil {
					continue
				}
				agents = append(agents, &a)
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return agents, nil
}

// agentTagEntry mirrors the JSON shape of protov2.AgentGroupTag stored in TagsJSON.
type agentTagEntry struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// agentMatchesGroup reports whether agent satisfies at least one of the given
// group tags (ANY-match). An empty tag list matches every agent (e.g. the
// built-in "default" group).
func agentMatchesGroup(agent *model.Agent, groupTags []*model.AgentGroupTag) bool {
	if len(groupTags) == 0 {
		return true
	}
	var entries []agentTagEntry
	if agent.TagsJSON != "" {
		if err := json.Unmarshal([]byte(agent.TagsJSON), &entries); err != nil {
			return false
		}
	}
	agentTagSet := make(map[string]string, len(entries))
	for _, e := range entries {
		agentTagSet[e.Name] = e.Value
	}
	for _, gt := range groupTags {
		if agentTagSet[gt.TagName] == gt.TagValue {
			return true
		}
	}
	return false
}

// agentMatchesSearch reports whether agent matches the search keyword.
// The search is a case-insensitive substring match across InstanceID,
// Hostname, IP, Version, and AgentType.
func agentMatchesSearch(agent *model.Agent, search string) bool {
	if search == "" {
		return true
	}
	s := strings.ToLower(search)
	return strings.Contains(strings.ToLower(agent.InstanceID), s) ||
		strings.Contains(strings.ToLower(agent.Hostname), s) ||
		strings.Contains(strings.ToLower(agent.IP), s) ||
		strings.Contains(strings.ToLower(agent.Version), s) ||
		strings.Contains(strings.ToLower(agent.AgentType), s)
}

// ListAgentsPaged returns a filtered, sorted, paginated slice of agents.
//
//   - group:    filter by group name; empty string or "default" means all agents
//   - page:     1-based page number
//   - pageSize: number of results per page
//   - search:   case-insensitive substring match on InstanceID / Hostname / IP / Version / AgentType
//
// In All-in-One mode (rdb == nil) agents are read from the in-memory map.
// In distributed mode (rdb != nil) agents are read from Redis so that all
// configserver replicas' data is visible to the admin.
func (m *Manager) ListAgentsPaged(ctx context.Context, group string, page, pageSize int, search string) ([]*model.Agent, int, error) {
	// Resolve group tags. An empty tag list means "match all" (default group).
	var groupTags []*model.AgentGroupTag
	if group != "" && group != model.DefaultGroupName {
		var err error
		groupTags, err = m.st.GetGroupTags(ctx, group)
		if err != nil {
			if !IsNotFound(err) {
				return nil, 0, err
			}
			// Group not found — return empty result set.
			return nil, 0, nil
		}
	}

	// Collect raw agents.
	var all []*model.Agent
	var err error
	if m.rdb != nil {
		all, err = m.listAgentsFromRedis(ctx)
	} else {
		all, err = m.ListAgents(ctx)
	}
	if err != nil {
		return nil, 0, err
	}

	// Filter by group membership and search keyword.
	var filtered []*model.Agent
	for _, a := range all {
		if !agentMatchesGroup(a, groupTags) {
			continue
		}
		if !agentMatchesSearch(a, search) {
			continue
		}
		filtered = append(filtered, a)
	}

	// Sort descending by last heartbeat time.
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].LastHeartbeat.After(filtered[j].LastHeartbeat)
	})

	total := len(filtered)
	start := (page - 1) * pageSize
	if start >= total {
		return nil, total, nil
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return filtered[start:end], total, nil
}

func (m *Manager) UpsertAgentConfigStatus(ctx context.Context, status *model.AgentConfigStatus) error {
	status.UpdatedAt = time.Now()
	key := agentStatusKey(status.InstanceID, status.ConfigName, status.ConfigType)
	m.statusesMu.Lock()
	m.statusesMap[key] = status
	m.statusesMu.Unlock()

	if m.rdb != nil {
		if raw, err := json.Marshal(status); err == nil {
			if err := m.rdb.Set(ctx, prefixStatus+key, raw, agentRedisTTL).Err(); err != nil {
				log.Printf("WARN: write agent config status to redis %s: %v", key, err)
			}
		}
	}
	return nil
}

func (m *Manager) GetAgentConfigStatuses(ctx context.Context, instanceID string) ([]*model.AgentConfigStatus, error) {
	if m.rdb != nil {
		return m.listAgentConfigStatusesFromRedis(ctx, instanceID)
	}
	prefix := instanceID + "\x00"
	m.statusesMu.RLock()
	var out []*model.AgentConfigStatus
	for k, s := range m.statusesMap {
		if strings.HasPrefix(k, prefix) {
			out = append(out, s)
		}
	}
	m.statusesMu.RUnlock()
	return out, nil
}

func (m *Manager) ListAgentConfigStatuses(ctx context.Context) ([]*model.AgentConfigStatus, error) {
	if m.rdb != nil {
		return m.listAgentConfigStatusesFromRedis(ctx, "")
	}
	m.statusesMu.RLock()
	out := make([]*model.AgentConfigStatus, 0, len(m.statusesMap))
	for _, s := range m.statusesMap {
		out = append(out, s)
	}
	m.statusesMu.RUnlock()
	return out, nil
}

func (m *Manager) listAgentConfigStatusesFromRedis(ctx context.Context, instanceID string) ([]*model.AgentConfigStatus, error) {
	pattern := prefixStatus + "*"
	if instanceID != "" {
		pattern = prefixStatus + instanceID + "\x00*"
	}
	var statuses []*model.AgentConfigStatus
	var cursor uint64
	for {
		keys, nextCursor, err := m.rdb.Scan(ctx, cursor, pattern, 200).Result()
		if err != nil {
			return nil, err
		}
		if len(keys) > 0 {
			vals, err := m.rdb.MGet(ctx, keys...).Result()
			if err != nil {
				return nil, err
			}
			for _, v := range vals {
				if v == nil {
					continue
				}
				var status model.AgentConfigStatus
				if err := json.Unmarshal([]byte(v.(string)), &status); err != nil {
					continue
				}
				statuses = append(statuses, &status)
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return statuses, nil
}

// Ensure *Manager satisfies store.Store so it can be used anywhere a store is expected.
var _ store.Store = (*Manager)(nil)

// The following methods complete the store.Store interface.

func (m *Manager) GetPipelineConfigS(ctx context.Context, name string) (*model.PipelineConfig, error) {
	return m.GetPipelineConfig(ctx, name)
}

func (m *Manager) GetInstanceConfigS(ctx context.Context, name string) (*model.InstanceConfig, error) {
	return m.GetInstanceConfig(ctx, name)
}

// RedisClient returns the underlying Redis client (nil in All-in-One mode).
func (m *Manager) RedisClient() redis.UniversalClient { return m.rdb }

// FormatKey returns a cache key for external use (e.g., pre-warming).
func FormatKeyPipeline(name string) string {
	return fmt.Sprintf("%s%s", prefixPipeline, name)
}
func FormatKeyInstance(name string) string {
	return fmt.Sprintf("%s%s", prefixInstance, name)
}

// ── User management (no caching – accessed rarely) ────────────────────────────

func (m *Manager) GetUser(ctx context.Context, username string) (*model.User, error) {
	return m.st.GetUser(ctx, username)
}

func (m *Manager) ListUsers(ctx context.Context) ([]*model.User, error) {
	return m.st.ListUsers(ctx)
}

func (m *Manager) CreateUser(ctx context.Context, user *model.User) error {
	return m.st.CreateUser(ctx, user)
}

func (m *Manager) UpdateUser(ctx context.Context, user *model.User) error {
	return m.st.UpdateUser(ctx, user)
}

func (m *Manager) DeleteUser(ctx context.Context, username string) error {
	return m.st.DeleteUser(ctx, username)
}

func (m *Manager) AdminExists(ctx context.Context) (bool, error) {
	return m.st.AdminExists(ctx)
}

// ── Role management (no caching – accessed rarely) ────────────────────────────

func (m *Manager) GetRole(ctx context.Context, name string) (*model.Role, error) {
	return m.st.GetRole(ctx, name)
}

func (m *Manager) ListRoles(ctx context.Context) ([]*model.Role, error) {
	return m.st.ListRoles(ctx)
}

func (m *Manager) CreateRole(ctx context.Context, role *model.Role) error {
	return m.st.CreateRole(ctx, role)
}

func (m *Manager) UpdateRole(ctx context.Context, role *model.Role) error {
	return m.st.UpdateRole(ctx, role)
}

func (m *Manager) DeleteRole(ctx context.Context, name string) error {
	return m.st.DeleteRole(ctx, name)
}

func (m *Manager) GetRolePermissions(ctx context.Context, roleName string) ([]*model.RolePermission, error) {
	return m.st.GetRolePermissions(ctx, roleName)
}

func (m *Manager) SetRolePermissions(ctx context.Context, roleName string, perms []*model.RolePermission) error {
	return m.st.SetRolePermissions(ctx, roleName, perms)
}

// ── Config history ────────────────────────────────────────────────────────────

func (m *Manager) SaveConfigHistory(ctx context.Context, h *model.ConfigHistory) error {
	return m.st.SaveConfigHistory(ctx, h)
}

func (m *Manager) ListConfigHistory(ctx context.Context, resourceType, resourceName string) ([]*model.ConfigHistory, error) {
	return m.st.ListConfigHistory(ctx, resourceType, resourceName)
}

func (m *Manager) ListDeletedConfigs(ctx context.Context, resourceType string) ([]*model.ConfigHistory, error) {
	return m.st.ListDeletedConfigs(ctx, resourceType)
}

func (m *Manager) GetConfigHistoryByID(ctx context.Context, id uint64) (*model.ConfigHistory, error) {
	return m.st.GetConfigHistoryByID(ctx, id)
}

// ── Audit logs ────────────────────────────────────────────────────────────────

func (m *Manager) CreateAuditLog(ctx context.Context, entry *model.AuditLog) error {
	return m.st.CreateAuditLog(ctx, entry)
}

func (m *Manager) ListAuditLogs(ctx context.Context, limit, offset int) ([]*model.AuditLog, int64, error) {
	return m.st.ListAuditLogs(ctx, limit, offset)
}
