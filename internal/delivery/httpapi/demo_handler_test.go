package httpapi_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/delivery/httpapi"
	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/platform/auth"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

type stubDemo struct {
	session usecase.DemoSession
	err     error
}

func (s stubDemo) Execute(context.Context) (usecase.DemoSession, error) {
	return s.session, s.err
}

func TestDemoRoutes(t *testing.T) {
	t.Parallel()
	issuer, err := auth.New(auth.Config{
		Secret: "test-secret-at-least-32-bytes-long!!",
		Issuer: "flashdeal-test",
	})
	require.NoError(t, err)
	deal := mustDemoDeal(t)
	starter := stubDemo{session: usecase.DemoSession{Deal: deal, UserID: "demo-buyer"}}
	log := slog.New(slog.DiscardHandler)

	t.Run("serves the HTML page when demo mode is on", func(t *testing.T) {
		t.Parallel()
		h := httpapi.NewRouter(httpapi.Deps{
			Log: log, Tokens: issuer, DemoEnabled: true, Demo: starter, Issuer: issuer,
			RequestTimeout: time.Second, ReadinessTimeout: time.Second,
		})
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
		require.Contains(t, rec.Body.String(), "FlashDeal")
	})

	t.Run("starts a demo session", func(t *testing.T) {
		t.Parallel()
		h := httpapi.NewRouter(httpapi.Deps{
			Log: log, Tokens: issuer, DemoEnabled: true, Demo: starter, Issuer: issuer,
			RequestTimeout: time.Second, ReadinessTimeout: time.Second,
		})
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/demo/session", nil)
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusCreated, rec.Code)
		require.Contains(t, rec.Body.String(), `"deal_id":`)
		require.Contains(t, rec.Body.String(), `"access_token":`)
	})

	t.Run("hides demo routes when demo mode is off", func(t *testing.T) {
		t.Parallel()
		h := httpapi.NewRouter(httpapi.Deps{
			Log: log, Tokens: issuer, RequestTimeout: time.Second, ReadinessTimeout: time.Second,
		})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
		require.Equal(t, http.StatusNotFound, rec.Code)
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/demo/session", nil))
		require.Equal(t, http.StatusNotFound, rec.Code)
	})
}

func mustDemoDeal(t *testing.T) domain.Deal {
	t.Helper()
	price, err := domain.NewMoney(4999, "USD")
	require.NoError(t, err)
	now := time.Now().UTC()
	d, err := domain.RestoreDeal(domain.DealState{
		ID: 42, ProductID: "p", Title: "t", Price: price, TotalStock: 5, PerUserLimit: 1,
		StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	})
	require.NoError(t, err)
	return d
}
