// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

// Command allinone runs both the Agent API and the Admin API in a single
// process, sharing the same in-process cache and DB connection.
// This mode operates without Redis (L1 + SQLite by default).
package main

import (
	"context"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/alibaba/ilogtail/config_server/internal/cache"
	"github.com/alibaba/ilogtail/config_server/internal/config"
	"github.com/alibaba/ilogtail/config_server/internal/metrics"
	adminserver "github.com/alibaba/ilogtail/config_server/internal/server/admin"
	agentserver "github.com/alibaba/ilogtail/config_server/internal/server/agent"
	"github.com/alibaba/ilogtail/config_server/internal/store/gormdb"
	"github.com/alibaba/ilogtail/config_server/webui"
)

func main() {
	var cfgFile string
	flag.StringVar(&cfgFile, "config", "", "Path to YAML configuration file (env vars override YAML values)")
	flag.Parse()

	cfg := config.Load(cfgFile)

	// Open DB (SQLite by default, MySQL if configured).
	st, err := gormdb.New(cfg.DBDriver, cfg.DBDSN, gormdb.PoolConfig{
		MaxOpenConns:    cfg.DBPoolMaxOpenConns,
		MaxIdleConns:    cfg.DBPoolMaxIdleConns,
		ConnMaxLifetime: cfg.DBPoolConnMaxLifetime,
		ConnMaxIdleTime: cfg.DBPoolConnMaxIdleTime,
	})
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	// In All-in-One mode Redis is optional; nil means L1+DB only.
	mgr := cache.New(st, nil /* no Redis */, cfg.L1MaxSizeMB, cfg.L1TTL, cfg.L2TTL)

	// Metrics collector: always available for Prometheus scraping at /metrics.
	// Push to a remote endpoint only when MetricsPushURL is configured.
	mc := metrics.New(mgr, cfg.MetricsOnlineWindow)

	webFS, err := fs.Sub(webui.Dist, "dist")
	if err != nil {
		log.Fatalf("embed fs: %v", err)
	}

	agentSrv := agentserver.NewAgentServer(cfg.AgentAddr(), mgr)
	adminSrv := adminserver.NewAdminServer(cfg.AdminAddr(), mgr, webFS, mc.Handler())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start metrics push loop if a remote endpoint is configured.
	mc.StartPush(ctx, cfg.MetricsPushURL, cfg.MetricsPushInterval)
	// Start background GC for stale agents (TTL 30 min, scan every 5 min).
	mgr.StartGC(ctx)

	go func() {
		if err := agentSrv.Start(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("agent server: %v", err)
		}
	}()
	go func() {
		if err := adminSrv.Start(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("admin server: %v", err)
		}
	}()

	log.Printf("All-in-One ConfigServer started (agent=%s, admin=%s, db=%s/%s)",
		cfg.AgentAddr(), cfg.AdminAddr(), cfg.DBDriver, maskDSN(cfg.DBDSN))

	<-ctx.Done()
	log.Println("Shutting down...")

	shutCtx := context.Background()
	_ = agentSrv.Shutdown(shutCtx)
	_ = adminSrv.Shutdown(shutCtx)
}

// maskDSN replaces the password in a DSN string with "***" so that
// credentials are never printed in plain text in log output.
// Supports the go-sql-driver/mysql format: user:password@protocol(addr)/dbname
// For DSNs without a password (e.g. SQLite file paths) the string is returned
// unchanged.
func maskDSN(dsn string) string {
	// Find the last '@' that separates credentials from host.
	at := -1
	for i := len(dsn) - 1; i >= 0; i-- {
		if dsn[i] == '@' {
			at = i
			break
		}
	}
	if at < 0 {
		return dsn // no credentials section
	}
	creds := dsn[:at]
	colon := -1
	for i := 0; i < len(creds); i++ {
		if creds[i] == ':' {
			colon = i
			break
		}
	}
	if colon < 0 {
		return dsn // no password
	}
	return creds[:colon+1] + "***" + dsn[at:]
}
