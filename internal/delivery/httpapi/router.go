// Package httpapi translates HTTP requests into application calls and maps
// the results back to HTTP. It is not named "http" to avoid shadowing net/http.
package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// Deps holds everything the HTTP layer needs. It is assembled in cmd/api,
// the only place that knows the concrete implementations.
type Deps struct {
	Log *slog.Logger
	// ReadinessChecks are probed in order by GET /readyz.
	ReadinessChecks []ReadinessCheck
	// ReadinessTimeout bounds each individual readiness probe.
	ReadinessTimeout time.Duration
}

// NewRouter builds the HTTP handler tree.
func NewRouter(d Deps) http.Handler {
	h := &handlers{
		log:              d.Log,
		readinessChecks:  d.ReadinessChecks,
		readinessTimeout: d.ReadinessTimeout,
	}

	r := chi.NewRouter()
	r.Get("/healthz", h.liveness)
	r.Get("/readyz", h.readiness)
	return r
}

type handlers struct {
	log              *slog.Logger
	readinessChecks  []ReadinessCheck
	readinessTimeout time.Duration
}
