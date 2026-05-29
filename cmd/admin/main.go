// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

// Command admin runs the Admin REST API and serves the embedded React WebUI.
// In distributed deployments this process is scaled independently from cmd/configserver.
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
	"github.com/alibaba/ilogtail/config_server/internal/store/gormdb"
	"github.com/alibaba/ilogtail/config_server/webui"
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
		Addrs:    []string{cfg.RedisAddr},
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})

	mgr := cache.New(st, rdb, cfg.L1MaxSizeMB, cfg.L1TTL, cfg.L2TTL)

	mc := metrics.New(mgr, cfg.MetricsOnlineWindow)

	// Expose only the dist sub-tree as the root of the static FS.
	webFS, err := fs.Sub(webui.Dist, "dist")
	if err != nil {
		log.Fatalf("embed fs: %v", err)
	}

	adminSrv := adminserver.NewAdminServer(cfg.AdminAddr(), mgr, webFS, mc.Handler())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mc.StartPush(ctx, cfg.MetricsPushURL, cfg.MetricsPushInterval)
	mgr.StartGC(ctx)

	go func() {
		if err := adminSrv.Start(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("admin server: %v", err)
		}
	}()

	log.Printf("Admin server started on %s", cfg.AdminAddr())
	<-ctx.Done()
	log.Println("Shutting down...")
	_ = adminSrv.Shutdown(context.Background())
}
