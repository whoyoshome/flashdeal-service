package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/delivery/httpapi"
	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/platform/auth"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

const (
	testJWTSecret = "test-secret-at-least-32-bytes-long!!"
	testJWTIssuer = "flashdeal-test"
)

var t0 = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// Fakes of the use cases. Each records its last input and returns the
// configured result.

type fakeDealManager struct {
	created    usecase.CreateDealInput
	activated  int64
	deal       domain.Deal
	createErr  error
	activateEr error
}

func (f *fakeDealManager) Create(_ context.Context, in usecase.CreateDealInput) (domain.Deal, error) {
	f.created = in
	return f.deal, f.createErr
}

func (f *fakeDealManager) Activate(_ context.Context, id int64) (domain.Deal, error) {
	f.activated = id
	return f.deal, f.activateEr
}

type fakeDealFinder struct {
	gotID     int64
	gotCursor int64
	gotLimit  int
	deal      domain.Deal
	page      usecase.DealPage
	err       error
}

func (f *fakeDealFinder) Get(_ context.Context, id int64) (domain.Deal, error) {
	f.gotID = id
	return f.deal, f.err
}

func (f *fakeDealFinder) ListActive(_ context.Context, cursor int64, limit int) (usecase.DealPage, error) {
	f.gotCursor, f.gotLimit = cursor, limit
	return f.page, f.err
}

type fakeReserver struct {
	got usecase.ReserveInput
	res domain.Reservation
	err error
}

func (f *fakeReserver) Execute(_ context.Context, in usecase.ReserveInput) (domain.Reservation, error) {
	f.got = in
	return f.res, f.err
}

type fakeConfirmer struct {
	got   usecase.ConfirmInput
	order domain.Order
	err   error
}

func (f *fakeConfirmer) Execute(_ context.Context, in usecase.ConfirmInput) (domain.Order, error) {
	f.got = in
	return f.order, f.err
}

type fakeCanceller struct {
	got usecase.CancelInput
	err error
}

func (f *fakeCanceller) Execute(_ context.Context, in usecase.CancelInput) error {
	f.got = in
	return f.err
}

// fakeMetrics records what the HTTP layer reports.
type fakeMetrics struct {
	mu           sync.Mutex
	routes       []string
	reservations []string
}

func (m *fakeMetrics) ObserveHTTP(method, route string, status int, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routes = append(m.routes, fmt.Sprintf("%s %s %d", method, route, status))
}

func (m *fakeMetrics) ReservationAttempt(result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reservations = append(m.reservations, result)
}

type api struct {
	deals     *fakeDealManager
	queries   *fakeDealFinder
	reserver  *fakeReserver
	confirmer *fakeConfirmer
	canceller *fakeCanceller
	metrics   *fakeMetrics
	tokens    *auth.Service
	logs      *bytes.Buffer
	handler   http.Handler
}

func newAPI(t *testing.T) *api {
	t.Helper()
	tokens, err := auth.New(auth.Config{Secret: testJWTSecret, Issuer: testJWTIssuer})
	require.NoError(t, err)
	a := &api{
		deals:     &fakeDealManager{deal: sampleDeal(t)},
		queries:   &fakeDealFinder{deal: sampleDeal(t)},
		reserver:  &fakeReserver{res: sampleReservation(t)},
		confirmer: &fakeConfirmer{order: sampleOrder(t)},
		canceller: &fakeCanceller{},
		metrics:   &fakeMetrics{},
		tokens:    tokens,
		logs:      &bytes.Buffer{},
	}
	a.handler = httpapi.NewRouter(httpapi.Deps{
		Log:            slog.New(slog.NewJSONHandler(a.logs, nil)),
		Deals:          a.deals,
		Queries:        a.queries,
		Reserve:        a.reserver,
		Confirm:        a.confirmer,
		Cancel:         a.canceller,
		Tokens:         tokens,
		Metrics:        a.metrics,
		MetricsHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("# metrics\n")) }),
		RequestTimeout: 5 * time.Second,
	})
	return a
}

func (a *api) bearer(t *testing.T, subject string, role auth.Role) string {
	t.Helper()
	tok, err := a.tokens.Issue(subject, role, time.Hour)
	require.NoError(t, err)
	return "Bearer " + tok
}

func (a *api) withAuth(t *testing.T, c call, subject string, role auth.Role) call {
	t.Helper()
	if c.headers == nil {
		c.headers = map[string]string{}
	}
	c.headers["Authorization"] = a.bearer(t, subject, role)
	return c
}

type call struct {
	method  string
	path    string
	body    string
	headers map[string]string
}

func (a *api) do(t *testing.T, c call) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), c.method, c.path, strings.NewReader(c.body))
	req.Header.Set("X-Request-ID", "req-test")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var v T
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &v))
	return v
}

type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func requireError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) errorBody {
	t.Helper()
	require.Equal(t, status, rec.Code, rec.Body.String())
	body := decode[errorBody](t, rec)
	require.Equal(t, code, body.Code)
	require.Equal(t, "req-test", body.RequestID)
	return body
}

func sampleDeal(t *testing.T) domain.Deal {
	t.Helper()
	price, err := domain.NewMoney(4999, "USD")
	require.NoError(t, err)
	d, err := domain.RestoreDeal(domain.DealState{
		ID: 7, ProductID: "sku-1", Title: "Headphones", Price: price,
		TotalStock: 100, Reserved: 10, Sold: 5, PerUserLimit: 2,
		StartsAt: t0, EndsAt: t0.Add(time.Hour), CreatedAt: t0, UpdatedAt: t0,
	})
	require.NoError(t, err)
	return d
}

func sampleReservation(t *testing.T) domain.Reservation {
	t.Helper()
	r, err := domain.RestoreReservation(domain.ReservationState{
		ID: "0192f0c4-6a3b-7c1d-9e2f-3a4b5c6d7e8f", DealID: 7, UserID: "user-1", Quantity: 2,
		Status: domain.StatusPending, IdempotencyKey: "key-1",
		ExpiresAt: t0.Add(5 * time.Minute), CreatedAt: t0, UpdatedAt: t0,
	})
	require.NoError(t, err)
	return r
}

func sampleOrder(t *testing.T) domain.Order {
	t.Helper()
	total, err := domain.NewMoney(9998, "USD")
	require.NoError(t, err)
	o, err := domain.RestoreOrder(domain.OrderState{
		ID: "0192f0c4-0000-7000-8000-000000000001", ReservationID: "0192f0c4-6a3b-7c1d-9e2f-3a4b5c6d7e8f",
		DealID: 7, UserID: "user-1", Quantity: 2, Total: total, CreatedAt: t0,
	})
	require.NoError(t, err)
	return o
}

const validDealBody = `{"product_id":"sku-1","title":"Headphones","price_cents":4999,"currency":"USD",
"total_stock":100,"per_user_limit":2,"starts_at":"2026-10-15T12:00:00Z","ends_at":"2026-10-15T13:00:00Z"}`

func TestCreateDeal(t *testing.T) {
	t.Parallel()

	t.Run("returns 201 with the stored deal and maps the body to the use case", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)

		rec := a.do(t, a.withAuth(t, call{method: http.MethodPost, path: "/v1/deals", body: validDealBody}, "admin", auth.RoleAdmin))

		require.Equal(t, http.StatusCreated, rec.Code)
		got := decode[map[string]any](t, rec)
		require.InDelta(t, 7, got["id"], 0)
		require.InDelta(t, 85, got["available_stock"], 0)
		require.Equal(t, "USD", got["currency"])
		require.Equal(t, "2026-10-15T12:00:00Z", got["starts_at"])
		require.Equal(t, usecase.CreateDealInput{
			ProductID: "sku-1", Title: "Headphones", PriceCents: 4999, Currency: "USD",
			TotalStock: 100, PerUserLimit: 2, StartsAt: t0, EndsAt: t0.Add(time.Hour),
		}, a.deals.created)
	})

	t.Run("returns 401 without a Bearer token", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)
		requireError(t, a.do(t, call{method: http.MethodPost, path: "/v1/deals", body: validDealBody}),
			http.StatusUnauthorized, "UNAUTHENTICATED")
	})

	t.Run("returns 403 for a non-admin token", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)
		requireError(t, a.do(t, a.withAuth(t, call{method: http.MethodPost, path: "/v1/deals", body: validDealBody}, "alice", auth.RoleUser)),
			http.StatusForbidden, "FORBIDDEN")
	})

	badBodies := []struct {
		name string
		body string
	}{
		{name: "rejects malformed JSON", body: `{"title":`},
		{name: "rejects unknown fields", body: `{"titel":"typo"}`},
		{name: "rejects more than one JSON object", body: validDealBody + `{}`},
		{name: "rejects a missing window", body: `{"product_id":"sku-1","title":"x","price_cents":1,"currency":"USD","total_stock":1,"per_user_limit":1}`},
		{name: "rejects a body over the size limit", body: `{"title":"` + strings.Repeat("a", 70_000) + `"}`},
	}
	for _, tc := range badBodies {
		t.Run(tc.name+" with 400", func(t *testing.T) {
			t.Parallel()
			a := newAPI(t)

			rec := a.do(t, a.withAuth(t, call{method: http.MethodPost, path: "/v1/deals", body: tc.body}, "admin", auth.RoleAdmin))

			requireError(t, rec, http.StatusBadRequest, "VALIDATION_ERROR")
		})
	}

	t.Run("returns the use case validation error with 400", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)
		a.deals.createErr = domain.NewValidation("total_stock must be positive")

		rec := a.do(t, a.withAuth(t, call{method: http.MethodPost, path: "/v1/deals", body: validDealBody}, "admin", auth.RoleAdmin))

		body := requireError(t, rec, http.StatusBadRequest, "VALIDATION_ERROR")
		require.Equal(t, "total_stock must be positive", body.Message)
	})
}

func TestActivateDeal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		err      error
		status   int
		wantCode string
	}{
		{name: "returns 200 with the deal", path: "/v1/deals/7/activate", status: http.StatusOK},
		{name: "returns 404 for an unknown deal", path: "/v1/deals/7/activate", err: domain.ErrDealNotFound, status: http.StatusNotFound, wantCode: "NOT_FOUND"},
		{name: "returns 409 for a deal that already ended", path: "/v1/deals/7/activate", err: domain.ErrDealNotActive, status: http.StatusConflict, wantCode: "DEAL_NOT_ACTIVE"},
		{name: "returns 400 for a non-numeric id", path: "/v1/deals/abc/activate", status: http.StatusBadRequest, wantCode: "VALIDATION_ERROR"},
		{name: "returns 400 for a zero id", path: "/v1/deals/0/activate", status: http.StatusBadRequest, wantCode: "VALIDATION_ERROR"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newAPI(t)
			a.deals.activateEr = tc.err

			rec := a.do(t, a.withAuth(t, call{method: http.MethodPost, path: tc.path}, "admin", auth.RoleAdmin))

			if tc.wantCode != "" {
				requireError(t, rec, tc.status, tc.wantCode)
				return
			}
			require.Equal(t, tc.status, rec.Code)
			require.Equal(t, int64(7), a.deals.activated)
		})
	}
}

func TestGetDeal(t *testing.T) {
	t.Parallel()

	t.Run("returns 200 with the deal", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)

		rec := a.do(t, call{method: http.MethodGet, path: "/v1/deals/7"})

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, int64(7), a.queries.gotID)
		require.InDelta(t, 7, decode[map[string]any](t, rec)["id"], 0)
	})

	t.Run("returns 404 for an unknown deal", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)
		a.queries.err = domain.ErrDealNotFound

		requireError(t, a.do(t, call{method: http.MethodGet, path: "/v1/deals/404"}), http.StatusNotFound, "NOT_FOUND")
	})
}

func TestListDeals(t *testing.T) {
	t.Parallel()

	t.Run("passes cursor and limit and returns the page with its next cursor", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)
		a.queries.page = usecase.DealPage{Deals: []domain.Deal{sampleDeal(t)}, NextCursor: 7}

		rec := a.do(t, call{method: http.MethodGet, path: "/v1/deals?status=active&cursor=3&limit=1"})

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, int64(3), a.queries.gotCursor)
		require.Equal(t, 1, a.queries.gotLimit)
		got := decode[map[string]any](t, rec)
		require.Len(t, got["data"], 1)
		require.InDelta(t, 7, got["next_cursor"], 0)
	})

	t.Run("returns an empty array and no cursor on the last page", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)

		rec := a.do(t, call{method: http.MethodGet, path: "/v1/deals"})

		require.Equal(t, http.StatusOK, rec.Code)
		require.JSONEq(t, `{"data":[]}`, rec.Body.String())
	})

	for _, query := range []string{"status=ended", "cursor=-1", "cursor=abc", "limit=ten"} {
		t.Run("rejects "+query+" with 400", func(t *testing.T) {
			t.Parallel()
			a := newAPI(t)

			requireError(t, a.do(t, call{method: http.MethodGet, path: "/v1/deals?" + query}), http.StatusBadRequest, "VALIDATION_ERROR")
		})
	}
}

func reserveCall(body string) call {
	return call{
		method: http.MethodPost, path: "/v1/deals/7/reservations", body: body,
		headers: map[string]string{"Idempotency-Key": "key-1"},
	}
}

func TestReserve(t *testing.T) {
	t.Parallel()

	t.Run("returns 201 with the reservation and passes user and key to the use case", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)

		rec := a.do(t, a.withAuth(t, reserveCall(`{"quantity":2}`), "user-1", auth.RoleUser))

		require.Equal(t, http.StatusCreated, rec.Code)
		require.Equal(t, usecase.ReserveInput{DealID: 7, UserID: "user-1", Quantity: 2, IdempotencyKey: "key-1"}, a.reserver.got)
		got := decode[map[string]any](t, rec)
		require.Equal(t, "pending", got["status"])
		require.Equal(t, "2026-10-15T12:05:00Z", got["expires_at"])
	})

	t.Run("returns 401 without a Bearer token before reaching the use case", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)

		requireError(t, a.do(t, reserveCall(`{"quantity":2}`)), http.StatusUnauthorized, "UNAUTHENTICATED")
		require.Zero(t, a.reserver.got)
	})

	t.Run("returns 400 for an unknown body field before reaching the use case", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)

		requireError(t, a.do(t, a.withAuth(t, reserveCall(`{"qty":2}`), "user-1", auth.RoleUser)), http.StatusBadRequest, "VALIDATION_ERROR")
		require.Zero(t, a.reserver.got)
	})

	t.Run("returns 400 without an Idempotency-Key", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)
		c := a.withAuth(t, reserveCall(`{"quantity":2}`), "user-1", auth.RoleUser)
		delete(c.headers, "Idempotency-Key")

		requireError(t, a.do(t, c), http.StatusBadRequest, "VALIDATION_ERROR")
		require.Zero(t, a.reserver.got)
	})

	errorsToStatus := []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrSoldOut, http.StatusConflict, "SOLD_OUT"},
		{domain.ErrUserLimitReached, http.StatusConflict, "USER_LIMIT_REACHED"},
		{domain.ErrDealNotActive, http.StatusConflict, "DEAL_NOT_ACTIVE"},
		{domain.ErrIdempotencyKeyReused, http.StatusConflict, "CONFLICT"},
		{domain.ErrDealNotFound, http.StatusNotFound, "NOT_FOUND"},
		{domain.ErrStockUnavailable, http.StatusServiceUnavailable, "STOCK_UNAVAILABLE"},
	}
	for _, tc := range errorsToStatus {
		t.Run("maps "+tc.code+" to "+http.StatusText(tc.status), func(t *testing.T) {
			t.Parallel()
			a := newAPI(t)
			a.reserver.err = fmt.Errorf("stock gate: %w", tc.err)

			requireError(t, a.do(t, a.withAuth(t, reserveCall(`{"quantity":2}`), "user-1", auth.RoleUser)), tc.status, tc.code)
		})
	}
}

func TestConfirm(t *testing.T) {
	t.Parallel()

	confirmCall := call{
		method: http.MethodPost, path: "/v1/reservations/0192f0c4-6a3b-7c1d-9e2f-3a4b5c6d7e8f/confirm",
	}

	t.Run("returns 200 with the order", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)

		rec := a.do(t, a.withAuth(t, confirmCall, "user-1", auth.RoleUser))

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, usecase.ConfirmInput{ReservationID: "0192f0c4-6a3b-7c1d-9e2f-3a4b5c6d7e8f", UserID: "user-1"}, a.confirmer.got)
		got := decode[map[string]any](t, rec)
		require.InDelta(t, 9998, got["total_cents"], 0)
		require.Equal(t, "0192f0c4-6a3b-7c1d-9e2f-3a4b5c6d7e8f", got["reservation_id"])
	})

	failures := []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrReservationNotFound, http.StatusNotFound, "NOT_FOUND"},
		{domain.ErrReservationNotPending, http.StatusConflict, "CONFLICT"},
		{domain.ErrReservationExpired, http.StatusGone, "RESERVATION_EXPIRED"},
	}
	for _, tc := range failures {
		t.Run("maps "+tc.code+" to "+http.StatusText(tc.status), func(t *testing.T) {
			t.Parallel()
			a := newAPI(t)
			a.confirmer.err = tc.err

			requireError(t, a.do(t, a.withAuth(t, confirmCall, "user-1", auth.RoleUser)), tc.status, tc.code)
		})
	}
}

func TestCancel(t *testing.T) {
	t.Parallel()

	cancelCall := call{
		method: http.MethodDelete, path: "/v1/reservations/0192f0c4-6a3b-7c1d-9e2f-3a4b5c6d7e8f",
	}

	t.Run("returns 204 with no body", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)

		rec := a.do(t, a.withAuth(t, cancelCall, "user-1", auth.RoleUser))

		require.Equal(t, http.StatusNoContent, rec.Code)
		require.Empty(t, rec.Body.String())
		require.Equal(t, usecase.CancelInput{ReservationID: "0192f0c4-6a3b-7c1d-9e2f-3a4b5c6d7e8f", UserID: "user-1"}, a.canceller.got)
	})

	t.Run("returns 409 for a reservation that is no longer pending", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)
		a.canceller.err = domain.ErrReservationNotPending

		requireError(t, a.do(t, a.withAuth(t, cancelCall, "user-1", auth.RoleUser)), http.StatusConflict, "CONFLICT")
	})
}

func TestErrorHandling(t *testing.T) {
	t.Parallel()

	t.Run("hides unexpected errors behind a generic 500 and logs them once", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)
		a.queries.err = errors.New("pq: connection to 10.0.0.5 refused")

		rec := a.do(t, call{method: http.MethodGet, path: "/v1/deals/7"})

		body := requireError(t, rec, http.StatusInternalServerError, "INTERNAL")
		require.Equal(t, "internal error", body.Message)
		require.NotContains(t, rec.Body.String(), "10.0.0.5")
		require.Contains(t, a.logs.String(), "unhandled error")
		require.Contains(t, a.logs.String(), "10.0.0.5")
	})

	t.Run("maps a request deadline to a retryable 503 REQUEST_TIMEOUT", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)
		a.reserver.err = fmt.Errorf("persist reservation: reserve stock on deal 7: timeout: %w", context.DeadlineExceeded)

		rec := a.do(t, a.withAuth(t, reserveCall(`{"quantity":1}`), "user-1", auth.RoleUser))

		body := requireError(t, rec, http.StatusServiceUnavailable, "REQUEST_TIMEOUT")
		require.Equal(t, "request timed out, retry later", body.Message)
		require.Equal(t, "1", rec.Header().Get("Retry-After"))
		require.NotContains(t, rec.Body.String(), "deal 7", "internal details stay in the log")
		require.Contains(t, a.logs.String(), "request timed out")
	})

	t.Run("prefers the domain code when a domain error wraps a deadline", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)
		a.reserver.err = fmt.Errorf("%w: %w", domain.ErrStockUnavailable, context.DeadlineExceeded)

		requireError(t, a.do(t, a.withAuth(t, reserveCall(`{"quantity":1}`), "user-1", auth.RoleUser)), http.StatusServiceUnavailable, "STOCK_UNAVAILABLE")
	})

	t.Run("hides corrupt state errors behind a generic 500", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)
		a.confirmer.err = fmt.Errorf("commit stock: %w", domain.ErrCorruptState)

		rec := a.do(t, a.withAuth(t, call{method: http.MethodPost, path: "/v1/reservations/x/confirm"}, "u", auth.RoleUser))

		requireError(t, rec, http.StatusInternalServerError, "INTERNAL")
	})

	t.Run("returns a JSON 404 for an unknown route", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)

		requireError(t, a.do(t, call{method: http.MethodGet, path: "/v1/nope"}), http.StatusNotFound, "NOT_FOUND")
	})

	t.Run("returns a JSON 405 for a wrong method", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)

		requireError(t, a.do(t, call{method: http.MethodPut, path: "/v1/deals/7"}), http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
	})

	t.Run("echoes the request id in the response header", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)

		rec := a.do(t, call{method: http.MethodGet, path: "/v1/deals/7"})

		require.Equal(t, "req-test", rec.Header().Get("X-Request-ID"))
	})
}

func TestMetricsReporting(t *testing.T) {
	t.Parallel()

	results := []struct {
		name string
		err  error
		want string
	}{
		{name: "a created reservation", want: "created"},
		{name: "a sold-out rejection", err: domain.ErrSoldOut, want: "sold_out"},
		{name: "a per-user limit rejection", err: domain.ErrUserLimitReached, want: "user_limit_reached"},
		{name: "a closed gate", err: domain.ErrStockUnavailable, want: "stock_unavailable"},
		{name: "a request timeout", err: fmt.Errorf("persist: %w", context.DeadlineExceeded), want: "request_timeout"},
		{name: "an unexpected failure", err: errors.New("boom"), want: "internal"},
	}
	for _, tc := range results {
		t.Run("counts "+tc.name+" as "+tc.want, func(t *testing.T) {
			t.Parallel()
			a := newAPI(t)
			a.reserver.err = tc.err

			a.do(t, a.withAuth(t, reserveCall(`{"quantity":2}`), "user-1", auth.RoleUser))

			require.Equal(t, []string{tc.want}, a.metrics.reservations)
		})
	}

	t.Run("counts a request rejected before the use case by its error code", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)

		a.do(t, a.withAuth(t, reserveCall(`{"qty":2}`), "user-1", auth.RoleUser))

		require.Equal(t, []string{"validation_error"}, a.metrics.reservations)
	})

	t.Run("observes /v1 requests by route pattern", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)
		a.reserver.err = domain.ErrSoldOut

		a.do(t, a.withAuth(t, reserveCall(`{"quantity":2}`), "user-1", auth.RoleUser))

		require.Equal(t, []string{"POST /v1/deals/{id}/reservations 409"}, a.metrics.routes)
	})

	t.Run("serves /metrics outside /v1, without observing the scrape", func(t *testing.T) {
		t.Parallel()
		a := newAPI(t)

		rec := a.do(t, call{method: http.MethodGet, path: "/metrics"})

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "# metrics\n", rec.Body.String())
		require.Empty(t, a.metrics.routes)
	})
}
