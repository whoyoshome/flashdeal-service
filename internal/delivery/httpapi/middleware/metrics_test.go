package middleware_test

import (
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/delivery/httpapi/middleware"
)

type observation struct {
	method, route string
	status        int
	duration      time.Duration
}

type recordingObserver struct {
	mu   sync.Mutex
	seen []observation
}

func (o *recordingObserver) ObserveHTTP(method, route string, status int, d time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen = append(o.seen, observation{method: method, route: route, status: status, duration: d})
}

func TestMetrics(t *testing.T) {
	t.Parallel()

	t.Run("observes the route pattern, the status sent and the latency", func(t *testing.T) {
		t.Parallel()
		// Virtual time makes the measured latency exact instead of depending
		// on the platform's timer resolution.
		synctest.Test(t, func(t *testing.T) {
			obs := &recordingObserver{}
			r := chi.NewRouter()
			r.Use(middleware.Metrics(obs))
			r.Get("/v1/deals/{id}", func(w http.ResponseWriter, _ *http.Request) {
				time.Sleep(25 * time.Millisecond)
				w.WriteHeader(http.StatusNotFound)
			})

			serve(t, r, newRequest(t, nil))

			require.Len(t, obs.seen, 1)
			require.Equal(t, http.MethodGet, obs.seen[0].method)
			require.Equal(t, "/v1/deals/{id}", obs.seen[0].route, "the raw path would explode label cardinality")
			require.Equal(t, http.StatusNotFound, obs.seen[0].status)
			require.Equal(t, 25*time.Millisecond, obs.seen[0].duration)
		})
	})

	t.Run("records 200 when the handler writes without a status", func(t *testing.T) {
		t.Parallel()
		obs := &recordingObserver{}
		h := middleware.Metrics(obs)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}))

		serve(t, h, newRequest(t, nil))

		require.Equal(t, http.StatusOK, obs.seen[0].status)
		require.Equal(t, "unmatched", obs.seen[0].route)
	})
}
