package middleware_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/delivery/httpapi/middleware"
	"github.com/whoyoshome/flashdeal-service/internal/platform/auth"
)

type fakeLimiter struct {
	res middleware.RateLimitResult
	err error
	n   int
}

func (f *fakeLimiter) Allow(context.Context, string) (middleware.RateLimitResult, error) {
	f.n++
	return f.res, f.err
}

func TestRateLimit(t *testing.T) {
	t.Parallel()

	tokens := testAuth(t)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	t.Run("is a no-op when the limiter is nil", func(t *testing.T) {
		h := middleware.Auth(tokens)(middleware.RateLimit(nil, nil)(ok))
		rec := serve(t, h, newRequest(t, map[string]string{
			"Authorization": bearer(t, tokens, "alice", auth.RoleUser),
		}))
		require.Equal(t, http.StatusNoContent, rec.Code)
	})

	t.Run("allows a request under budget and sets limit headers", func(t *testing.T) {
		lim := &fakeLimiter{res: middleware.RateLimitResult{Allowed: true, Remaining: 9, Limit: 10}}
		h := middleware.Auth(tokens)(middleware.RateLimit(lim, nil)(ok))
		rec := serve(t, h, newRequest(t, map[string]string{
			"Authorization": bearer(t, tokens, "alice", auth.RoleUser),
		}))
		require.Equal(t, http.StatusNoContent, rec.Code)
		require.Equal(t, "10", rec.Header().Get("X-RateLimit-Limit"))
		require.Equal(t, "9", rec.Header().Get("X-RateLimit-Remaining"))
		require.Equal(t, 1, lim.n)
	})

	t.Run("rejects an over-budget caller with 429", func(t *testing.T) {
		lim := &fakeLimiter{res: middleware.RateLimitResult{
			Allowed: false, Remaining: 0, Limit: 10, RetryAfter: 1500 * time.Millisecond,
		}}
		h := middleware.Auth(tokens)(middleware.RateLimit(lim, nil)(ok))
		rec := serve(t, h, newRequest(t, map[string]string{
			"Authorization": bearer(t, tokens, "alice", auth.RoleUser),
		}))
		require.Equal(t, http.StatusTooManyRequests, rec.Code)
		require.Equal(t, "2", rec.Header().Get("Retry-After"))
		var body errorBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, "RATE_LIMITED", body.Code)
	})

	t.Run("fails closed with 503 when the limiter errors", func(t *testing.T) {
		lim := &fakeLimiter{err: errors.New("redis down")}
		h := middleware.Auth(tokens)(middleware.RateLimit(lim, nil)(ok))
		rec := serve(t, h, newRequest(t, map[string]string{
			"Authorization": bearer(t, tokens, "alice", auth.RoleUser),
		}))
		require.Equal(t, http.StatusServiceUnavailable, rec.Code)
		var body errorBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, "STOCK_UNAVAILABLE", body.Code)
	})
}
