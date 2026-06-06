// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

// Command configserver runs only the Agent API endpoint.
// For distributed deployments this process handles agent heartbeats while
// cmd/admin handles the operator WebUI.  Both processes share state via
// Redis (L2 cache + Pub/Sub) and MySQL.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/alibaba/ilogtail/config_server/internal/cache"
	"github.com/alibaba/ilogtail/config_server/internal/config"
	agentserver "github.com/alibaba/ilogtail/config_server/internal/server/agent"
	"github.com/alibaba/ilogtail/config_server/internal/store/gormdb"
	"github.com/redis/go-redis/v9"
)

func main() {
	var cfgFile string
	flag.StringVar(&cfgFile, "config", "", "Path to YAML configuration file (env vars override YAML values)")
	flag.Parse()

	cfg := config.Load(cfgFile)

	if err := cfg.ValidateDistributed(); err != nil {
		log.Fatalf("config validation: %v", err)
	}

	st, err := gormdb.New(cfg.DBDriver, cfg.DBDSN, gormdb.PoolConfig{
		MaxOpenConns:    cfg.DBPoolMaxOpenConns,
		MaxIdleConns:    cfg.DBPoolMaxIdleConns,
		ConnMaxLifetime: cfg.DBPoolConnMaxLifetime,
		ConnMaxIdleTime: cfg.DBPoolConnMaxIdleTime,
	})
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	rdb := redis.NewUniversalClient(&redis.UniversalOptions{
		Addrs:    cfg.RedisAddrs,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})

	mgr := cache.New(st, rdb, cfg.L1MaxSizeMB, cfg.L1TTL, cfg.L2TTL, cfg.RedisHFE)
	agentSrv := agentserver.NewAgentServer(cfg.AgentAddr(), mgr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start background GC for stale agents (TTL 30 min, scan every 5 min).
	mgr.StartGC(ctx)

	go func() {
		if err := agentSrv.Start(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("agent server: %v", err)
		}
	}()

	log.Printf("ConfigServer (agent-only) started on %s", cfg.AgentAddr())
	<-ctx.Done()
	log.Println("Shutting down...")
	_ = agentSrv.Shutdown(context.Background())
}
