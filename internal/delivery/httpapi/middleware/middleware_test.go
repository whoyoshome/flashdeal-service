package middleware_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/delivery/httpapi/middleware"
)

type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func serve(t *testing.T, h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func newRequest(t *testing.T, headers map[string]string) *http.Request {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/deals/7", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

func TestRequestID(t *testing.T) {
	t.Parallel()

	var seen string
	h := middleware.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = middleware.RequestIDFrom(r.Context())
	}))

	tests := []struct {
		name     string
		incoming string
		keep     bool
	}{
		{name: "reuses a well-formed incoming id", incoming: "lb-01J9Z8.abc_123", keep: true},
		{name: "generates an id when none is sent", incoming: ""},
		{name: "replaces an id with unsafe characters", incoming: "evil\nlog-line"},
		{name: "replaces an id longer than the limit", incoming: strings.Repeat("a", 129)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(t, h, newRequest(t, map[string]string{middleware.RequestIDHeader: tc.incoming}))

			got := rec.Header().Get(middleware.RequestIDHeader)
			require.Equal(t, got, seen, "the context and the response header carry the same id")
			if tc.keep {
				require.Equal(t, tc.incoming, got)
				return
			}
			require.Len(t, got, 32)
			require.NotEqual(t, tc.incoming, got)
		})
	}

	t.Run("returns an empty id outside the middleware", func(t *testing.T) {
		require.Empty(t, middleware.RequestIDFrom(t.Context()))
	})
}

func TestRecoverer(t *testing.T) {
	t.Parallel()

	t.Run("turns a panic into a 500 with the standard body and logs the stack", func(t *testing.T) {
		t.Parallel()
		var logs bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&logs, nil))
		h := middleware.RequestID(middleware.Recoverer(log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("nil map write")
		})))

		rec := serve(t, h, newRequest(t, map[string]string{middleware.RequestIDHeader: "req-1"}))

		require.Equal(t, http.StatusInternalServerError, rec.Code)
		var body errorBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, errorBody{Code: "INTERNAL", Message: "internal error", RequestID: "req-1"}, body)
		require.NotContains(t, rec.Body.String(), "nil map write", "panic details must not reach the client")
		require.Contains(t, logs.String(), "panic recovered")
		require.Contains(t, logs.String(), "stack")
	})

	t.Run("re-raises http.ErrAbortHandler", func(t *testing.T) {
		t.Parallel()
		h := middleware.Recoverer(slog.New(slog.DiscardHandler))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic(http.ErrAbortHandler)
		}))

		require.PanicsWithError(t, http.ErrAbortHandler.Error(), func() {
			serve(t, h, newRequest(t, nil))
		})
	})
}

func TestLogging(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    int
		wantLevel string
	}{
		{name: "logs a successful request at info with its route pattern", status: http.StatusCreated, wantLevel: "INFO"},
		{name: "logs a client error at info", status: http.StatusConflict, wantLevel: "INFO"},
		{name: "logs a server error at error level", status: http.StatusServiceUnavailable, wantLevel: "ERROR"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			r := chi.NewRouter()
			r.Use(middleware.RequestID, middleware.Logging(slog.New(slog.NewJSONHandler(&logs, nil))))
			r.Get("/v1/deals/{id}", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("hello"))
			})

			serve(t, r, newRequest(t, map[string]string{middleware.RequestIDHeader: "req-2"}))

			var line map[string]any
			require.NoError(t, json.Unmarshal(logs.Bytes(), &line))
			require.Equal(t, tc.wantLevel, line["level"])
			require.Equal(t, "/v1/deals/{id}", line["route"], "logs the pattern, not the raw path")
			require.InDelta(t, tc.status, line["status"], 0)
			require.InDelta(t, 5, line["bytes"], 0)
			require.Equal(t, "req-2", line["request_id"])
		})
	}

	t.Run("records 200 when the handler writes without a status", func(t *testing.T) {
		t.Parallel()
		var logs bytes.Buffer
		h := middleware.Logging(slog.New(slog.NewJSONHandler(&logs, nil)))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}))

		serve(t, h, newRequest(t, nil))

		require.Contains(t, logs.String(), `"status":200`)
		require.Contains(t, logs.String(), `"route":"unmatched"`)
	})
}

func TestTimeout(t *testing.T) {
	t.Parallel()

	t.Run("gives the request a context deadline", func(t *testing.T) {
		t.Parallel()
		var deadline time.Time
		var ok bool
		h := middleware.Timeout(3 * time.Second)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			deadline, ok = r.Context().Deadline()
		}))

		before := time.Now()
		serve(t, h, newRequest(t, nil))

		require.True(t, ok)
		require.WithinDuration(t, before.Add(3*time.Second), deadline, time.Second)
	})
}

func TestAuth(t *testing.T) {
	t.Parallel()

	var seen string
	h := middleware.RequestID(middleware.Auth(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = middleware.UserIDFrom(r.Context())
	})))

	t.Run("passes the user id to the handler", func(t *testing.T) {
		rec := serve(t, h, newRequest(t, map[string]string{middleware.UserIDHeader: "user-42"}))

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "user-42", seen)
	})

	rejected := []struct {
		name string
		id   string
	}{
		{name: "rejects a missing header", id: ""},
		{name: "rejects an id with spaces", id: "user 42"},
		{name: "rejects an id longer than the limit", id: strings.Repeat("u", 129)},
	}
	for _, tc := range rejected {
		t.Run(tc.name+" with 401", func(t *testing.T) {
			rec := serve(t, h, newRequest(t, map[string]string{middleware.UserIDHeader: tc.id}))

			require.Equal(t, http.StatusUnauthorized, rec.Code)
			var body errorBody
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Equal(t, "UNAUTHENTICATED", body.Code)
			require.NotEmpty(t, body.RequestID)
		})
	}

	t.Run("returns an empty user outside the middleware", func(t *testing.T) {
		require.Empty(t, middleware.UserIDFrom(context.Background()))
	})
}
