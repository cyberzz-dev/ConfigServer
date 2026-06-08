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
	"container/list"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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

	// keyResolveCanaries is the fixed key used to cache the list of rolling
	// canary releases inside the resolve cache.
	keyResolveCanaries = "__canaries__"
	// msgFlushResolve is published on the Pub/Sub channel to instruct peer
	// replicas to clear their own resolve caches.
	msgFlushResolve = "__flush_resolve__"

	// resolveTTL is the TTL (seconds) for per-agent config-name entries in the
	// resolve cache. Epoch-based invalidation handles correctness; TTL is just
	// a backstop to reclaim memory for agents that stop heartbeating.
	resolveTTL = 5 * 60 // 5 minutes

	// resolveCanaryTTL is the TTL (seconds) for the active-canary list cached
	// under keyResolveCanaries.  Any canary write also calls
	// invalidateResolveCache, so this TTL acts only as a fallback safety net.
	resolveCanaryTTL = 30 // 30 seconds

	// resolveMaxMB is the maximum size of the dedicated resolve cache.
	// Sizing is per-instance: in distributed mode each ConfigServer instance
	// serves ~100 K agents, so 64 MB (≈ 188 K entries at ~340 B each) provides
	// comfortable headroom for the epoch-transition overlap window where both
	// old- and new-epoch entries coexist until the old ones expire via TTL.
	// For larger per-instance agent counts, raise proportionally (~640 B/agent
	// to account for the overlap window).
	resolveMaxMB = 64

	// agentRedisTTL is the TTL for agent entries stored in Redis (distributed mode).
	// Each heartbeat refreshes the TTL, so entries expire 7 days after the last
	// heartbeat, effectively acting as a "last-seen" registry across all replicas.
	agentRedisTTL = 7 * 24 * time.Hour

	// agentHashSafetyTTL is the hash-key-level safety-net TTL used when HFE is
	// enabled (Valkey 9.0+ / Redis 7.4+).  Individual fields expire at
	// agentRedisTTL via HEXPIRE; this longer key-level TTL ensures the hash is
	// eventually reclaimed even if some fields were written without a field TTL
	// (e.g. by an older replica during a rolling upgrade).
	agentHashSafetyTTL = agentRedisTTL * 2

	// Agent in-memory cache limits.
	// maxAgents is a per-instance limit; in distributed mode (Redis enabled) the
	// full agent registry lives in Redis and agents that overflow the local map
	// are still accessible via GetAgent's Redis fallback path.  For fleets
	// exceeding 100 K agents, run multiple ConfigServer instances (each handles
	// ~100 K agents) behind a load balancer — do NOT raise maxAgents above a few
	// hundred-thousand per instance or the in-memory footprint becomes untenable
	// (~4 KB/agent × 1 M = 4 GB).
	maxAgents = 100_000

	// maxStatuses is the in-memory cap for config-delivery status entries.
	// Each entry is ~250 B; 100 K agents × 10 configs = 1 M entries ≈ 250 MB.
	// Entries exceeding the cap are silently dropped from memory but are still
	// written to Redis in distributed mode, so the admin UI remains accurate.
	maxStatuses  = maxAgents * 10
	agentTTL     = 30 * time.Minute
	agentGCEvery = 5 * time.Minute
)

// IsNotFound reports whether err represents a "record not found" store error.
func IsNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

// ── Redis Cluster helpers ─────────────────────────────────────────────────────

// forEachMaster executes fn against every master node in Cluster mode,
// or against the single node in Standalone/Sentinel mode.
// This ensures SCAN-style operations that must enumerate all keys visit
// all shards in a cluster rather than a single arbitrarily chosen node.
func forEachMaster(ctx context.Context, rdb redis.UniversalClient, fn func(ctx context.Context, c redis.UniversalClient) error) error {
	if cc, ok := rdb.(*redis.ClusterClient); ok {
		return cc.ForEachMaster(ctx, func(ctx context.Context, node *redis.Client) error {
			return fn(ctx, node)
		})
	}
	return fn(ctx, rdb)
}

// clusterPublish publishes a message on the correct node.
// In Redis Cluster mode it uses SPUBLISH (Sharded Pub/Sub, Redis ≥ 7.0) so
// that the message is delivered on the same slot-owner node that subscribers
// connect to via SSubscribe.  In Standalone/Sentinel mode it falls back to
// the regular PUBLISH command.
func clusterPublish(ctx context.Context, rdb redis.UniversalClient, channel, message string) error {
	if cc, ok := rdb.(*redis.ClusterClient); ok {
		return cc.SPublish(ctx, channel, message).Err()
	}
	return rdb.Publish(ctx, channel, message).Err()
}

// clusterSubscribe returns a *redis.PubSub that uses SSUBSCRIBE (Sharded
// Pub/Sub) in Cluster mode and regular SUBSCRIBE in Standalone/Sentinel mode.
// In Cluster mode SPUBLISH always routes to the slot-owning node, and
// SSUBSCRIBE connects to that same node, so messages are never dropped.
func clusterSubscribe(rdb redis.UniversalClient, ctx context.Context, channels ...string) *redis.PubSub {
	if cc, ok := rdb.(*redis.ClusterClient); ok {
		return cc.SSubscribe(ctx, channels...)
	}
	return rdb.Subscribe(ctx, channels...)
}

// agentConfigSet is the value stored in the resolve cache per agent.
// It holds only config names (no Detail bytes) so the cache stays small.
// On a cache hit the full config content is hydrated from the existing
// per-config L1/L2 cache entries.
type agentConfigSet struct {
	Pipeline []string `json:"p"`
	Instance []string `json:"i"`
	Onetime  []string `json:"o"`
}

// Manager wraps a store.Store with a two or three-tier cache. Agent data
// (heartbeats and config statuses) is kept in memory in all-in-one mode and is
// additionally mirrored to Redis in distributed mode so the admin process can
// expose a fleet-wide view.
type Manager struct {
	st         store.Store
	l1         *freecache.Cache
	rdb        redis.UniversalClient // nil ⇒ All-in-One (no Redis)
	l1TTL      time.Duration
	l2TTL      time.Duration
	sf         singleflight.Group
	hfeEnabled bool // use HEXPIRE for per-field TTL (Valkey 9.0+ / Redis 7.4+)

	// resolveCache holds per-agent config-name resolution results (agentConfigSet)
	// keyed by [8-byte epoch LE][8-byte FNV-64a agent hash].  It is separate from
	// l1 so that bumping resolveEpoch makes all current entries unreachable without
	// a mass Clear(), letting them expire naturally via TTL and avoiding a
	// thundering herd of DB queries after a structural write.
	resolveCache *freecache.Cache
	resolveEpoch atomic.Uint64 // incremented by invalidateResolveCache

	// In-memory agent registry — never persisted to DB.
	// agentsLRU/agentsLRUIdx provide O(1) eviction of the least-recently-seen
	// agent when the map hits maxAgents capacity.
	agentsMu     sync.RWMutex
	agentsMap    map[string]*model.Agent  // key: instanceID
	agentsLRU    *list.List               // front=newest, back=oldest (instanceID values)
	agentsLRUIdx map[string]*list.Element // instanceID → list element

	// In-memory agent config status — key: instanceID + ":" + configName + ":" + configType
	statusesMu  sync.RWMutex
	statusesMap map[string]*model.AgentConfigStatus
}

// New creates a Manager.
//   - rdb == nil       → All-in-One mode (L1 + L3 only)
//   - rdb != nil       → full three-tier mode with Pub/Sub cache invalidation
//   - hfeEnabled=true  → use HEXPIRE for per-field TTL on agent config-status
//     hashes (requires Valkey 9.0+ or Redis 7.4+);
//     ignored when rdb is nil.
//
// Call StartGC(ctx) after New to enable periodic eviction of stale agents.
func New(st store.Store, rdb redis.UniversalClient, l1MaxMB int, l1TTL, l2TTL time.Duration, hfeEnabled bool) *Manager {
	m := &Manager{
		st:           st,
		l1:           freecache.NewCache(l1MaxMB * 1024 * 1024),
		rdb:          rdb,
		l1TTL:        l1TTL,
		l2TTL:        l2TTL,
		hfeEnabled:   hfeEnabled && rdb != nil,
		resolveCache: freecache.NewCache(resolveMaxMB * 1024 * 1024),
		agentsMap:    make(map[string]*model.Agent, maxAgents),
		agentsLRU:    list.New(),
		agentsLRUIdx: make(map[string]*list.Element, maxAgents),
		statusesMap:  make(map[string]*model.AgentConfigStatus),
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
		// clusterPublish uses SPUBLISH in Cluster mode so the message reaches
		// the correct slot-owning node where subscribers are connected.
		if err := clusterPublish(ctx, m.rdb, pubSubChannel, key); err != nil {
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
	// clusterSubscribe uses SSUBSCRIBE in Cluster mode (Sharded Pub/Sub, Redis ≥ 7.0)
	// so that the subscriber connects to the same slot-owning node that clusterPublish
	// targets.  In Standalone/Sentinel mode it falls back to regular SUBSCRIBE.
	sub := clusterSubscribe(m.rdb, ctx, pubSubChannel)
	// Receive() blocks until Redis returns the subscription-confirmation message.
	// On error we still close ready so startup is never deadlocked; the L1 TTL
	// (5 min) acts as the backstop until connectivity is restored.
	if _, err := sub.Receive(ctx); err != nil {
		log.Printf("WARN: cache pubsub subscribe confirmation: %v", err)
	}
	close(ready)
	for msg := range sub.Channel() {
		if msg.Payload == msgFlushResolve {
			// A peer replica performed a structural write; bump the local epoch
			// so our cached agent-resolution entries are superseded on next lookup.
			// Also delete the shared canary-list entry so it is re-fetched fresh.
			m.resolveEpoch.Add(1)
			m.resolveCache.Del([]byte(keyResolveCanaries))
		} else {
			m.l1.Del([]byte(msg.Payload))
		}
	}
}

// ── Resolve-cache helpers ─────────────────────────────────────────────────────

// resolveAgentKey returns a 16-byte cache key: the current resolve epoch
// (8 bytes, little-endian) followed by the FNV-64a hash of the agent's match
// attributes (IP, host identity, version, sorted tags).
//
// Embedding the epoch as a key prefix makes all entries from the previous epoch
// unreachable as soon as resolveEpoch is incremented — entries then expire
// naturally via TTL rather than being evicted by a mass Clear().
func (m *Manager) resolveAgentKey(match model.AgentMatchContext) []byte {
	h := fnv.New64a()
	h.Write([]byte(match.IP))
	h.Write([]byte{0})
	if match.Hostid != "" {
		h.Write([]byte(match.Hostid))
	} else {
		h.Write([]byte(match.Hostname))
	}
	h.Write([]byte{0})
	h.Write([]byte(match.Version))
	h.Write([]byte{0})
	tags := make([]string, len(match.Tags))
	for i, t := range match.Tags {
		tags[i] = t.TagName + "=" + t.TagValue
	}
	sort.Strings(tags)
	for _, t := range tags {
		h.Write([]byte(t))
		h.Write([]byte{0})
	}
	var buf [16]byte
	binary.LittleEndian.PutUint64(buf[0:8], m.resolveEpoch.Load())
	binary.LittleEndian.PutUint64(buf[8:16], h.Sum64())
	return buf[:]
}

// invalidateResolveCache makes all current agent-resolution cache entries
// unreachable by bumping resolveEpoch (their keys embed the previous epoch value).
// It also explicitly deletes the shared canary-list entry so that the next
// getActiveCanaries call fetches a fresh list from the DB.
//
// In distributed mode a msgFlushResolve message is published so that peer
// replicas perform the same epoch bump on their own caches.
//
// Using an epoch bump instead of resolveCache.Clear() avoids a thundering herd:
// agents re-populate the cache one by one over the next heartbeat cycle rather
// than all missing simultaneously.
func (m *Manager) invalidateResolveCache(ctx context.Context) {
	m.resolveEpoch.Add(1)
	m.resolveCache.Del([]byte(keyResolveCanaries))
	if m.rdb != nil {
		if err := clusterPublish(ctx, m.rdb, pubSubChannel, msgFlushResolve); err != nil {
			log.Printf("WARN: publish flush_resolve: %v", err)
		}
	}
}

// getActiveCanaries returns all CanaryRelease records with status "rolling".
// The list is cached in resolveCache for resolveCanaryTTL seconds to avoid
// a full-table scan on every heartbeat.  Any canary write also calls
// invalidateResolveCache (which deletes the key), so staleness is bounded.
//
// The DB path is protected by singleflight so that a mass cache miss (e.g.
// after a canary write clears the key) causes only one ListCanaries query
// regardless of how many agent goroutines arrive simultaneously.
func (m *Manager) getActiveCanaries(ctx context.Context) ([]*model.CanaryRelease, error) {
	key := []byte(keyResolveCanaries)
	if raw, err := m.resolveCache.Get(key); err == nil {
		var crs []*model.CanaryRelease
		if json.Unmarshal(raw, &crs) == nil {
			return crs, nil
		}
	}
	v, err, _ := m.sf.Do(keyResolveCanaries, func() (interface{}, error) {
		all, err := m.st.ListCanaries(ctx)
		if err != nil {
			return nil, err
		}
		var active []*model.CanaryRelease
		for _, cr := range all {
			if cr.Status == model.CanaryStatusRolling {
				active = append(active, cr)
			}
		}
		if raw, err := json.Marshal(active); err == nil {
			_ = m.resolveCache.Set(key, raw, resolveCanaryTTL)
		}
		return active, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]*model.CanaryRelease), nil
}

// hydrateAgentConfigSet converts a cached agentConfigSet (names only) into full
// PipelineConfig, InstanceConfig, and OnetimeCommand slices by fetching content
// from the per-config L1/L2/DB cache.  It also applies any active canary overlays
// for the given agent using pure CPU computation (no additional DB queries).
//
// If a config has been deleted since the names were cached, it is silently skipped;
// the handler's deletion-signal logic will then instruct the agent to remove it.
func (m *Manager) hydrateAgentConfigSet(ctx context.Context, aset agentConfigSet, match model.AgentMatchContext) ([]*model.PipelineConfig, []*model.InstanceConfig, []*model.OnetimeCommand, error) {
	canaries, err := m.getActiveCanaries(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	crMap := make(map[string]*model.CanaryRelease, len(canaries))
	for _, cr := range canaries {
		crMap[cr.ConfigName+"\x00"+cr.ConfigType] = cr
	}

	pipes := make([]*model.PipelineConfig, 0, len(aset.Pipeline))
	for _, name := range aset.Pipeline {
		cfg, err := m.GetPipelineConfig(ctx, name)
		if err != nil {
			if IsNotFound(err) {
				continue // deleted since names were cached; handler will send version=-1
			}
			return nil, nil, nil, err
		}
		if cr, ok := crMap[name+"\x00"+model.ConfigTypePipeline]; ok {
			eligible, cerr := model.CanaryEligible(cr, match, name)
			if cerr != nil {
				log.Printf("WARN: canary eligibility check failed for pipeline %q agent %s: %v", name, match.IP, cerr)
			} else if eligible {
				// Copy to avoid mutating the cached config object held in L1/L2.
				cp := *cfg
				cp.Detail = cr.CanaryDetail
				cp.Version = cr.CanaryVersion
				cfg = &cp
			}
		}
		pipes = append(pipes, cfg)
	}

	insts := make([]*model.InstanceConfig, 0, len(aset.Instance))
	for _, name := range aset.Instance {
		cfg, err := m.GetInstanceConfig(ctx, name)
		if err != nil {
			if IsNotFound(err) {
				continue
			}
			return nil, nil, nil, err
		}
		if cr, ok := crMap[name+"\x00"+model.ConfigTypeInstance]; ok {
			eligible, cerr := model.CanaryEligible(cr, match, name)
			if cerr != nil {
				log.Printf("WARN: canary eligibility check failed for instance %q agent %s: %v", name, match.IP, cerr)
			} else if eligible {
				cp := *cfg
				cp.Detail = cr.CanaryDetail
				cp.Version = cr.CanaryVersion
				cfg = &cp
			}
		}
		insts = append(insts, cfg)
	}

	onetimes := make([]*model.OnetimeCommand, 0, len(aset.Onetime))
	for _, name := range aset.Onetime {
		cmd, err := m.st.GetOnetimeCommand(ctx, name)
		if err != nil {
			if IsNotFound(err) {
				continue
			}
			return nil, nil, nil, err
		}
		onetimes = append(onetimes, cmd)
	}

	return pipes, insts, onetimes, nil
}

// GetConfigsForAgent resolves which configs should be delivered to an agent.
//
// Results are cached in resolveCache keyed by a hash of the agent's matching
// attributes (IP, host identity, version, tags).  Only the config names are
// stored in the cache; config Detail bytes are fetched from the per-config
// L1/L2/DB cache on a cache hit, keeping resolve-cache memory usage small.
//
// The cache is invalidated (via invalidateResolveCache) on any structural write
// that could change which configs an agent receives.
func (m *Manager) GetConfigsForAgent(ctx context.Context, match model.AgentMatchContext) ([]*model.PipelineConfig, []*model.InstanceConfig, []*model.OnetimeCommand, error) {
	key := m.resolveAgentKey(match)
	if raw, err := m.resolveCache.Get(key); err == nil {
		var aset agentConfigSet
		if json.Unmarshal(raw, &aset) == nil {
			return m.hydrateAgentConfigSet(ctx, aset, match)
		}
	}

	// Cache miss: full DB resolution path.
	pipes, insts, onetimes, err := m.st.GetConfigsForAgent(ctx, match)
	if err != nil {
		return nil, nil, nil, err
	}

	// Cache only the names — Detail bytes live in the per-config L1/L2 entries.
	aset := agentConfigSet{
		Pipeline: make([]string, len(pipes)),
		Instance: make([]string, len(insts)),
		Onetime:  make([]string, len(onetimes)),
	}
	for i, c := range pipes {
		aset.Pipeline[i] = c.Name
	}
	for i, c := range insts {
		aset.Instance[i] = c.Name
	}
	for i, c := range onetimes {
		aset.Onetime[i] = c.Name
	}
	if raw, err := json.Marshal(aset); err == nil {
		_ = m.resolveCache.Set(key, raw, resolveTTL)
	}

	// Apply canary overlays on the cache-miss path too, so every delivery path
	// goes through hydrateAgentConfigSet regardless of whether the resolve cache
	// was cold or warm.
	return m.hydrateAgentConfigSet(ctx, aset, match)
}

// Convenience wrappers so the rest of the server only holds a *Manager.

func (m *Manager) CreateGroup(ctx context.Context, g *model.AgentGroup) error {
	if err := m.st.CreateGroup(ctx, g); err != nil {
		return err
	}
	m.invalidateResolveCache(ctx)
	return nil
}
func (m *Manager) GetGroup(ctx context.Context, name string) (*model.AgentGroup, error) {
	return m.st.GetGroup(ctx, name)
}
func (m *Manager) ListGroups(ctx context.Context) ([]*model.AgentGroup, error) {
	return m.st.ListGroups(ctx)
}
func (m *Manager) UpdateGroup(ctx context.Context, g *model.AgentGroup) error {
	if err := m.st.UpdateGroup(ctx, g); err != nil {
		return err
	}
	m.invalidateResolveCache(ctx)
	return nil
}
func (m *Manager) DeleteGroup(ctx context.Context, name string) error {
	if err := m.st.DeleteGroup(ctx, name); err != nil {
		return err
	}
	m.invalidateResolveCache(ctx)
	return nil
}
func (m *Manager) SetGroupTags(ctx context.Context, groupName string, tags []*model.AgentGroupTag) error {
	if err := m.st.SetGroupTags(ctx, groupName, tags); err != nil {
		return err
	}
	m.invalidateResolveCache(ctx)
	return nil
}
func (m *Manager) GetGroupTags(ctx context.Context, groupName string) ([]*model.AgentGroupTag, error) {
	return m.st.GetGroupTags(ctx, groupName)
}
func (m *Manager) AddGroupConfig(ctx context.Context, mapping *model.GroupConfigMapping) error {
	if err := m.st.AddGroupConfig(ctx, mapping); err != nil {
		return err
	}
	m.invalidateResolveCache(ctx)
	return nil
}
func (m *Manager) RemoveGroupConfig(ctx context.Context, groupName, configName, configType string) error {
	if err := m.st.RemoveGroupConfig(ctx, groupName, configName, configType); err != nil {
		return err
	}
	m.invalidateResolveCache(ctx)
	return nil
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
	m.invalidateResolveCache(ctx)
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
	m.invalidateResolveCache(ctx)
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

// agentStatusKey returns the in-memory map key for a single config status entry.
func agentStatusKey(instanceID, configName, configType string) string {
	return instanceID + ":" + configName + ":" + configType
}

// agentHashKey returns the Redis Hash key that stores all config statuses for
// one agent. Using a Hash reduces key count from N_agents×N_configs to N_agents.
func agentHashKey(instanceID string) string {
	return prefixStatus + instanceID
}

// agentHashField returns the Hash field name for a single config status entry.
func agentHashField(configName, configType string) string {
	return configName + ":" + configType
}

func (m *Manager) UpsertAgent(ctx context.Context, agent *model.Agent) error {
	m.agentsMu.Lock()
	if elem, exists := m.agentsLRUIdx[agent.InstanceID]; exists {
		// Existing agent: refresh data and move to front (most-recently-seen).
		m.agentsMap[agent.InstanceID] = agent
		m.agentsLRU.MoveToFront(elem)
	} else {
		// New agent: enforce capacity cap before inserting.
		if len(m.agentsMap) >= maxAgents {
			m.evictOldestAgentLocked()
		}
		m.agentsMap[agent.InstanceID] = agent
		elem := m.agentsLRU.PushFront(agent.InstanceID)
		m.agentsLRUIdx[agent.InstanceID] = elem
	}
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

// evictOldestAgentLocked removes the least-recently-seen agent from agentsMap
// in O(1) time using the LRU doubly-linked list (back = oldest).
// It also purges that agent's entries from statusesMap.
// Caller MUST hold agentsMu (write lock).
func (m *Manager) evictOldestAgentLocked() {
	back := m.agentsLRU.Back()
	if back == nil {
		return
	}
	id := back.Value.(string)
	m.agentsLRU.Remove(back)
	delete(m.agentsLRUIdx, id)
	a, ok := m.agentsMap[id]
	if !ok {
		return
	}
	delete(m.agentsMap, id)
	var hbStr string
	if a != nil {
		hbStr = a.LastHeartbeat.Format(time.RFC3339)
	}
	log.Printf("WARN: agent cache full (%d); evicted oldest agent %q (last heartbeat: %s)",
		maxAgents, id, hbStr)
	prefix := id + "\x00"
	m.statusesMu.Lock()
	for k := range m.statusesMap {
		if strings.HasPrefix(k, prefix) {
			delete(m.statusesMap, k)
		}
	}
	m.statusesMu.Unlock()
}

// evictStaleAgents removes all agents whose LastHeartbeat is older than agentTTL
// and purges their config-status entries from both memory and Redis.
func (m *Manager) evictStaleAgents() {
	cutoff := time.Now().Add(-agentTTL)

	m.agentsMu.Lock()
	var stale []string
	for id, a := range m.agentsMap {
		if a.LastHeartbeat.Before(cutoff) {
			stale = append(stale, id)
			delete(m.agentsMap, id)
			if elem, ok := m.agentsLRUIdx[id]; ok {
				m.agentsLRU.Remove(elem)
				delete(m.agentsLRUIdx, id)
			}
		}
	}
	m.agentsMu.Unlock()

	if len(stale) == 0 {
		return
	}

	// Clean up in-memory status map
	m.statusesMu.Lock()
	for _, id := range stale {
		prefix := id + ":"
		for k := range m.statusesMap {
			if strings.HasPrefix(k, prefix) {
				delete(m.statusesMap, k)
			}
		}
	}
	m.statusesMu.Unlock()

	// Clean up Redis entries (distributed mode only).
	// Each agent's entire status Hash is a single key, so one DEL per agent suffices.
	// In Redis Cluster mode a multi-key DEL crossing slot boundaries causes a
	// CROSSSLOT error, so we delete each key individually.
	if m.rdb != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		deleted := 0
		for _, id := range stale {
			for _, k := range []string{prefixAgent + id, agentHashKey(id)} {
				if err := m.rdb.Del(ctx, k).Err(); err != nil {
					log.Printf("WARN: agent GC redis del %q: %v", k, err)
				} else {
					deleted++
				}
			}
		}
		log.Printf("agent GC: evicted %d stale agents, deleted %d Redis keys (TTL=%s)",
			len(stale), deleted, agentTTL)
	} else {
		log.Printf("agent GC: evicted %d stale agents (TTL=%s)", len(stale), agentTTL)
	}
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
//
// Redis Cluster compatibility:
//   - In Cluster mode, SCAN only covers a single node and MGet across keys from
//     different slots causes CROSSSLOT errors. We use forEachMaster to run an
//     independent SCAN loop on every master node, then fetch each key with a
//     single-key GET (always routed to the correct slot).
//   - In Standalone/Sentinel mode forEachMaster is a no-op wrapper so behaviour
//     is identical to the previous implementation.
func (m *Manager) listAgentsFromRedis(ctx context.Context) ([]*model.Agent, error) {
	var mu sync.Mutex
	var agents []*model.Agent

	err := forEachMaster(ctx, m.rdb, func(ctx context.Context, c redis.UniversalClient) error {
		var cursor uint64
		for {
			keys, nextCursor, err := c.Scan(ctx, cursor, prefixAgent+"*", 200).Result()
			if err != nil {
				return err
			}
			for _, key := range keys {
				raw, err := c.Get(ctx, key).Bytes()
				if err != nil {
					continue // key may have expired between SCAN and GET
				}
				var a model.Agent
				if json.Unmarshal(raw, &a) != nil {
					continue
				}
				mu.Lock()
				agents = append(agents, &a)
				mu.Unlock()
			}
			cursor = nextCursor
			if cursor == 0 {
				break
			}
		}
		return nil
	})
	return agents, err
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
	// Enforce cap: allow updates to existing keys; only drop genuinely new entries
	// when the map is full.  Redis is still written below so the admin UI stays
	// accurate in distributed mode even when the local cap is reached.
	_, exists := m.statusesMap[key]
	if exists || len(m.statusesMap) < maxStatuses {
		m.statusesMap[key] = status
	}
	m.statusesMu.Unlock()

	if m.rdb != nil {
		if raw, err := json.Marshal(status); err == nil {
			hashKey := agentHashKey(status.InstanceID)
			field := agentHashField(status.ConfigName, status.ConfigType)
			if m.hfeEnabled {
				// HFE mode (Valkey 9.0+ / Redis 7.4+): set a per-field TTL via
				// HEXPIRE so each config-status entry expires independently.
				// Fields that stop being reported (e.g. a config removed from the
				// agent) expire at agentRedisTTL from their last update without
				// being kept alive by heartbeats for other configs in the same hash.
				// The hash-key TTL (agentHashSafetyTTL) is a safety net that is
				// also refreshed on every write; it reclaims the key in the
				// unlikely case where some fields were written without HEXPIRE
				// (e.g. by an older replica during a rolling upgrade).
				pipe := m.rdb.Pipeline()
				pipe.HSet(ctx, hashKey, field, raw)
				pipe.HExpire(ctx, hashKey, agentRedisTTL, field)
				pipe.Expire(ctx, hashKey, agentHashSafetyTTL)
				if _, err := pipe.Exec(ctx); err != nil {
					log.Printf("WARN: write agent config status (hfe) to redis %s/%s: %v", hashKey, field, err)
				}
			} else {
				// Standard mode: a single TTL covers the entire hash; it is
				// refreshed on every field write so the hash survives as long as
				// any config is being reported by the agent.
				pipe := m.rdb.Pipeline()
				pipe.HSet(ctx, hashKey, field, raw)
				pipe.Expire(ctx, hashKey, agentRedisTTL)
				if _, err := pipe.Exec(ctx); err != nil {
					log.Printf("WARN: write agent config status to redis %s/%s: %v", hashKey, field, err)
				}
			}
		}
	}
	return nil
}

func (m *Manager) GetAgentConfigStatuses(ctx context.Context, instanceID string) ([]*model.AgentConfigStatus, error) {
	if m.rdb != nil {
		// Single HGETALL fetches all config statuses for this agent in one round-trip.
		fields, err := m.rdb.HGetAll(ctx, agentHashKey(instanceID)).Result()
		if err != nil {
			return nil, err
		}
		out := make([]*model.AgentConfigStatus, 0, len(fields))
		for _, raw := range fields {
			var s model.AgentConfigStatus
			if err := json.Unmarshal([]byte(raw), &s); err == nil {
				out = append(out, &s)
			}
		}
		return out, nil
	}
	prefix := instanceID + ":"
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
		return m.listAgentConfigStatusesFromRedis(ctx)
	}
	m.statusesMu.RLock()
	out := make([]*model.AgentConfigStatus, 0, len(m.statusesMap))
	for _, s := range m.statusesMap {
		out = append(out, s)
	}
	m.statusesMu.RUnlock()
	return out, nil
}

func (m *Manager) listAgentConfigStatusesFromRedis(ctx context.Context) ([]*model.AgentConfigStatus, error) {
	// Redis Cluster compatibility:
	//   - In Cluster mode, SCAN only covers a single node. A Pipeline that sends
	//     HGetAll for keys from different slots routes commands to different nodes,
	//     which go-redis handles correctly for ClusterClient pipelines, but a SCAN
	//     on a single node would miss keys on other nodes.
	//   - We use forEachMaster to run an independent SCAN + HGetAll loop on every
	//     master node. Each HGetAll is a single-key command always routed correctly.
	//   - In Standalone/Sentinel mode forEachMaster is a no-op wrapper, so
	//     behaviour is identical to the previous implementation.
	pattern := prefixStatus + "*"

	var mu sync.Mutex
	var statuses []*model.AgentConfigStatus

	err := forEachMaster(ctx, m.rdb, func(ctx context.Context, c redis.UniversalClient) error {
		var cursor uint64
		for {
			keys, nextCursor, err := c.Scan(ctx, cursor, pattern, 200).Result()
			if err != nil {
				return err
			}
			for _, key := range keys {
				fields, err := c.HGetAll(ctx, key).Result()
				if err != nil {
					continue // key may have expired between SCAN and HGetAll
				}
				mu.Lock()
				for _, raw := range fields {
					var s model.AgentConfigStatus
					if json.Unmarshal([]byte(raw), &s) == nil {
						statuses = append(statuses, &s)
					}
				}
				mu.Unlock()
			}
			cursor = nextCursor
			if cursor == 0 {
				break
			}
		}
		return nil
	})
	return statuses, err
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

// ── Canary releases ───────────────────────────────────────────────────────────
// Canary writes also invalidate the resolve cache so that the new rollout
// parameters take effect on the next heartbeat cycle.

func (m *Manager) CreateCanary(ctx context.Context, cr *model.CanaryRelease) error {
	if err := m.st.CreateCanary(ctx, cr); err != nil {
		return err
	}
	m.invalidateResolveCache(ctx)
	return nil
}

func (m *Manager) GetCanary(ctx context.Context, configName, configType string) (*model.CanaryRelease, error) {
	return m.st.GetCanary(ctx, configName, configType)
}

func (m *Manager) ListCanaries(ctx context.Context) ([]*model.CanaryRelease, error) {
	return m.st.ListCanaries(ctx)
}

func (m *Manager) UpdateCanary(ctx context.Context, cr *model.CanaryRelease) error {
	if err := m.st.UpdateCanary(ctx, cr); err != nil {
		return err
	}
	m.invalidateResolveCache(ctx)
	return nil
}

func (m *Manager) DeleteCanary(ctx context.Context, configName, configType string) error {
	if err := m.st.DeleteCanary(ctx, configName, configType); err != nil {
		return err
	}
	m.invalidateResolveCache(ctx)
	return nil
}
