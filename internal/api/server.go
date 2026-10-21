// Package api serves the LogFlux HTTP API (health, metrics, query, alerts).
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/fatihserhatturan/logflux/internal/metrics"
)

// Server is the LogFlux API server
type Server struct {
	addr    string
	mux     *http.ServeMux
	metrics *metrics.Registry
	started time.Time

	// baseCtx is the parent of every request context; cancelling it on
	// shutdown ends long-lived streams (/api/tail)
	baseCtx    context.Context
	cancelBase context.CancelFunc

	mu     sync.Mutex
	server *http.Server
	ln     net.Listener
}

// New creates an API server with /health and /metrics registered
func New(addr string, reg *metrics.Registry) *Server {
	s := &Server{
		addr:    addr,
		mux:     http.NewServeMux(),
		metrics: reg,
		started: time.Now(),
	}
	s.baseCtx, s.cancelBase = context.WithCancel(context.Background())
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/metrics", s.handleMetrics)
	return s
}

// Handle registers an additional handler
func (s *Server) Handle(pattern string, h http.Handler) {
	s.mux.Handle(pattern, h)
}

// HandleFunc registers an additional handler function
func (s *Server) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	s.mux.HandleFunc(pattern, h)
}

// Start binds the listener and serves in the background
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("api: failed to listen on %s: %w", s.addr, err)
	}
	srv := &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return s.baseCtx },
	}

	s.mu.Lock()
	s.ln = ln
	s.server = srv
	s.mu.Unlock()

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			fmt.Printf("api: server error: %v\n", err)
		}
	}()
	return nil
}

// Addr returns the actual listening address
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		return s.ln.Addr().String()
	}
	return s.addr
}

// Shutdown gracefully stops the server
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	srv := s.server
	s.mu.Unlock()
	s.cancelBase()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "healthy",
		"time":   time.Now().Format(time.RFC3339),
		"uptime": time.Since(s.started).Round(time.Second).String(),
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = s.metrics.WriteTo(w)
}

// writeJSON writes v as a JSON response
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
