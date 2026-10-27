package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// RateLimitResult is what a RateLimiter returns for one request.
type RateLimitResult struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Duration
	Limit      int
}

// RateLimiter decides whether a subject may proceed. Redis-backed token
// buckets implement it; nil disables the middleware.
type RateLimiter interface {
	Allow(ctx context.Context, subject string) (RateLimitResult, error)
}

// RateLimit spends one token for the authenticated subject. It must run
// after Auth. Over-budget callers get 429 RATE_LIMITED with Retry-After.
// Redis failures fail closed as 503 STOCK_UNAVAILABLE: without the limiter
// the service would admit unlimited authenticated traffic.
func RateLimit(limiter RateLimiter, log *slog.Logger) func(http.Handler) http.Handler {
	if limiter == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			subject := UserIDFrom(r.Context())
			if subject == "" {
				writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "missing or invalid Bearer token")
				return
			}
			res, err := limiter.Allow(r.Context(), subject)
			if err != nil {
				if log != nil {
					log.WarnContext(r.Context(), "rate limit unavailable",
						slog.String("request_id", RequestIDFrom(r.Context())),
						slog.Any("error", err))
				}
				writeError(w, r, http.StatusServiceUnavailable, "STOCK_UNAVAILABLE", "stock service unavailable")
				return
			}
			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(res.Limit))
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(res.Remaining))
			if !res.Allowed {
				secs := int(res.RetryAfter.Round(time.Second) / time.Second)
				if secs < 1 {
					secs = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(secs))
				writeError(w, r, http.StatusTooManyRequests, "RATE_LIMITED", "rate limit exceeded, retry later")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
