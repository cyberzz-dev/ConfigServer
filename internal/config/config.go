// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

// Package config loads server configuration from an optional YAML file and/or
// environment variables.
//
// Priority (highest wins): environment variables > YAML file > built-in defaults.
package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all configserver settings.
type Config struct {
	// Database
	DBDriver string // sqlite | mysql
	DBDSN    string // file path for sqlite, DSN for mysql

	// Connection pool (applies to both SQLite and MySQL)
	DBPoolMaxOpenConns    int           // max concurrent connections
	DBPoolMaxIdleConns    int           // idle connections to keep open
	DBPoolConnMaxLifetime time.Duration // max time a connection may be reused
	DBPoolConnMaxIdleTime time.Duration // max time a connection may be idle

	// Redis (optional in All-in-One mode)
	RedisAddr     string
	RedisPassword string
	RedisDB       int

	// Cache
	L1MaxSizeMB int
	L1TTL       time.Duration
	L2TTL       time.Duration

	// Agent API server
	AgentHost string
	AgentPort int

	// Admin API server
	AdminHost string
	AdminPort int

	// Metrics
	// MetricsPushURL is the remote endpoint for pushing Prometheus text metrics
	// (e.g. http://vmagent:8429/api/v1/import/prometheus or a Pushgateway URL).
	// Leave empty to disable push; /metrics scrape endpoint is always available.
	MetricsPushURL      string
	MetricsPushInterval time.Duration
	MetricsOnlineWindow time.Duration // heartbeat age threshold for "online" status
}

// yamlFile is the YAML-decoded representation of the config file.
// Duration fields are strings (e.g. "5m", "30s") parsed by time.ParseDuration.
type yamlFile struct {
	Database struct {
		Driver string `yaml:"driver"`
		DSN    string `yaml:"dsn"`
		Pool   struct {
			MaxOpenConns    int `yaml:"max_open_conns"`
			MaxIdleConns    int `yaml:"max_idle_conns"`
			ConnMaxLifetime int `yaml:"conn_max_lifetime"`  // seconds
			ConnMaxIdleTime int `yaml:"conn_max_idle_time"` // seconds
		} `yaml:"pool"`
	} `yaml:"database"`
	Redis struct {
		Addr     string `yaml:"addr"`
		Password string `yaml:"password"`
		DB       int    `yaml:"db"`
	} `yaml:"redis"`
	Cache struct {
		L1MaxSizeMB int    `yaml:"l1_max_size_mb"`
		L1TTL       string `yaml:"l1_ttl"`
		L2TTL       string `yaml:"l2_ttl"`
	} `yaml:"cache"`
	AgentServer struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
	} `yaml:"agent_server"`
	AdminServer struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
	} `yaml:"admin_server"`
	Metrics struct {
		PushURL      string `yaml:"push_url"`
		PushInterval string `yaml:"push_interval"`
		OnlineWindow string `yaml:"online_window"`
	} `yaml:"metrics"`
}

// Load builds a Config with the following priority (highest wins):
//
//	environment variables > YAML file (file argument) > built-in defaults
//
// Pass an empty string for file to skip YAML loading.
func Load(file string) *Config {
	c := configDefaults()
	if file != "" {
		if err := applyFile(c, file); err != nil {
			log.Fatalf("config file %q: %v", file, err)
		}
		log.Printf("Loaded configuration from %s", file)
	}
	applyEnv(c)
	return c
}

// configDefaults returns a Config populated with built-in defaults.
func configDefaults() *Config {
	return &Config{
		DBDriver:              "sqlite",
		DBDSN:                 "configserver.db",
		DBPoolMaxOpenConns:    25,
		DBPoolMaxIdleConns:    10,
		DBPoolConnMaxLifetime: 3600 * time.Second,
		DBPoolConnMaxIdleTime: 600 * time.Second,
		RedisAddr:             "",
		RedisPassword:         "",
		RedisDB:               0,
		L1MaxSizeMB:           100,
		L1TTL:                 5 * time.Minute,
		L2TTL:                 30 * 24 * time.Hour,
		AgentHost:             "0.0.0.0",
		AgentPort:             8080,
		AdminHost:             "0.0.0.0",
		AdminPort:             8081,
		MetricsPushURL:        "",
		MetricsPushInterval:   30 * time.Second,
		MetricsOnlineWindow:   5 * time.Minute,
	}
}

// applyFile reads path as YAML and merges non-zero values into c.
func applyFile(c *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	var yf yamlFile
	if err := yaml.Unmarshal(data, &yf); err != nil {
		return fmt.Errorf("parse yaml: %w", err)
	}

	parseDur := func(s string, fallback time.Duration) time.Duration {
		if s == "" {
			return fallback
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			log.Printf("WARN: config file: invalid duration %q, using %s", s, fallback)
			return fallback
		}
		return d
	}

	if v := yf.Database.Driver; v != "" {
		c.DBDriver = v
	}
	if v := yf.Database.DSN; v != "" {
		c.DBDSN = v
	}
	if v := yf.Database.Pool.MaxOpenConns; v != 0 {
		c.DBPoolMaxOpenConns = v
	}
	if v := yf.Database.Pool.MaxIdleConns; v != 0 {
		c.DBPoolMaxIdleConns = v
	}
	if v := yf.Database.Pool.ConnMaxLifetime; v != 0 {
		c.DBPoolConnMaxLifetime = time.Duration(v) * time.Second
	}
	if v := yf.Database.Pool.ConnMaxIdleTime; v != 0 {
		c.DBPoolConnMaxIdleTime = time.Duration(v) * time.Second
	}
	if v := yf.Redis.Addr; v != "" {
		c.RedisAddr = v
	}
	// Password may legitimately be empty; always apply when redis section is present.
	c.RedisPassword = yf.Redis.Password
	if yf.Redis.DB != 0 {
		c.RedisDB = yf.Redis.DB
	}
	if yf.Cache.L1MaxSizeMB != 0 {
		c.L1MaxSizeMB = yf.Cache.L1MaxSizeMB
	}
	c.L1TTL = parseDur(yf.Cache.L1TTL, c.L1TTL)
	c.L2TTL = parseDur(yf.Cache.L2TTL, c.L2TTL)
	if v := yf.AgentServer.Host; v != "" {
		c.AgentHost = v
	}
	if yf.AgentServer.Port != 0 {
		c.AgentPort = yf.AgentServer.Port
	}
	if v := yf.AdminServer.Host; v != "" {
		c.AdminHost = v
	}
	if yf.AdminServer.Port != 0 {
		c.AdminPort = yf.AdminServer.Port
	}
	if v := yf.Metrics.PushURL; v != "" {
		c.MetricsPushURL = v
	}
	c.MetricsPushInterval = parseDur(yf.Metrics.PushInterval, c.MetricsPushInterval)
	c.MetricsOnlineWindow = parseDur(yf.Metrics.OnlineWindow, c.MetricsOnlineWindow)

	return nil
}

// applyEnv overrides any Config field whose corresponding environment variable
// is explicitly set (even to empty string for string fields).
func applyEnv(c *Config) {
	setStr := func(dest *string, key string) {
		if v, ok := os.LookupEnv(key); ok {
			*dest = v
		}
	}
	setInt := func(dest *int, key string) {
		v, ok := os.LookupEnv(key)
		if !ok || v == "" {
			return
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			log.Printf("WARN: invalid %s=%q, keeping current value %d", key, v, *dest)
			return
		}
		*dest = n
	}
	setDur := func(dest *time.Duration, key string) {
		v, ok := os.LookupEnv(key)
		if !ok || v == "" {
			return
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			log.Printf("WARN: invalid %s=%q, keeping current value %s", key, v, *dest)
			return
		}
		*dest = d
	}

	// setIntDur reads an integer env var and converts it to time.Duration (seconds).
	setIntDur := func(dest *time.Duration, key string) {
		v, ok := os.LookupEnv(key)
		if !ok || v == "" {
			return
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			log.Printf("WARN: invalid %s=%q, keeping current value %s", key, v, *dest)
			return
		}
		*dest = time.Duration(n) * time.Second
	}

	setStr(&c.DBDriver, "CONFIGSERVER_DB_DRIVER")
	setStr(&c.DBDSN, "CONFIGSERVER_DB_DSN")
	setInt(&c.DBPoolMaxOpenConns, "CONFIGSERVER_DB_POOL_MAX_OPEN_CONNS")
	setInt(&c.DBPoolMaxIdleConns, "CONFIGSERVER_DB_POOL_MAX_IDLE_CONNS")
	setIntDur(&c.DBPoolConnMaxLifetime, "CONFIGSERVER_DB_POOL_CONN_MAX_LIFETIME")
	setIntDur(&c.DBPoolConnMaxIdleTime, "CONFIGSERVER_DB_POOL_CONN_MAX_IDLE_TIME")
	setStr(&c.RedisAddr, "CONFIGSERVER_REDIS_ADDR")
	setStr(&c.RedisPassword, "CONFIGSERVER_REDIS_PASSWORD")
	setInt(&c.RedisDB, "CONFIGSERVER_REDIS_DB")
	setInt(&c.L1MaxSizeMB, "CONFIGSERVER_L1_MAX_SIZE_MB")
	setDur(&c.L1TTL, "CONFIGSERVER_L1_TTL")
	setDur(&c.L2TTL, "CONFIGSERVER_L2_TTL")
	setStr(&c.AgentHost, "CONFIGSERVER_AGENT_HOST")
	setInt(&c.AgentPort, "CONFIGSERVER_AGENT_PORT")
	setStr(&c.AdminHost, "CONFIGSERVER_ADMIN_HOST")
	setInt(&c.AdminPort, "CONFIGSERVER_ADMIN_PORT")
	setStr(&c.MetricsPushURL, "CONFIGSERVER_METRICS_PUSH_URL")
	setDur(&c.MetricsPushInterval, "CONFIGSERVER_METRICS_PUSH_INTERVAL")
	setDur(&c.MetricsOnlineWindow, "CONFIGSERVER_METRICS_ONLINE_WINDOW")
}

// RedisEnabled returns true when a Redis address is configured.
func (c *Config) RedisEnabled() bool {
	return c.RedisAddr != ""
}

// AgentAddr returns the listen address for the agent API server.
func (c *Config) AgentAddr() string {
	return fmt.Sprintf("%s:%d", c.AgentHost, c.AgentPort)
}

// AdminAddr returns the listen address for the admin API server.
func (c *Config) AdminAddr() string {
	return fmt.Sprintf("%s:%d", c.AdminHost, c.AdminPort)
}

// ValidateDistributed checks that required fields are set for distributed (non-All-in-One) mode.
func (c *Config) ValidateDistributed() error {
	if c.DBDriver != "mysql" {
		return fmt.Errorf("distributed mode requires database.driver=mysql (got %q)", c.DBDriver)
	}
	if c.RedisAddr == "" {
		return fmt.Errorf("distributed mode requires redis.addr to be set")
	}
	return nil
}
