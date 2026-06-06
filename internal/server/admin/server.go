// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

package admin

import (
	"compress/gzip"
	"context"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/alibaba/ilogtail/config_server/internal/cache"
	"github.com/alibaba/ilogtail/config_server/internal/config"
)

// gzipPool reuses gzip.Writer instances to avoid per-request allocations.
var gzipPool = sync.Pool{
	New: func() any { w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed); return w },
}

// gzipResponseWriter wraps http.ResponseWriter to compress the response body.
type gzipResponseWriter struct {
	http.ResponseWriter
	gz *gzip.Writer
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) { return g.gz.Write(b) }
func (g *gzipResponseWriter) WriteHeader(code int) {
	g.ResponseWriter.Header().Del("Content-Length")
	g.ResponseWriter.WriteHeader(code)
}

// compressibleType reports whether the Content-Type should be gzip-compressed.
func compressibleType(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.HasPrefix(ct, "text/") ||
		strings.Contains(ct, "javascript") ||
		strings.Contains(ct, "json") ||
		strings.Contains(ct, "xml") ||
		strings.Contains(ct, "svg") ||
		strings.Contains(ct, "wasm")
}

// gzipMiddleware wraps next and compresses responses when the client accepts gzip
// and the Content-Type is text-like.
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		// Determine Content-Type from path extension so we can decide early.
		ext := strings.ToLower(filepath.Ext(r.URL.Path))
		ct := mime.TypeByExtension(ext)
		if !compressibleType(ct) {
			next.ServeHTTP(w, r)
			return
		}
		gz := gzipPool.Get().(*gzip.Writer)
		gz.Reset(w)
		defer func() {
			gz.Close()
			gzipPool.Put(gz)
		}()
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Del("Content-Length")
		next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, gz: gz}, r)
	})
}

// AdminServer serves the REST API and the embedded React WebUI.
type AdminServer struct {
	srv *http.Server
}

// NewAdminServer constructs an AdminServer.
// webFS is the embedded filesystem containing the built WebUI (webui/dist/).
// Pass nil to disable the WebUI (API-only mode).
// metricsHandler, when non-nil, is registered at GET /metrics for Prometheus
// scraping.  The endpoint is not protected by session auth.
func NewAdminServer(addr string, mgr *cache.Manager, webFS fs.FS, metricsHandler http.Handler, smtp config.SMTPConfig) *AdminServer {
	// Switch to Redis-backed session store when Redis is available.
	initSessionStore(mgr.Redis())

	mux := http.NewServeMux()

	// REST API routes.
	h := NewAdminHandler(mgr, smtp)
	RegisterAdminRoutes(mux, h)

	// Prometheus scrape endpoint — publicly accessible (no /api/v1/ prefix,
	// so the requireAuth middleware passes it through unconditionally).
	if metricsHandler != nil {
		mux.Handle("GET /metrics", metricsHandler)
	}

	// Serve the React SPA.
	if webFS != nil {
		static := gzipMiddleware(http.FileServerFS(webFS))
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
