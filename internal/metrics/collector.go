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
// Push:   start Collector.StartPush to periodically POST to a remote endpoint
//
//	(vmagent /api/v1/import/prometheus or Prometheus Pushgateway).
package metrics

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alibaba/ilogtail/config_server/internal/model"
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
}

// New creates a Collector.
// onlineWindow is the heartbeat age threshold below which an agent is considered "online".
func New(agents AgentLister, onlineWindow time.Duration) *Collector {
	if onlineWindow <= 0 {
		onlineWindow = 5 * time.Minute
	}
	return &Collector{agents: agents, onlineWindow: onlineWindow}
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

	sb.WriteString("# HELP agent_hearbeat Agent heartbeat status by agent labels. Value is running status (running/online/ok=1, other=0 when non-numeric) and timestamp is LastHeartbeat.\n")
	sb.WriteString("# TYPE agent_hearbeat gauge\n")
	for _, a := range agents {
		if a == nil || a.LastHeartbeat.IsZero() {
			continue
		}
		fmt.Fprintf(&sb,
			"agent_hearbeat{%s} %g %d\n",
			formatLabels([]labelPair{
				{name: "type", value: a.AgentType},
				{name: "uuid", value: a.InstanceID},
				{name: "ip", value: a.IP},
				{name: "hostname", value: a.Hostname},
				{name: "version", value: a.Version},
			}),
			runningStatusValue(a.RunningStatus),
			unixMilli(a.LastHeartbeat),
		)
	}

	sb.WriteString("# HELP agent_config Agent config apply status by config and agent labels. Value is config status and timestamp is UpdatedAt.\n")
	sb.WriteString("# TYPE agent_config gauge\n")
	for _, s := range statuses {
		if s == nil || s.UpdatedAt.IsZero() {
			continue
		}
		agent := agentsByID[s.InstanceID]
		fmt.Fprintf(&sb,
			"agent_config{%s} %d %d\n",
			formatLabels([]labelPair{
				{name: "config_name", value: s.ConfigName},
				{name: "config_type", value: s.ConfigType},
				{name: "agent_type", value: agentLabelValue(agent, func(a *model.Agent) string { return a.AgentType })},
				{name: "agent_uuid", value: s.InstanceID},
				{name: "agent_ip", value: agentLabelValue(agent, func(a *model.Agent) string { return a.IP })},
				{name: "agent_hostname", value: agentLabelValue(agent, func(a *model.Agent) string { return a.Hostname })},
				{name: "agent_version", value: agentLabelValue(agent, func(a *model.Agent) string { return a.Version })},
			}),
			s.Status,
			unixMilli(s.UpdatedAt),
		)
	}

	return sb.String(), nil
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
// cancelled.  pushURL should be the full remote endpoint, for example:
//   - vmagent:  http://vmagent:8429/api/v1/import/prometheus
//   - Pushgateway: http://pushgw:9091/metrics/job/configserver
func (c *Collector) StartPush(ctx context.Context, pushURL string, interval time.Duration) {
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
				if err := c.push(ctx, pushURL); err != nil {
					log.Printf("WARN: metrics push to %s: %v", pushURL, err)
				}
			}
		}
	}()
}

func (c *Collector) push(ctx context.Context, url string) error {
	text, err := c.Collect(ctx)
	if err != nil {
		return fmt.Errorf("collect: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(text))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("remote returned HTTP %d", resp.StatusCode)
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

func agentLabelValue(agent *model.Agent, getter func(*model.Agent) string) string {
	if agent == nil {
		return ""
	}
	return getter(agent)
}
