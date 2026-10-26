//go:build e2e

// Package e2e_test drives the running service over HTTP, exactly as a client
// would: the binary in its Docker image, with real PostgreSQL and Redis.
// Start the stack first: make up && make migrate && make test-e2e.
package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var (
	baseURL = envOr("E2E_BASE_URL", "http://localhost:8080")
	client  = &http.Client{Timeout: 10 * time.Second}
	// runID keeps users and idempotency keys unique across runs against the
	// same database.
	runID = time.Now().UnixNano()
	seq   atomic.Int64
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func unique(prefix string) string { return fmt.Sprintf("%s-%d-%d", prefix, runID, seq.Add(1)) }

type response struct {
	Status  int
	Header  http.Header
	Body    map[string]any
	RawBody string
}

func call(t *testing.T, method, path, body string, headers map[string]string) response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, baseURL+path, bytes.NewBufferString(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	require.NoError(t, err, "is the stack running? make up && make migrate")
	defer func() { require.NoError(t, res.Body.Close()) }()
	raw, err := io.ReadAll(res.Body)
	require.NoError(t, err)

	out := response{Status: res.StatusCode, Header: res.Header, RawBody: string(raw)}
	if len(raw) > 0 {
		require.NoError(t, json.Unmarshal(raw, &out.Body), "body: %s", raw)
	}
	return out
}

func asUser(user, key string) map[string]string {
	return map[string]string{"X-User-ID": user, "Idempotency-Key": key}
}

// createDeal creates a deal active now and, unless activate is false, opens
// its stock in the gate.
func createDeal(t *testing.T, stock, limit int, activate bool) int64 {
	t.Helper()
	now := time.Now().UTC()
	body := fmt.Sprintf(`{"product_id":%q,"title":"e2e","price_cents":4999,"currency":"USD",`+
		`"total_stock":%d,"per_user_limit":%d,"starts_at":%q,"ends_at":%q}`,
		unique("sku"), stock, limit, now.Add(-time.Minute).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339))
	res := call(t, http.MethodPost, "/v1/deals", body, nil)
	require.Equal(t, http.StatusCreated, res.Status, res.RawBody)
	id := int64(res.Body["id"].(float64))
	if activate {
		act := call(t, http.MethodPost, fmt.Sprintf("/v1/deals/%d/activate", id), "", nil)
		require.Equal(t, http.StatusOK, act.Status, act.RawBody)
	}
	return id
}

// requireAvailable waits for the deal's snapshot to report want units: the
// read path may be served from a short-lived cache.
func requireAvailable(t *testing.T, dealID int64, want int) {
	t.Helper()
	require.Eventually(t, func() bool {
		res := call(t, http.MethodGet, fmt.Sprintf("/v1/deals/%d", dealID), "", nil)
		return res.Status == http.StatusOK && int(res.Body["available_stock"].(float64)) == want
	}, 10*time.Second, 100*time.Millisecond)
}

func reservePath(dealID int64) string { return fmt.Sprintf("/v1/deals/%d/reservations", dealID) }

// TestRetrySameIdempotencyKey_SingleReservation is the phase 4 definition of
// done: retrying with the same Idempotency-Key yields one reservation.
func TestRetrySameIdempotencyKey_SingleReservation(t *testing.T) {
	t.Parallel()
	deal := createDeal(t, 10, 2, true)
	user, key := unique("user"), unique("key")

	first := call(t, http.MethodPost, reservePath(deal), `{"quantity":1}`, asUser(user, key))
	retry := call(t, http.MethodPost, reservePath(deal), `{"quantity":1}`, asUser(user, key))

	require.Equal(t, http.StatusCreated, first.Status, first.RawBody)
	require.Equal(t, http.StatusCreated, retry.Status, retry.RawBody)
	require.Equal(t, first.Body["id"], retry.Body["id"])
	require.Equal(t, "true", retry.Header.Get("Idempotency-Replayed"))
	requireAvailable(t, deal, 9)
}

// TestConcurrentRetries_SingleReservation fires the same request many times
// at once: every answer is the one reservation or "still in progress".
func TestConcurrentRetries_SingleReservation(t *testing.T) {
	t.Parallel()
	const retries = 20
	deal := createDeal(t, 10, 5, true)
	user, key := unique("user"), unique("key")

	var mu sync.Mutex
	ids := map[any]int{}
	inProgress := 0
	var wg sync.WaitGroup
	for range retries {
		wg.Go(func() {
			res := call(t, http.MethodPost, reservePath(deal), `{"quantity":1}`, asUser(user, key))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case res.Status == http.StatusCreated:
				ids[res.Body["id"]]++
			case res.Status == http.StatusConflict && res.Body["code"] == "IDEMPOTENCY_IN_PROGRESS":
				inProgress++
			default:
				t.Errorf("unexpected response %d: %s", res.Status, res.RawBody)
			}
		})
	}
	wg.Wait()

	require.Len(t, ids, 1, "all successful retries must return the same reservation")
	requireAvailable(t, deal, 9)
	t.Logf("%d retries: %d answered with the reservation, %d told to retry later", retries, retries-inProgress, inProgress)
}

func TestIdempotencyKeyReusedForDifferentRequest(t *testing.T) {
	t.Parallel()
	deal := createDeal(t, 10, 5, true)
	user, key := unique("user"), unique("key")
	require.Equal(t, http.StatusCreated, call(t, http.MethodPost, reservePath(deal), `{"quantity":1}`, asUser(user, key)).Status)

	res := call(t, http.MethodPost, reservePath(deal), `{"quantity":2}`, asUser(user, key))

	require.Equal(t, http.StatusConflict, res.Status)
	require.Equal(t, "CONFLICT", res.Body["code"])
	requireAvailable(t, deal, 9)
}

func TestReserveRequiresActivation(t *testing.T) {
	t.Parallel()
	deal := createDeal(t, 10, 1, false)

	res := call(t, http.MethodPost, reservePath(deal), `{"quantity":1}`, asUser(unique("user"), unique("key")))

	require.Equal(t, http.StatusConflict, res.Status)
	require.Equal(t, "DEAL_NOT_ACTIVE", res.Body["code"])
}

func TestReserveConfirmCancelFlow(t *testing.T) {
	t.Parallel()
	deal := createDeal(t, 3, 2, true)
	alice, bob := unique("alice"), unique("bob")

	// alice reserves and confirms one unit.
	res := call(t, http.MethodPost, reservePath(deal), `{"quantity":1}`, asUser(alice, unique("key")))
	require.Equal(t, http.StatusCreated, res.Status, res.RawBody)
	resID := res.Body["id"].(string)
	order := call(t, http.MethodPost, "/v1/reservations/"+resID+"/confirm", "", map[string]string{"X-User-ID": alice})
	require.Equal(t, http.StatusOK, order.Status, order.RawBody)
	require.InDelta(t, 4999, order.Body["total_cents"], 0)

	// The order is final: it can be neither confirmed again nor cancelled.
	again := call(t, http.MethodPost, "/v1/reservations/"+resID+"/confirm", "", map[string]string{"X-User-ID": alice})
	require.Equal(t, http.StatusConflict, again.Status)
	cancelled := call(t, http.MethodDelete, "/v1/reservations/"+resID, "", map[string]string{"X-User-ID": alice})
	require.Equal(t, http.StatusConflict, cancelled.Status)

	// bob takes the other two units, then gives one back.
	bobRes := call(t, http.MethodPost, reservePath(deal), `{"quantity":2}`, asUser(bob, unique("key")))
	require.Equal(t, http.StatusCreated, bobRes.Status, bobRes.RawBody)
	soldOut := call(t, http.MethodPost, reservePath(deal), `{"quantity":1}`, asUser(unique("carol"), unique("key")))
	require.Equal(t, "SOLD_OUT", soldOut.Body["code"])

	// Another user cannot see or cancel bob's reservation.
	intruder := call(t, http.MethodDelete, "/v1/reservations/"+bobRes.Body["id"].(string), "", map[string]string{"X-User-ID": alice})
	require.Equal(t, http.StatusNotFound, intruder.Status)

	gone := call(t, http.MethodDelete, "/v1/reservations/"+bobRes.Body["id"].(string), "", map[string]string{"X-User-ID": bob})
	require.Equal(t, http.StatusNoContent, gone.Status)

	// The cancelled units are back on sale.
	requireAvailable(t, deal, 2)
	carol := call(t, http.MethodPost, reservePath(deal), `{"quantity":1}`, asUser(unique("carol"), unique("key")))
	require.Equal(t, http.StatusCreated, carol.Status, carol.RawBody)
}
