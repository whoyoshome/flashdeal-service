//go:build chaos

// Package chaos_test breaks the running Docker Compose stack under load and
// checks the service degrades the way docs/ARCHITECTURE.md promises: with
// Redis down it fails closed (503) without overselling, and on SIGTERM it
// finishes the requests in flight and exits cleanly.
//
// The tests stop and start containers of the shared stack, so they never
// run in parallel. Start the stack first:
//
//	make up && make migrate && make test-chaos
package chaos_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var (
	baseURL = envOr("CHAOS_BASE_URL", "http://localhost:8080")
	// Every request opens a fresh connection. A reused keep-alive connection
	// that the server closes while shutting down would fail a POST that was
	// never received, which is indistinguishable from a request cut short.
	client = &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
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

// compose runs a docker compose command against the repository's stack.
func compose(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose"}, args...)...) //nolint:gosec // arguments are fixed by the tests below
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "docker compose %s: %s", strings.Join(args, " "), out)
	return string(out)
}

type reply struct {
	status int
	code   string
	body   map[string]any
}

func send(ctx context.Context, method, path, body string, headers map[string]string) (reply, error) {
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, bytes.NewBufferString(body))
	if err != nil {
		return reply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return reply{}, err
	}
	out := reply{status: res.StatusCode}
	if len(raw) > 0 && json.Unmarshal(raw, &out.body) == nil {
		out.code, _ = out.body["code"].(string)
	}
	return out, nil
}

func call(t *testing.T, method, path, body string, headers map[string]string) reply {
	t.Helper()
	r, err := send(t.Context(), method, path, body, headers)
	require.NoError(t, err)
	return r
}

// waitReady polls /readyz. It does not use t.Context(), which is already
// cancelled when cleanup functions run.
func waitReady(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		r, err := send(context.Background(), http.MethodGet, "/readyz", "", nil)
		return err == nil && r.status == http.StatusOK
	}, 90*time.Second, 250*time.Millisecond, "the service never became ready")
}

func createDeal(t *testing.T, stock int) int64 {
	t.Helper()
	now := time.Now().UTC()
	body := fmt.Sprintf(`{"product_id":%q,"title":"chaos","price_cents":100,"currency":"USD",`+
		`"total_stock":%d,"per_user_limit":1,"starts_at":%q,"ends_at":%q}`,
		unique("sku"), stock, now.Add(-time.Minute).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339))
	r := call(t, http.MethodPost, "/v1/deals", body, nil)
	require.Equal(t, http.StatusCreated, r.status)
	id := int64(r.body["id"].(float64))
	activate(t, id)
	return id
}

func activate(t *testing.T, dealID int64) {
	t.Helper()
	r := call(t, http.MethodPost, fmt.Sprintf("/v1/deals/%d/activate", dealID), "", nil)
	require.Equal(t, http.StatusOK, r.status, "activate deal %d", dealID)
}

// requireHeld checks that PostgreSQL has reserved or sold exactly want units
// of the deal, waiting out the short-lived cache in front of single-deal
// reads.
func requireHeld(t *testing.T, dealID int64, want int) {
	t.Helper()
	var held int
	require.Eventually(t, func() bool {
		r, err := send(t.Context(), http.MethodGet, fmt.Sprintf("/v1/deals/%d", dealID), "", nil)
		if err != nil || r.status != http.StatusOK {
			return false
		}
		held = int(r.body["total_stock"].(float64) - r.body["available_stock"].(float64))
		return held == want
	}, 15*time.Second, 250*time.Millisecond, "deal %d: PostgreSQL holds %d units, buyers were told %d", dealID, held, want)
}

// load sends reservations for one deal from several workers at a steady
// pace and tallies every outcome by the phase the test is in.
type load struct {
	dealID int64
	phase  atomic.Value
	mu     sync.Mutex
	counts map[string]int
	// slowest is the longest response time per outcome.
	slowest map[string]time.Duration
	// created counts 201s; each is a unit PostgreSQL must hold.
	created atomic.Int64
	// unanswered holds requests that got a connection but no response. Only
	// a stopping API may produce them: Docker Desktop's port proxy accepts
	// connections while the container behind it is gone. Whether one was cut
	// short after writing is decided by PostgreSQL, not by the client.
	unanswered []error
}

func startLoad(t *testing.T, dealID int64, workers int, every time.Duration) (*load, func()) {
	t.Helper()
	l := &load{dealID: dealID, counts: map[string]int{}, slowest: map[string]time.Duration{}}
	l.setPhase("before")
	// Stopping only ends the loop: a request already sent is never cancelled,
	// because a reservation committed on the server after its client gave up
	// would look like a lost answer.
	done := make(chan struct{})
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			tick := time.NewTicker(every)
			defer tick.Stop()
			for {
				select {
				case <-done:
					return
				case <-tick.C:
					l.reserveOnce()
				}
			}
		})
	}
	return l, func() {
		close(done)
		wg.Wait()
	}
}

func (l *load) setPhase(p string) { l.phase.Store(p) }

// reserveOnce sends one reservation and tallies it under the phase in which
// it was sent. A request that passed the gate just before Redis stopped may
// still be answered 201 during the outage; one sent during the outage never.
func (l *load) reserveOnce() {
	phase := l.phase.Load().(string)
	start := time.Now()
	user := unique("user")
	r, err := send(context.Background(), http.MethodPost, fmt.Sprintf("/v1/deals/%d/reservations", l.dealID), `{"quantity":1}`,
		map[string]string{"X-User-ID": user, "Idempotency-Key": user})
	outcome := fmt.Sprintf("%d %s", r.status, r.code)
	switch {
	case err != nil && refused(err):
		outcome = "connection refused"
	case err != nil:
		outcome = "unanswered"
	case r.status == http.StatusCreated:
		l.created.Add(1)
		outcome = "201"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.counts[phase+": "+outcome]++
	l.slowest[phase+": "+outcome] = max(l.slowest[phase+": "+outcome], time.Since(start))
	if outcome == "unanswered" {
		l.unanswered = append(l.unanswered, err)
	}
}

// slowestResponses returns the longest response time per outcome.
func (l *load) slowestResponses() map[string]time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return maps.Clone(l.slowest)
}

func (l *load) snapshot() map[string]int {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]int, len(l.counts))
	for k, v := range l.counts {
		out[k] = v
	}
	return out
}

// refused reports a connection the server never accepted, which is the
// expected answer of a stopped listener.
func refused(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

// during runs the load in phase p for d.
func during(l *load, p string, d time.Duration) {
	l.setPhase(p)
	<-time.After(d)
}

// TestRedisOutage_FailsClosedWithoutOverselling stops Redis while buyers keep
// reserving. Every reservation answered during the outage must be refused
// with 503 STOCK_UNAVAILABLE within a second, never sold without the gate,
// and every unit a buyer was told is theirs must be held in PostgreSQL. When
// Redis returns, sales resume.
func TestRedisOutage_FailsClosedWithoutOverselling(t *testing.T) {
	waitReady(t)
	deal := createDeal(t, 100_000)
	l, stop := startLoad(t, deal, 10, 20*time.Millisecond)
	t.Cleanup(func() {
		compose(t, "start", "redis")
		waitReady(t)
	})

	during(l, "before", 2*time.Second)
	l.setPhase("stopping")
	compose(t, "stop", "redis")
	during(l, "outage", 5*time.Second)
	l.setPhase("starting")
	compose(t, "start", "redis")
	waitReady(t)
	// Activation is idempotent: if Redis came back without the deal's keys
	// it loads them again from PostgreSQL, otherwise it changes nothing.
	activate(t, deal)
	during(l, "after", 3*time.Second)
	stop()

	counts := l.snapshot()
	t.Logf("outcomes: %v", counts)
	t.Logf("slowest response per outcome: %v", l.slowestResponses())
	require.Positive(t, counts["before: 201"], "the deal sold before the outage")
	require.Positive(t, counts["outage: 503 STOCK_UNAVAILABLE"], "requests sent during the outage get 503")
	require.Positive(t, counts["after: 201"], "sales resume once Redis is back")
	for outcome, n := range counts {
		if strings.HasPrefix(outcome, "outage: ") {
			require.Equal(t, "outage: 503 STOCK_UNAVAILABLE", outcome, "%d requests sent during the outage got %q", n, outcome)
		}
		require.NotContains(t, outcome, ": 500", "no internal errors")
	}
	require.Empty(t, l.unanswered, "Redis being down never leaves a request unanswered")
	// Failing closed must also be fast: a request hung on a dead Redis holds a
	// connection and a goroutine, and under a flash sale they pile up.
	require.Less(t, l.slowestResponses()["outage: 503 STOCK_UNAVAILABLE"], time.Second,
		"the outage is answered within one dial timeout, not the request timeout")

	// Zero overselling: PostgreSQL holds exactly the units buyers were sold.
	requireHeld(t, deal, int(l.created.Load()))
}

// TestSIGTERM_UnderLoad_DrainsAndExitsCleanly stops the API container while
// buyers keep reserving. Requests already received must be answered (only
// new connections may be refused), the process must exit with code 0, and
// every unit sold must be held in PostgreSQL.
func TestSIGTERM_UnderLoad_DrainsAndExitsCleanly(t *testing.T) {
	waitReady(t)
	deal := createDeal(t, 100_000)
	l, stop := startLoad(t, deal, 10, 20*time.Millisecond)
	t.Cleanup(func() {
		compose(t, "start", "api")
		waitReady(t)
	})

	during(l, "before", 2*time.Second)
	l.setPhase("stopping")
	compose(t, "stop", "api") // SIGTERM, then up to stop_grace_period
	during(l, "stopped", time.Second)
	stop()

	exitCode := strings.TrimSpace(compose(t, "ps", "--all", "--format", "{{.ExitCode}}", "api"))
	require.Equal(t, "0", exitCode, "the API exits cleanly on SIGTERM")
	require.Contains(t, compose(t, "logs", "--tail", "20", "api"), `"msg":"stopped"`)

	counts := l.snapshot()
	t.Logf("outcomes: %v", counts)
	t.Logf("slowest response per outcome: %v", l.slowestResponses())
	require.Positive(t, counts["before: 201"])
	for outcome := range counts {
		require.NotContains(t, outcome, ": 5", "no request failed with a 5xx while draining")
		require.NotEqual(t, "before: unanswered", outcome)
	}

	// The drain guarantee: every unit PostgreSQL holds was answered with a
	// 201. A request finished on the server but cut short on the wire would
	// leave PostgreSQL holding more units than buyers were told about.
	compose(t, "start", "api")
	waitReady(t)
	requireHeld(t, deal, int(l.created.Load()))
}
