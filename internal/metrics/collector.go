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
//   - NO per-agent label series (instance_id, hostname, IP) — these would create
//     one time-series per agent and cause cardinality explosion in the TSDB.
//   - Only bounded-label dimensions are used: running_status (~5 values),
//     agent_type (~10 values).  Version is intentionally excluded as the label
//     value space is unbounded; callers that need per-version counts should build
//     a separate recording rule from the agent list API.
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
	"strings"
	"time"

	"github.com/alibaba/ilogtail/config_server/internal/model"
)

// AgentLister is satisfied by *cache.Manager.
type AgentLister interface {
	ListAgents(ctx context.Context) ([]*model.Agent, error)
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

	now := time.Now()
	var total, online int

	// Bounded-cardinality dimensions.
	statusCounts := map[string]int{}
	typeCounts := map[string]int{}

	for _, a := range agents {
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
