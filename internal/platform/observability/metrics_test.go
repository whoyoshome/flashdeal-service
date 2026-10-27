package observability_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/platform/observability"
)

// scrape returns the exposition text a Prometheus server would read.
func scrape(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body, err := io.ReadAll(rec.Body)
	require.NoError(t, err)
	return string(body)
}

type fakePool struct{}

func (fakePool) AcquiredConns() int32                { return 18 }
func (fakePool) IdleConns() int32                    { return 2 }
func (fakePool) MaxConns() int32                     { return 20 }
func (fakePool) AcquireCount() int64                 { return 1000 }
func (fakePool) EmptyAcquireCount() int64            { return 250 }
func (fakePool) EmptyAcquireWaitTime() time.Duration { return 1500 * time.Millisecond }
func (fakePool) CanceledAcquireCount() int64         { return 3 }

func TestMetrics(t *testing.T) {
	t.Parallel()

	t.Run("exports HTTP requests by route pattern and their latency", func(t *testing.T) {
		t.Parallel()
		m := observability.NewMetrics()

		m.ObserveHTTP(http.MethodPost, "/v1/deals/{id}/reservations", http.StatusConflict, 3*time.Millisecond)
		m.ObserveHTTP(http.MethodPost, "/v1/deals/{id}/reservations", http.StatusConflict, 4*time.Millisecond)

		out := scrape(t, m.Handler())
		require.Contains(t, out, `http_requests_total{method="POST",route="/v1/deals/{id}/reservations",status="409"} 2`)
		require.Contains(t, out, `http_request_duration_seconds_bucket{method="POST",route="/v1/deals/{id}/reservations",le="0.005"} 2`)
		require.Contains(t, out, `http_request_duration_seconds_count{method="POST",route="/v1/deals/{id}/reservations"} 2`)
	})

	t.Run("exports the business counters", func(t *testing.T) {
		t.Parallel()
		m := observability.NewMetrics()

		m.ReservationAttempt("created")
		m.ReservationAttempt("sold_out")
		m.ReservationAttempt("sold_out")
		m.ReservationsExpired(4)
		m.StockGateRepaired(1)

		out := scrape(t, m.Handler())
		require.Contains(t, out, `reservations_total{result="created"} 1`)
		require.Contains(t, out, `reservations_total{result="sold_out"} 2`)
		require.Contains(t, out, "reservations_expired_total 4")
		require.Contains(t, out, "stock_gate_repairs_total 1")
	})

	t.Run("exports every known reservation outcome at zero before the first attempt", func(t *testing.T) {
		t.Parallel()

		out := scrape(t, observability.NewMetrics().Handler())

		require.Contains(t, out, `reservations_total{result="created"} 0`)
		require.Contains(t, out, `reservations_total{result="sold_out"} 0`)
		require.Contains(t, out, `reservations_total{result="request_timeout"} 0`)
	})

	t.Run("replaces stock levels so deals that closed disappear", func(t *testing.T) {
		t.Parallel()
		m := observability.NewMetrics()

		m.SetStockLevels(map[int64]int{7: 12, 8: 0})
		m.SetStockLevels(map[int64]int{7: 3})

		out := scrape(t, m.Handler())
		require.Contains(t, out, `stock_remaining{deal_id="7"} 3`)
		require.NotContains(t, out, `deal_id="8"`)
	})

	t.Run("reads the connection pool's statistics at scrape time", func(t *testing.T) {
		t.Parallel()
		m := observability.NewMetrics()

		m.WatchPool(func() observability.PoolStats { return fakePool{} })

		out := scrape(t, m.Handler())
		require.Contains(t, out, "db_pool_acquired_conns 18")
		require.Contains(t, out, "db_pool_max_conns 20")
		require.Contains(t, out, "db_pool_empty_acquires_total 250")
		require.Contains(t, out, "db_pool_empty_acquire_wait_seconds_total 1.5")
		require.Contains(t, out, "db_pool_canceled_acquires_total 3")
	})

	t.Run("includes the Go runtime metrics", func(t *testing.T) {
		t.Parallel()

		require.Contains(t, scrape(t, observability.NewMetrics().Handler()), "go_goroutines ")
	})
}

func TestAdminHandler(t *testing.T) {
	t.Parallel()
	h := observability.NewAdminHandler()

	for _, path := range []string{"/debug/pprof/", "/debug/pprof/goroutine?debug=1", "/debug/pprof/cmdline"} {
		t.Run("serves "+path, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
			require.Equal(t, http.StatusOK, rec.Code)
		})
	}

	t.Run("serves nothing outside /debug/pprof", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
		require.Equal(t, http.StatusNotFound, rec.Code)
	})
}
