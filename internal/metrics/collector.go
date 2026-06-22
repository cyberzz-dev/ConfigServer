// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

// Package metrics exposes aggregated agent statistics in Prometheus text format.
//
// High-cardinality design:
//   - Aggregated configserver_* metrics use only bounded-label dimensions.
//   - agent_hearbeat and agent_config intentionally expose per-agent series for
//     external fleet inspection. Keep these disabled at the Prometheus scrape
//     layer if the deployment cannot tolerate per-agent cardinality.
//
// Scrape: /metrics on the admin server (no auth, served alongside the WebUI).
// Push:   start Collector.StartPush to periodically POST Prometheus Remote Write
// payloads to an endpoint such as /api/v1/write.
package metrics

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alibaba/ilogtail/config_server/internal/model"
	"github.com/golang/snappy"
	"github.com/prometheus/prometheus/prompb"
	"github.com/redis/go-redis/v9"
)

// AgentLister is satisfied by *cache.Manager.
type AgentLister interface {
	ListAgents(ctx context.Context) ([]*model.Agent, error)
	ListAgentConfigStatuses(ctx context.Context) ([]*model.AgentConfigStatus, error)
}

// Collector gathers aggregated agent metrics.
type Collector struct {
	agents       AgentLister
	onlineWindow time.Duration
	pushRDB      redis.UniversalClient
}

// New creates a Collector.
// onlineWindow is the heartbeat age threshold below which an agent is considered "online".
func New(agents AgentLister, onlineWindow time.Duration) *Collector {
	if onlineWindow <= 0 {
		onlineWindow = 5 * time.Minute
	}
	c := &Collector{agents: agents, onlineWindow: onlineWindow}
	if provider, ok := agents.(interface{ RedisClient() redis.UniversalClient }); ok {
		c.pushRDB = provider.RedisClient()
	}
	return c
}

// Collect returns Prometheus text exposition for the current agent snapshot.
// All metrics are gauges; no per-agent labels are emitted to avoid high cardinality.
func (c *Collector) Collect(ctx context.Context) (string, error) {
	agents, err := c.agents.ListAgents(ctx)
	if err != nil {
		return "", fmt.Errorf("list agents: %w", err)
	}
	statuses, err := c.agents.ListAgentConfigStatuses(ctx)
	if err != nil {
		return "", fmt.Errorf("list agent config statuses: %w", err)
	}

	now := time.Now()
	var total, online int

	// Bounded-cardinality dimensions.
	statusCounts := map[string]int{}
	typeCounts := map[string]int{}
	agentsByID := make(map[string]*model.Agent, len(agents))

	for _, a := range agents {
		if a == nil {
			continue
		}
		agentsByID[a.InstanceID] = a
		total++
		if now.Sub(a.LastHeartbeat) <= c.onlineWindow {
			online++
		}
		if a.RunningStatus != "" {
			statusCounts[a.RunningStatus]++
		}
		if a.AgentType != "" {
			typeCounts[a.AgentType]++
		}
	}

	var sb strings.Builder

	writeMetric(&sb,
		"configserver_agents_total",
		"gauge",
		"Total number of agents registered since the last server start.",
		nil, total,
	)
	writeMetric(&sb,
		"configserver_agents_online_total",
		"gauge",
		fmt.Sprintf("Agents whose last heartbeat was within %s.", c.onlineWindow),
		nil, online,
	)

	sb.WriteString("# HELP configserver_agents_by_status Number of agents grouped by running_status.\n")
	sb.WriteString("# TYPE configserver_agents_by_status gauge\n")
	for _, status := range sortedKeys(statusCounts) {
		fmt.Fprintf(&sb, "configserver_agents_by_status{status=%q} %d\n", status, statusCounts[status])
	}

	sb.WriteString("# HELP configserver_agents_by_type Number of agents grouped by agent_type.\n")
	sb.WriteString("# TYPE configserver_agents_by_type gauge\n")
	for _, typ := range sortedKeys(typeCounts) {
		fmt.Fprintf(&sb, "configserver_agents_by_type{agent_type=%q} %d\n", typ, typeCounts[typ])
	}

	sb.WriteString("# HELP agent_hearbeat Agent online heartbeat by agent labels. Value is 1 when LastHeartbeat is within the online window, otherwise 0.\n")
	sb.WriteString("# TYPE agent_hearbeat gauge\n")
	sb.WriteString("# HELP agent_last_heartbeat_timestamp_seconds Last heartbeat Unix timestamp by agent labels.\n")
	sb.WriteString("# TYPE agent_last_heartbeat_timestamp_seconds gauge\n")
	for _, a := range agents {
		if a == nil || a.LastHeartbeat.IsZero() {
			continue
		}
		labels := []labelPair{
			{name: "type", value: a.AgentType},
			{name: "uuid", value: a.InstanceID},
			{name: "ip", value: a.IP},
			{name: "hostname", value: a.Hostname},
			{name: "version", value: a.Version},
		}
		labels = append(labels, parseTagsJSON(a.TagsJSON)...)
		formattedLabels := formatLabels(labels)
		fmt.Fprintf(&sb,
			"agent_hearbeat{%s} %g\n",
			formattedLabels,
			agentOnlineValue(now, a.LastHeartbeat, c.onlineWindow),
		)
		fmt.Fprintf(&sb,
			"agent_last_heartbeat_timestamp_seconds{%s} %d\n",
			formattedLabels,
			a.LastHeartbeat.Unix(),
		)
	}

	sb.WriteString("# HELP agent_config Agent config apply status by config and agent labels.\n")
	sb.WriteString("# TYPE agent_config gauge\n")
	sb.WriteString("# HELP agent_config_updated_timestamp_seconds Last config status update Unix timestamp by config and agent labels.\n")
	sb.WriteString("# TYPE agent_config_updated_timestamp_seconds gauge\n")
	for _, s := range statuses {
		if s == nil || s.UpdatedAt.IsZero() {
			continue
		}
		agent := agentsByID[s.InstanceID]
		labels := formatLabels([]labelPair{
			{name: "config_name", value: s.ConfigName},
			{name: "config_type", value: s.ConfigType},
			{name: "agent_type", value: agentLabelValue(agent, func(a *model.Agent) string { return a.AgentType })},
			{name: "agent_uuid", value: s.InstanceID},
			{name: "agent_ip", value: agentLabelValue(agent, func(a *model.Agent) string { return a.IP })},
			{name: "agent_hostname", value: agentLabelValue(agent, func(a *model.Agent) string { return a.Hostname })},
			{name: "agent_version", value: agentLabelValue(agent, func(a *model.Agent) string { return a.Version })},
		})
		fmt.Fprintf(&sb,
			"agent_config{%s} %d\n",
			labels,
			s.Status,
		)
		fmt.Fprintf(&sb,
			"agent_config_updated_timestamp_seconds{%s} %d\n",
			labels,
			s.UpdatedAt.Unix(),
		)
	}

	return sb.String(), nil
}

// CollectProto returns the current agent snapshot as Prometheus TimeSeries protobuf objects.
// This is used for the Remote Write protocol.
func (c *Collector) CollectProto(ctx context.Context) ([]prompb.TimeSeries, error) {
	agents, err := c.agents.ListAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	statuses, err := c.agents.ListAgentConfigStatuses(ctx)
	if err != nil {
		return nil, fmt.Errorf("list agent config statuses: %w", err)
	}

	now := time.Now()
	var total, online int

	statusCounts := map[string]int{}
	typeCounts := map[string]int{}
	agentsByID := make(map[string]*model.Agent, len(agents))

	for _, a := range agents {
		if a == nil {
			continue
		}
		agentsByID[a.InstanceID] = a
		total++
		if now.Sub(a.LastHeartbeat) <= c.onlineWindow {
			online++
		}
		if a.RunningStatus != "" {
			statusCounts[a.RunningStatus]++
		}
		if a.AgentType != "" {
			typeCounts[a.AgentType]++
		}
	}

	var series []prompb.TimeSeries
	ts := now.UnixMilli()

	addMetric := func(name string, labels map[string]string, value float64, timestamp int64) {
		if timestamp == 0 {
			timestamp = ts
		}
		promLabels := make([]prompb.Label, 0, len(labels)+1)
		promLabels = append(promLabels, prompb.Label{Name: "__name__", Value: name})
		for k, v := range labels {
			promLabels = append(promLabels, prompb.Label{Name: k, Value: v})
		}
		// Sort labels to comply with Prometheus Remote Write requirements
		sort.Slice(promLabels, func(i, j int) bool {
			return promLabels[i].Name < promLabels[j].Name
		})

		series = append(series, prompb.TimeSeries{
			Labels:  promLabels,
			Samples: []prompb.Sample{{Value: value, Timestamp: timestamp}},
		})
	}

	addMetric("configserver_agents_total", nil, float64(total), 0)
	addMetric("configserver_agents_online_total", nil, float64(online), 0)

	for _, status := range sortedKeys(statusCounts) {
		addMetric("configserver_agents_by_status", map[string]string{"status": status}, float64(statusCounts[status]), 0)
	}

	for _, typ := range sortedKeys(typeCounts) {
		addMetric("configserver_agents_by_type", map[string]string{"agent_type": typ}, float64(typeCounts[typ]), 0)
	}

	for _, a := range agents {
		if a == nil || a.LastHeartbeat.IsZero() {
			continue
		}
		lbls := map[string]string{
			"type":     a.AgentType,
			"uuid":     a.InstanceID,
			"ip":       a.IP,
			"hostname": a.Hostname,
			"version":  a.Version,
		}
		for _, lp := range parseTagsJSON(a.TagsJSON) {
			lbls[lp.name] = lp.value
		}
		addMetric("agent_hearbeat", lbls, agentOnlineValue(now, a.LastHeartbeat, c.onlineWindow), 0)
		addMetric("agent_last_heartbeat_timestamp_seconds", lbls, float64(a.LastHeartbeat.Unix()), 0)
	}

	for _, s := range statuses {
		if s == nil || s.UpdatedAt.IsZero() {
			continue
		}
		agent := agentsByID[s.InstanceID]
		lbls := map[string]string{
			"config_name":    s.ConfigName,
			"config_type":    s.ConfigType,
			"agent_type":     agentLabelValue(agent, func(a *model.Agent) string { return a.AgentType }),
			"agent_uuid":     s.InstanceID,
			"agent_ip":       agentLabelValue(agent, func(a *model.Agent) string { return a.IP }),
			"agent_hostname": agentLabelValue(agent, func(a *model.Agent) string { return a.Hostname }),
			"agent_version":  agentLabelValue(agent, func(a *model.Agent) string { return a.Version }),
		}
		addMetric("agent_config", lbls, float64(s.Status), 0)
		addMetric("agent_config_updated_timestamp_seconds", lbls, float64(s.UpdatedAt.Unix()), 0)
	}

	return series, nil
}

// Handler returns an http.Handler that serves the current metrics snapshot in
// Prometheus text format.  Register this at /metrics on any HTTP mux.
func (c *Collector) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		text, err := c.Collect(r.Context())
		if err != nil {
			http.Error(w, "collect metrics: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = io.WriteString(w, text)
	})
}

// StartPush begins pushing metrics to pushURL every interval until ctx is
// cancelled. Optional username/password configure HTTP Basic Auth. pushURL
// should be the full remote endpoint for Remote Write, for example:
//   - vmagent:  http://vmagent:8429/api/v1/write
func (c *Collector) StartPush(ctx context.Context, pushURL, username, password string, interval time.Duration) {
	if pushURL == "" {
		return
	}
	log.Printf("Metrics push enabled: url=%s interval=%s", pushURL, interval)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := c.pushWithLease(ctx, pushURL, username, password, interval); err != nil {
					log.Printf("WARN: metrics push to %s: %v", pushURL, err)
				}
			}
		}
	}()
}

var metricsPushLockReleaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
end
return 0
`)

func (c *Collector) pushWithLease(ctx context.Context, url, username, password string, interval time.Duration) error {
	if c.pushRDB == nil {
		return c.push(ctx, url, username, password)
	}
	token, err := randomHexToken(16)
	if err != nil {
		return fmt.Errorf("metrics push lock token: %w", err)
	}
	ttl := interval * 2
	if ttl < time.Minute {
		ttl = time.Minute
	}
	acquired, err := c.pushRDB.SetNX(ctx, "configserver:metrics:push_lock", token, ttl).Result()
	if err != nil {
		return fmt.Errorf("acquire metrics push lock: %w", err)
	}
	if !acquired {
		return nil
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := metricsPushLockReleaseScript.Run(releaseCtx, c.pushRDB, []string{"configserver:metrics:push_lock"}, token).Err(); err != nil {
			log.Printf("WARN: release metrics push lock: %v", err)
		}
	}()
	return c.push(ctx, url, username, password)
}

func (c *Collector) push(ctx context.Context, url, username, password string) error {
	series, err := c.CollectProto(ctx)
	if err != nil {
		return fmt.Errorf("collect proto: %w", err)
	}
	if len(series) == 0 {
		return nil
	}
	req := &prompb.WriteRequest{Timeseries: series}
	data, err := req.Marshal()
	if err != nil {
		return fmt.Errorf("marshal prompb: %w", err)
	}
	compressed := snappy.Encode(nil, data)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(compressed))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Encoding", "snappy")
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	httpReq.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	if username != "" || password != "" {
		httpReq.SetBasicAuth(username, password)
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("remote returned HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// writeMetric writes a single label-free gauge/counter metric block.
func writeMetric(sb *strings.Builder, name, typ, help string, labels map[string]string, value int) {
	fmt.Fprintf(sb, "# HELP %s %s\n", name, help)
	fmt.Fprintf(sb, "# TYPE %s %s\n", name, typ)
	if len(labels) == 0 {
		fmt.Fprintf(sb, "%s %d\n", name, value)
		return
	}
	var pairs []string
	for k, v := range labels {
		pairs = append(pairs, fmt.Sprintf("%s=%q", k, v))
	}
	sort.Strings(pairs)
	fmt.Fprintf(sb, "%s{%s} %d\n", name, strings.Join(pairs, ","), value)
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

type labelPair struct {
	name  string
	value string
}

func formatLabels(labels []labelPair) string {
	pairs := make([]string, 0, len(labels))
	for _, label := range labels {
		pairs = append(pairs, fmt.Sprintf("%s=%q", label.name, label.value))
	}
	return strings.Join(pairs, ",")
}

func unixMilli(t time.Time) int64 {
	return t.UnixNano() / int64(time.Millisecond)
}

func runningStatusValue(status string) float64 {
	if status == "" {
		return 0
	}
	if value, err := strconv.ParseFloat(status, 64); err == nil {
		return value
	}
	switch strings.ToLower(status) {
	case "running", "online", "ok", "healthy", "active":
		return 1
	default:
		return 0
	}
}

func agentOnlineValue(now, lastHeartbeat time.Time, onlineWindow time.Duration) float64 {
	if lastHeartbeat.IsZero() {
		return 0
	}
	if now.Sub(lastHeartbeat) <= onlineWindow {
		return 1
	}
	return 0
}

func randomHexToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func agentLabelValue(agent *model.Agent, getter func(*model.Agent) string) string {
	if agent == nil {
		return ""
	}
	return getter(agent)
}

// nonAlnum matches characters not valid in a Prometheus label name.
var nonAlnum = regexp.MustCompile(`[^a-zA-Z0-9_]`)

// parseTagsJSON decodes an agent's TagsJSON field into label pairs.
// Each tag name is sanitised to satisfy [a-zA-Z_][a-zA-Z0-9_]* and
// prefixed with "tag_" to avoid collision with built-in labels.
func parseTagsJSON(tagsJSON string) []labelPair {
	if tagsJSON == "" {
		return nil
	}
	var entries []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(tagsJSON), &entries); err != nil {
		return nil
	}
	pairs := make([]labelPair, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		if e.Name == "" {
			continue
		}
		sanitised := nonAlnum.ReplaceAllString(e.Name, "_")
		if len(sanitised) > 0 && sanitised[0] >= '0' && sanitised[0] <= '9' {
			sanitised = "_" + sanitised
		}
		label := "tag_" + sanitised
		if _, dup := seen[label]; dup {
			continue
		}
		seen[label] = struct{}{}
		pairs = append(pairs, labelPair{name: label, value: e.Value})
	}
	return pairs
}
