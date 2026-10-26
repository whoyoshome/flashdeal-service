package middleware

import (
	"net/http"
	"time"
)

// HTTPObserver records handled requests. It is satisfied by the Prometheus
// metrics in platform/observability, so this package does not depend on any
// metrics library.
type HTTPObserver interface {
	ObserveHTTP(method, route string, status int, d time.Duration)
}

// Metrics records every request's status and latency by route pattern. It
// sits outside the timeout, so a request that timed out is measured with
// the status the client actually received.
func Metrics(obs HTTPObserver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)

			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			obs.ObserveHTTP(r.Method, routePattern(r), status, time.Since(start))
		})
	}
}
