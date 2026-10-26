package middleware_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/delivery/httpapi/middleware"
	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

// fakeIdemStore is an in-memory IdempotencyStore that mimics the Redis
// semantics: one owner per key, final responses replace the claim.
type fakeIdemStore struct {
	mu        sync.Mutex
	records   map[usecase.IdempotencyRef]usecase.IdempotencyClaim
	tokens    map[usecase.IdempotencyRef]string
	err       error
	completed int
	released  int
}

func newFakeIdemStore() *fakeIdemStore {
	return &fakeIdemStore{
		records: map[usecase.IdempotencyRef]usecase.IdempotencyClaim{},
		tokens:  map[usecase.IdempotencyRef]string{},
	}
}

func (f *fakeIdemStore) Begin(_ context.Context, ref usecase.IdempotencyRef, fp, token string, _ time.Duration) (usecase.IdempotencyClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return usecase.IdempotencyClaim{}, f.err
	}
	if rec, ok := f.records[ref]; ok {
		return rec, nil
	}
	f.records[ref] = usecase.IdempotencyClaim{State: usecase.IdempotencyInProgress, Fingerprint: fp}
	f.tokens[ref] = token
	return usecase.IdempotencyClaim{State: usecase.IdempotencyNew, Fingerprint: fp}, nil
}

func (f *fakeIdemStore) Complete(_ context.Context, ref usecase.IdempotencyRef, fp string, status int, body []byte, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed++
	f.records[ref] = usecase.IdempotencyClaim{State: usecase.IdempotencyDone, Fingerprint: fp, Status: status, Body: body}
	return nil
}

func (f *fakeIdemStore) Release(_ context.Context, ref usecase.IdempotencyRef, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released++
	if f.tokens[ref] == token && f.records[ref].State == usecase.IdempotencyInProgress {
		delete(f.records, ref)
	}
	return nil
}

// idemHandler counts calls and answers with the configured status.
type idemHandler struct {
	mu     sync.Mutex
	calls  int
	status int
	block  chan struct{} // when set, the handler waits on it before answering
	panics bool
}

func (h *idemHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.calls++
	n := h.calls
	h.mu.Unlock()
	if h.block != nil {
		<-h.block
	}
	if h.panics {
		panic("boom")
	}
	body, _ := json.Marshal(map[string]any{"call": n, "path": r.URL.Path})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(h.status)
	_, _ = w.Write(body)
}

func newIdemChain(store usecase.IdempotencyStore, next http.Handler) http.Handler {
	cfg := middleware.IdempotencyConfig{LockTTL: 10 * time.Second, ResponseTTL: 24 * time.Hour}
	log := slog.New(slog.DiscardHandler)
	return middleware.RequestID(middleware.Recoverer(log)(middleware.Auth(middleware.Idempotency(store, cfg, log)(next))))
}

func idemRequest(t *testing.T, user, key, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/deals/7/reservations", strings.NewReader(body))
	req.Header.Set(middleware.UserIDHeader, user)
	if key != "" {
		req.Header.Set(middleware.IdempotencyKeyHeader, key)
	}
	return req
}

func TestIdempotency(t *testing.T) {
	t.Parallel()

	t.Run("runs the first request and replays the stored response on a retry", func(t *testing.T) {
		t.Parallel()
		store, h := newFakeIdemStore(), &idemHandler{status: http.StatusCreated}
		chain := newIdemChain(store, h)

		first := serve(t, chain, idemRequest(t, "alice", "k1", `{"quantity":1}`))
		retry := serve(t, chain, idemRequest(t, "alice", "k1", `{"quantity":1}`))

		require.Equal(t, http.StatusCreated, first.Code)
		require.Equal(t, http.StatusCreated, retry.Code)
		require.Equal(t, first.Body.String(), retry.Body.String())
		require.Equal(t, "true", retry.Header().Get(middleware.IdempotencyReplayedHeader))
		require.Empty(t, first.Header().Get(middleware.IdempotencyReplayedHeader))
		require.Equal(t, 1, h.calls, "the retry must not run the handler")
	})

	t.Run("replays a final 4xx such as SOLD_OUT", func(t *testing.T) {
		t.Parallel()
		store, h := newFakeIdemStore(), &idemHandler{status: http.StatusConflict}
		chain := newIdemChain(store, h)

		serve(t, chain, idemRequest(t, "alice", "k1", `{"quantity":1}`))
		retry := serve(t, chain, idemRequest(t, "alice", "k1", `{"quantity":1}`))

		require.Equal(t, http.StatusConflict, retry.Code)
		require.Equal(t, 1, h.calls)
	})

	t.Run("does not store a 5xx, so the retry runs again", func(t *testing.T) {
		t.Parallel()
		store, h := newFakeIdemStore(), &idemHandler{status: http.StatusServiceUnavailable}
		chain := newIdemChain(store, h)

		serve(t, chain, idemRequest(t, "alice", "k1", `{"quantity":1}`))
		serve(t, chain, idemRequest(t, "alice", "k1", `{"quantity":1}`))

		require.Equal(t, 2, h.calls)
		require.Zero(t, store.completed)
		require.Equal(t, 2, store.released)
	})

	t.Run("rejects the same key with a different body with 409 CONFLICT", func(t *testing.T) {
		t.Parallel()
		store, h := newFakeIdemStore(), &idemHandler{status: http.StatusCreated}
		chain := newIdemChain(store, h)
		serve(t, chain, idemRequest(t, "alice", "k1", `{"quantity":1}`))

		rec := serve(t, chain, idemRequest(t, "alice", "k1", `{"quantity":2}`))

		require.Equal(t, http.StatusConflict, rec.Code)
		var body errorBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, "CONFLICT", body.Code)
		require.Equal(t, 1, h.calls)
	})

	t.Run("scopes keys per user", func(t *testing.T) {
		t.Parallel()
		store, h := newFakeIdemStore(), &idemHandler{status: http.StatusCreated}
		chain := newIdemChain(store, h)

		serve(t, chain, idemRequest(t, "alice", "shared-key", `{"quantity":1}`))
		bob := serve(t, chain, idemRequest(t, "bob", "shared-key", `{"quantity":1}`))

		require.Equal(t, http.StatusCreated, bob.Code)
		require.Empty(t, bob.Header().Get(middleware.IdempotencyReplayedHeader))
		require.Equal(t, 2, h.calls)
	})

	t.Run("answers 409 IDEMPOTENCY_IN_PROGRESS while the first request is running", func(t *testing.T) {
		t.Parallel()
		store := newFakeIdemStore()
		h := &idemHandler{status: http.StatusCreated, block: make(chan struct{})}
		chain := newIdemChain(store, h)

		var wg sync.WaitGroup
		var first *httptest.ResponseRecorder
		wg.Go(func() { first = serve(t, chain, idemRequest(t, "alice", "k1", `{"quantity":1}`)) })
		require.Eventually(t, func() bool {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.calls == 1
		}, 5*time.Second, time.Millisecond, "the first request must be inside the handler")

		dup := serve(t, chain, idemRequest(t, "alice", "k1", `{"quantity":1}`))
		close(h.block)
		wg.Wait()

		require.Equal(t, http.StatusConflict, dup.Code)
		require.Equal(t, "1", dup.Header().Get("Retry-After"))
		var body errorBody
		require.NoError(t, json.Unmarshal(dup.Body.Bytes(), &body))
		require.Equal(t, "IDEMPOTENCY_IN_PROGRESS", body.Code)
		require.Equal(t, http.StatusCreated, first.Code)
		require.Equal(t, 1, h.calls)
	})

	t.Run("releases the claim when the handler panics", func(t *testing.T) {
		t.Parallel()
		store, h := newFakeIdemStore(), &idemHandler{status: http.StatusCreated, panics: true}
		chain := newIdemChain(store, h)

		rec := serve(t, chain, idemRequest(t, "alice", "k1", `{"quantity":1}`))

		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.Equal(t, 1, store.released)
		require.Empty(t, store.records, "a retry must be able to run again")
	})

	t.Run("passes requests without a key straight through", func(t *testing.T) {
		t.Parallel()
		store, h := newFakeIdemStore(), &idemHandler{status: http.StatusBadRequest}
		chain := newIdemChain(store, h)

		rec := serve(t, chain, idemRequest(t, "alice", "", `{"quantity":1}`))

		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, 1, h.calls)
		require.Empty(t, store.records)
	})

	t.Run("fails closed with 503 when the store is unreachable", func(t *testing.T) {
		t.Parallel()
		store, h := newFakeIdemStore(), &idemHandler{status: http.StatusCreated}
		store.err = errors.New("dial tcp: connection refused")
		chain := newIdemChain(store, h)

		rec := serve(t, chain, idemRequest(t, "alice", "k1", `{"quantity":1}`))

		require.Equal(t, http.StatusServiceUnavailable, rec.Code)
		var body errorBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, string(domain.CodeUnavailable), body.Code)
		require.Zero(t, h.calls)
	})

	t.Run("rejects a body larger than the limit with 400", func(t *testing.T) {
		t.Parallel()
		store, h := newFakeIdemStore(), &idemHandler{status: http.StatusCreated}
		chain := newIdemChain(store, h)

		rec := serve(t, chain, idemRequest(t, "alice", "k1", strings.Repeat("x", 70_000)))

		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Zero(t, h.calls)
	})

	t.Run("hands the handler the full body after fingerprinting it", func(t *testing.T) {
		t.Parallel()
		var got string
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b := new(strings.Builder)
			_, _ = io.Copy(b, r.Body)
			got = b.String()
			w.WriteHeader(http.StatusCreated)
		})
		chain := newIdemChain(newFakeIdemStore(), next)

		serve(t, chain, idemRequest(t, "alice", "k1", `{"quantity":3}`))

		require.Equal(t, `{"quantity":3}`, got)
	})
}
