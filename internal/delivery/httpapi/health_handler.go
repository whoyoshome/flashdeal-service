package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
)

// Pinger reports whether a dependency is reachable. Declaring it here, where
// it is consumed, keeps this package free of database and Redis drivers.
type Pinger interface {
	Ping(ctx context.Context) error
}

// ReadinessCheck names a dependency that must be reachable before the
// instance accepts traffic.
type ReadinessCheck struct {
	Name   string
	Pinger Pinger
}

const (
	healthOK          = "ok"
	healthUnavailable = "unavailable"
)

// liveness answers whether the process is up. It never checks dependencies:
// a database outage must not make the orchestrator restart healthy replicas.
func (h *handlers) liveness(w http.ResponseWriter, r *http.Request) {
	h.writeJSON(w, r, http.StatusOK, healthResponse{Status: healthOK})
}

// readiness answers whether the instance can serve traffic, so the load
// balancer stops routing to it while a dependency is down.
func (h *handlers) readiness(w http.ResponseWriter, r *http.Request) {
	// Probe concurrently so the worst case is one timeout, not one per
	// dependency; probes would otherwise exceed the orchestrator's deadline.
	errs := make([]error, len(h.readinessChecks))
	var wg sync.WaitGroup
	for i, c := range h.readinessChecks {
		wg.Go(func() { errs[i] = h.probe(r.Context(), c) })
	}
	wg.Wait()

	resp := healthResponse{Status: healthOK, Checks: make(map[string]string, len(h.readinessChecks))}
	status := http.StatusOK
	for i, c := range h.readinessChecks {
		if err := errs[i]; err != nil {
			// Expected during outages and polled every few seconds, so this
			// is a warning; error details stay in the log, never in the body.
			h.log.WarnContext(r.Context(), "readiness check failed",
				slog.String("dependency", c.Name), slog.Any("error", err))
			resp.Checks[c.Name] = healthUnavailable
			resp.Status = healthUnavailable
			status = http.StatusServiceUnavailable
			continue
		}
		resp.Checks[c.Name] = healthOK
	}

	h.writeJSON(w, r, status, resp)
}

func (h *handlers) probe(ctx context.Context, c ReadinessCheck) error {
	ctx, cancel := context.WithTimeout(ctx, h.readinessTimeout)
	defer cancel()
	return c.Pinger.Ping(ctx)
}
