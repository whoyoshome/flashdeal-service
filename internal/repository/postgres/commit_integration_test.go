//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

// TestReservationRepoAsyncCommit pins ADR 0006: the reservation transaction
// commits asynchronously, the setting is in force before a stock shard is
// locked, and it never outlives that transaction.
func TestReservationRepoAsyncCommit(t *testing.T) {
	t.Parallel()
	rec := &sqlRecorder{}
	// One connection, so the follow-up query runs on the same session the
	// reservation used.
	pool, err := postgres.NewPool(t.Context(), config.Postgres{
		URL: testURL, MaxConns: 1, ConnectTimeout: 5 * time.Second, StatementTimeout: 5 * time.Second,
	}, rec)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	d := createDeal(t, dealOpts{stock: 10})

	_, created, err := postgres.NewReservationRepo(pool).Create(t.Context(), newReservation(t, d.ID(), uniqueUser(), "k", 1))
	require.NoError(t, err)
	require.True(t, created)

	var stmts []string
	for _, s := range rec.statements() {
		switch {
		case strings.HasPrefix(s, "begin"), strings.HasPrefix(s, "commit"),
			strings.HasPrefix(s, "SET LOCAL"), strings.HasPrefix(s, "INSERT"), strings.HasPrefix(s, "UPDATE"),
			strings.HasPrefix(s, "WITH"), strings.HasPrefix(s, "SAVEPOINT"):
			stmts = append(stmts, strings.Fields(s)[0])
		}
	}
	// WITH is the statement that locks a stock shard and takes the units.
	require.Equal(t, []string{"begin", "SET", "INSERT", "SAVEPOINT", "WITH", "commit"}, stmts,
		"async commit is set inside the transaction, before any stock shard is locked")

	var setting string
	require.NoError(t, pool.QueryRow(t.Context(), "SHOW synchronous_commit").Scan(&setting))
	require.Equal(t, "on", setting, "SET LOCAL must not leak to the next use of the session")
}
