package middleware

import (
	"context"
	"net/http"
	"time"
)

// Timeout bounds the time a request may spend in the service. It sets a
// context deadline that every Postgres and Redis call honors, so a slow
// dependency fails the request instead of piling up goroutines. Work that
// must outlive the request detaches with context.WithoutCancel.
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
