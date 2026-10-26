// Package postgres implements the use-case ports on top of PostgreSQL via pgx.
package postgres

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whoyoshome/flashdeal-service/internal/platform/config"
)

// NewPool creates a bounded pgx pool and verifies the database is reachable,
// so a misconfigured instance fails at startup instead of on the first request.
// tracer, when not nil, observes every query (see platform/observability).
func NewPool(ctx context.Context, cfg config.Postgres, tracer pgx.QueryTracer) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		// pgx redacts the password from parse errors.
		return nil, fmt.Errorf("parse postgres url: %w", err)
	}
	pcfg.MaxConns = cfg.MaxConns
	pcfg.MinConns = cfg.MinConns
	pcfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	// A server-side statement timeout bounds every query even if a caller
	// forgets a context deadline.
	pcfg.ConnConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(cfg.StatementTimeout.Milliseconds(), 10)
	pcfg.ConnConfig.Tracer = tracer

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}
