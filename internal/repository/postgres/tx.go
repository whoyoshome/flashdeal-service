package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

var _ usecase.TxManager = (*TxManager)(nil)

// finishTimeout bounds COMMIT and ROLLBACK, which run detached from the
// caller's cancellation and therefore need their own deadline.
const finishTimeout = 2 * time.Second

// txKey is the context key under which the active transaction travels.
type txKey struct{}

// querier is the subset of pgx shared by a pool and a transaction, so the
// same SQL runs inside or outside a transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// TxManager opens transactions and carries them in the context so use cases
// never see a pgx.Tx.
type TxManager struct {
	pool *pgxpool.Pool
}

// NewTxManager returns a TxManager backed by pool.
func NewTxManager(pool *pgxpool.Pool) *TxManager {
	return &TxManager{pool: pool}
}

// WithinTx runs fn in a transaction, committing if fn returns nil and rolling
// back otherwise. If ctx already carries a transaction, fn joins it, so the
// outermost caller decides when to commit.
func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return withinTx(ctx, m.pool, fn)
}

func withinTx(ctx context.Context, pool *pgxpool.Pool, fn func(ctx context.Context) error) (err error) {
	if _, ok := txFrom(ctx); ok {
		return fn(ctx)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err == nil {
			return
		}
		// Roll back even if ctx was cancelled; a dead request must not leave
		// a transaction open on the server.
		rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
		defer cancel()
		if rbErr := tx.Rollback(rbCtx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("rollback tx: %w", rbErr))
		}
	}()

	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		return err
	}
	// Once fn succeeded the outcome is decided, so COMMIT must not depend on
	// whether the caller is still waiting. Cancelling it mid-flight leaves the
	// result unknown: under load the commit landed in PostgreSQL while the
	// client got a 500, holding stock nobody knew about.
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	if err := tx.Commit(commitCtx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

func txFrom(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(pgx.Tx)
	return tx, ok
}

// db returns the transaction in ctx, or the pool when there is none.
func db(ctx context.Context, pool *pgxpool.Pool) querier {
	if tx, ok := txFrom(ctx); ok {
		return tx
	}
	return pool
}
