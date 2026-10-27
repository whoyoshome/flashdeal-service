//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/platform/config"
	"github.com/whoyoshome/flashdeal-service/internal/repository/postgres"
)

// sqlRecorder is a pgx.QueryTracer that records every statement, in order.
type sqlRecorder struct {
	mu   sync.Mutex
	sqls []string
}

func (r *sqlRecorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sqls = append(r.sqls, strings.Join(strings.Fields(data.SQL), " "))
	return ctx
}

func (r *sqlRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (r *sqlRecorder) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return ctx
}

func (r *sqlRecorder) TraceBatchQuery(_ context.Context, _ *pgx.Conn, data pgx.TraceBatchQueryData) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sqls = append(r.sqls, strings.Join(strings.Fields(data.SQL), " "))
}

func (r *sqlRecorder) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}

func (r *sqlRecorder) statements() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sqls...)
}

func (r *sqlRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sqls = nil
}

// reservationStatements keeps the statements of a reservation's path, by
// first keyword, dropping the reads around them.
func reservationStatements(sqls []string) []string {
	var stmts []string
	for _, s := range sqls {
		switch {
		case strings.HasPrefix(s, "begin"), strings.HasPrefix(s, "commit"),
			strings.HasPrefix(s, "SET LOCAL"), strings.HasPrefix(s, "INSERT"), strings.HasPrefix(s, "UPDATE"),
			strings.HasPrefix(s, "WITH"), strings.HasPrefix(s, "SAVEPOINT"):
			stmts = append(stmts, strings.Fields(s)[0])
		}
	}
	return stmts
}

// TestReservationRepoAsyncCommit pins ADR 0006 and ADR 0013: a reservation
// commits asynchronously on both of its paths, the setting is in force
// before a stock shard is locked, and it never outlives the transaction.
func TestReservationRepoAsyncCommit(t *testing.T) {
	t.Parallel()

	// One connection, so the follow-up queries run on the same session the
	// reservation used.
	newRecordedPool := func(t *testing.T) (*sqlRecorder, *pgxpool.Pool) {
		t.Helper()
		rec := &sqlRecorder{}
		pool, err := postgres.NewPool(t.Context(), config.Postgres{
			URL: testURL, MaxConns: 1, ConnectTimeout: 5 * time.Second, StatementTimeout: 5 * time.Second,
		}, rec)
		require.NoError(t, err)
		t.Cleanup(pool.Close)
		return rec, pool
	}
	requireNoLeak := func(t *testing.T, pool *pgxpool.Pool) {
		t.Helper()
		var setting string
		require.NoError(t, pool.QueryRow(t.Context(), "SHOW synchronous_commit").Scan(&setting))
		require.Equal(t, "on", setting, "the async setting must not leak to the next use of the session")
	}

	t.Run("reserves on a free shard in one statement", func(t *testing.T) {
		t.Parallel()
		rec, pool := newRecordedPool(t)
		d := createDeal(t, dealOpts{stock: 10})

		_, created, err := postgres.NewReservationRepo(pool).Create(t.Context(), newReservation(t, d.ID(), uniqueUser(), "k", 1))
		require.NoError(t, err)
		require.True(t, created)

		sqls := rec.statements()
		require.Equal(t, []string{"WITH"}, reservationStatements(sqls), "no transaction block, one round trip")
		require.Contains(t, sqls[0], "set_config('synchronous_commit', 'off', true)")
		requireNoLeak(t, pool)
	})

	t.Run("falls back to a transaction when every shard with room is locked", func(t *testing.T) {
		t.Parallel()
		rec, pool := newRecordedPool(t)
		d := createDeal(t, dealOpts{stock: 10, shards: 1})
		var pid int
		require.NoError(t, pool.QueryRow(t.Context(), "SELECT pg_backend_pid()").Scan(&pid))
		rec.reset()

		first := newReservation(t, d.ID(), uniqueUser(), "k", 1)
		second := newReservation(t, d.ID(), uniqueUser(), "k", 1)
		held := make(chan struct{})
		release := make(chan struct{})
		holder := make(chan error, 1)
		go func() {
			holder <- postgres.NewTxManager(testPool).WithinTx(t.Context(), func(ctx context.Context) error {
				if _, _, err := postgres.NewReservationRepo(testPool).Create(ctx, first); err != nil {
					return err
				}
				close(held)
				<-release
				return nil
			})
		}()
		<-held

		created := make(chan error, 1)
		go func() {
			_, _, err := postgres.NewReservationRepo(pool).Create(t.Context(), second)
			created <- err
		}()
		require.Eventually(t, func() bool {
			var waiting bool
			err := testPool.QueryRow(t.Context(),
				"SELECT coalesce(wait_event_type = 'Lock', false) FROM pg_stat_activity WHERE pid = $1", pid).Scan(&waiting)
			return err == nil && waiting
		}, 5*time.Second, 10*time.Millisecond, "the fallback waits for the held shard")
		close(release)
		require.NoError(t, <-holder)
		require.NoError(t, <-created)

		// The single statement, then the transaction: SAVEPOINT and the two
		// shard statements (skip, then wait) after the async setting.
		require.Equal(t, []string{"WITH", "begin", "SET", "INSERT", "SAVEPOINT", "WITH", "WITH", "commit"},
			reservationStatements(rec.statements()))
		requireNoLeak(t, pool)
	})
}
