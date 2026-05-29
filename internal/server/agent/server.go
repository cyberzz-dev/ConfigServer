// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

package agent

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/alibaba/ilogtail/config_server/internal/cache"
)

// AgentServer is the HTTP server that serves the agent-facing protocol API.
type AgentServer struct {
	srv *http.Server
}

// NewAgentServer constructs an AgentServer listening on addr.
func NewAgentServer(addr string, mgr *cache.Manager) *AgentServer {
	mux := http.NewServeMux()
	h := NewAgentHandler(mgr)
	RegisterAgentRoutes(mux, h)

	return &AgentServer{
		srv: &http.Server{
			Addr:         addr,
			Handler:      mux,
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 15 * time.Second,
			IdleTimeout:  60 * time.Second,
		},
	}
}

// Start begins listening. It blocks until the server stops.
func (s *AgentServer) Start() error {
	log.Printf("Agent API server listening on %s", s.srv.Addr)
	return s.srv.ListenAndServe()
}

// Shutdown gracefully stops the server within the provided timeout.
func (s *AgentServer) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}
