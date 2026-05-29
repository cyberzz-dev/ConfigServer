// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

package admin

import (
	"context"
	"io/fs"
	"log"
	"net/http"
	"time"

	"github.com/alibaba/ilogtail/config_server/internal/cache"
)

// AdminServer serves the REST API and the embedded React WebUI.
type AdminServer struct {
	srv *http.Server
}

// NewAdminServer constructs an AdminServer.
// webFS is the embedded filesystem containing the built WebUI (webui/dist/).
// Pass nil to disable the WebUI (API-only mode).
// metricsHandler, when non-nil, is registered at GET /metrics for Prometheus
// scraping.  The endpoint is not protected by session auth.
func NewAdminServer(addr string, mgr *cache.Manager, webFS fs.FS, metricsHandler http.Handler) *AdminServer {
	// Switch to Redis-backed session store when Redis is available.
	initSessionStore(mgr.Redis())

	mux := http.NewServeMux()

	// REST API routes.
	h := NewAdminHandler(mgr)
	RegisterAdminRoutes(mux, h)

	// Prometheus scrape endpoint — publicly accessible (no /api/v1/ prefix,
	// so the requireAuth middleware passes it through unconditionally).
	if metricsHandler != nil {
		mux.Handle("GET /metrics", metricsHandler)
	}

	// Serve the React SPA.
	if webFS != nil {
		static := http.FileServerFS(webFS)
		// All non-API requests fall through to the SPA so that client-side
		// routing works correctly (index.html for any unknown path).
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			// Try to serve the actual file; if not found, serve index.html.
			f, err := webFS.Open(r.URL.Path[1:]) // strip leading "/"
			if err != nil {
				http.ServeFileFS(w, r, webFS, "index.html")
				return
			}
			f.Close()
			static.ServeHTTP(w, r)
		})
	}

	// Wrap the entire mux with the session-based auth middleware.
	// Only /api/v1/... paths (except /api/v1/auth/...) are protected.
	// Agent gRPC paths (/Agent/...) live on a separate AgentServer and are
	// never affected by this middleware.
	return &AdminServer{
		srv: &http.Server{
			Addr:         addr,
			Handler:      requireAuth(mgr, mux),
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 15 * time.Second,
			IdleTimeout:  60 * time.Second,
		},
	}
}

// Start begins listening. It blocks until the server stops.
func (s *AdminServer) Start() error {
	log.Printf("Admin API server listening on %s", s.srv.Addr)
	return s.srv.ListenAndServe()
}

// Shutdown gracefully stops the server within the provided timeout.
func (s *AdminServer) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}
