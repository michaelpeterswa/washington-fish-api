// Package api hosts the public HTTP surface: a chi router with ops probes and
// the versioned /v1 group. Handlers are added per build phase; phase 1 wires
// only health/readiness.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/michaelpeterswa/washington-fish-api/internal/predict"
	"github.com/michaelpeterswa/washington-fish-api/internal/predict/bite"
	"github.com/michaelpeterswa/washington-fish-api/internal/store"
)

// Server bundles the dependencies handlers need.
type Server struct {
	Store       *store.Store
	Predict     *predict.Service
	Logger      *slog.Logger
	DefaultUnit bite.TempUnit // temperature unit when ?units= is absent
}

// NewRouter builds the chi router with middleware and routes mounted.
func NewRouter(s *Server) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	// Liveness: process is up. No dependency checks.
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Root: plain-text landing pointing at the docs.
	r.Get("/", s.handleRoot)

	// Readiness: dependencies (DB) reachable.
	r.Get("/readyz", s.handleReady)

	// API reference: Scalar-rendered docs at /docs over the embedded spec.
	r.Get("/openapi.yaml", s.handleOpenAPISpec)
	r.Get("/docs", s.handleDocs)
	r.Get("/docs/scalar.js", s.handleScalarJS)

	r.Route("/v1", func(r chi.Router) {
		r.Get("/lakes", s.handleSearchLakes)
		r.Get("/lakes/{id}", s.handleLakeDetail)
		r.Get("/lakes/{id}/species", s.handleLakeSpecies)
		r.Get("/lakes/{id}/prediction", s.handleLakePrediction)
		r.Get("/rank", s.handleRank)
		// POST /catch-logs lands in phase 6.
	})

	return r
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) handleReady(w http.ResponseWriter, req *http.Request) {
	ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
	defer cancel()

	if s.Store != nil {
		if err := s.Store.Ping(ctx); err != nil {
			s.Logger.WarnContext(ctx, "readiness check failed", slog.String("error", err.Error()))
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "reason": "database"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
