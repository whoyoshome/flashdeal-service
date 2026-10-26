package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/delivery/httpapi"
)

// fakePinger returns err, or blocks until ctx is done when block is set.
type fakePinger struct {
	err   error
	block bool
}

func (f fakePinger) Ping(ctx context.Context) error {
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.err
}

// rendezvousPinger succeeds only if its partner is probed at the same time:
// it announces itself on mine and waits for other. Probed sequentially, the
// first one would wait for a partner that never starts and time out.
type rendezvousPinger struct {
	mine, other chan struct{}
}

func (p rendezvousPinger) Ping(ctx context.Context) error {
	close(p.mine)
	select {
	case <-p.other:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type healthBody struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

func newRouter(checks ...httpapi.ReadinessCheck) http.Handler {
	return httpapi.NewRouter(httpapi.Deps{
		Log:              slog.New(slog.DiscardHandler),
		ReadinessChecks:  checks,
		ReadinessTimeout: 20 * time.Millisecond,
	})
}

func get(t *testing.T, h http.Handler, path string) (*httptest.ResponseRecorder, healthBody, string) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	raw := rec.Body.String()
	var body healthBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return rec, body, raw
}

func TestLiveness(t *testing.T) {
	t.Parallel()

	t.Run("returns 200 even when a dependency is down", func(t *testing.T) {
		t.Parallel()
		h := newRouter(httpapi.ReadinessCheck{Name: "postgres", Pinger: fakePinger{err: errors.New("connection refused")}})

		rec, body, _ := get(t, h, "/healthz")

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
		require.Equal(t, "ok", body.Status)
	})
}

func TestReadiness(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		postgres   fakePinger
		redis      fakePinger
		wantCode   int
		wantStatus string
		wantChecks map[string]string
	}{
		{
			name:       "returns 200 when every dependency is reachable",
			wantCode:   http.StatusOK,
			wantStatus: "ok",
			wantChecks: map[string]string{"postgres": "ok", "redis": "ok"},
		},
		{
			name:       "returns 503 when postgres is unreachable",
			postgres:   fakePinger{err: errors.New("dial tcp 10.0.0.1:5432: connection refused")},
			wantCode:   http.StatusServiceUnavailable,
			wantStatus: "unavailable",
			wantChecks: map[string]string{"postgres": "unavailable", "redis": "ok"},
		},
		{
			name:       "returns 503 when redis is unreachable",
			redis:      fakePinger{err: errors.New("dial tcp 10.0.0.2:6379: connection refused")},
			wantCode:   http.StatusServiceUnavailable,
			wantStatus: "unavailable",
			wantChecks: map[string]string{"postgres": "ok", "redis": "unavailable"},
		},
		{
			name:       "returns 503 when a dependency exceeds the readiness timeout",
			redis:      fakePinger{block: true},
			wantCode:   http.StatusServiceUnavailable,
			wantStatus: "unavailable",
			wantChecks: map[string]string{"postgres": "ok", "redis": "unavailable"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newRouter(
				httpapi.ReadinessCheck{Name: "postgres", Pinger: tc.postgres},
				httpapi.ReadinessCheck{Name: "redis", Pinger: tc.redis},
			)

			rec, body, raw := get(t, h, "/readyz")

			require.Equal(t, tc.wantCode, rec.Code)
			require.Equal(t, tc.wantStatus, body.Status)
			require.Equal(t, tc.wantChecks, body.Checks)
			require.NotContains(t, raw, "dial tcp", "internal error details must not reach the client")
		})
	}
}

func TestReadinessProbesDependenciesConcurrently(t *testing.T) {
	t.Parallel()

	t.Run("bounds latency by one timeout instead of one per dependency", func(t *testing.T) {
		t.Parallel()
		a, b := make(chan struct{}), make(chan struct{})
		h := httpapi.NewRouter(httpapi.Deps{
			Log: slog.New(slog.DiscardHandler),
			ReadinessChecks: []httpapi.ReadinessCheck{
				{Name: "postgres", Pinger: rendezvousPinger{mine: a, other: b}},
				{Name: "redis", Pinger: rendezvousPinger{mine: b, other: a}},
			},
			// Generous so a slow machine cannot fail the concurrent path; a
			// sequential implementation still fails deterministically.
			ReadinessTimeout: 5 * time.Second,
		})

		rec, body, _ := get(t, h, "/readyz")

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, map[string]string{"postgres": "ok", "redis": "ok"}, body.Checks)
	})
}

func TestTracingMiddleware(t *testing.T) {
	t.Parallel()

	var traced []string
	h := httpapi.NewRouter(httpapi.Deps{
		Log:            slog.New(slog.DiscardHandler),
		Queries:        &fakeDealFinder{deal: sampleDeal(t)},
		RequestTimeout: time.Second,
		Tracing: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				traced = append(traced, r.URL.Path)
				next.ServeHTTP(w, r)
			})
		},
	})

	for _, path := range []string{"/v1/deals/7", "/healthz"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	}

	require.Equal(t, []string{"/v1/deals/7"}, traced, "business routes are traced; probes are not")
}
