// Package redisrepo implements the use-case ports on top of Redis. It is not
// named "redis" to avoid clashing with the go-redis package.
package redisrepo

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/whoyoshome/flashdeal-service/internal/platform/config"
)

// NewClient creates a Redis client with explicit timeouts and verifies the
// server is reachable, so a misconfigured instance fails at startup.
func NewClient(ctx context.Context, cfg config.Redis) (*redis.Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		// Honor caller deadlines in addition to the socket timeouts.
		ContextTimeoutEnabled: true,
		// No retries. With Redis down, each retry waited out another dial
		// timeout, so reservations hung until the request timeout (5 s) before
		// failing closed. A retry after a lost reply could also run a
		// reservation script twice, taking the buyer's units from the gate twice.
		// A failed call surfaces as ErrStockUnavailable and the client retries
		// the whole request with its Idempotency-Key.
		MaxRetries: -1,
		// go-redis also retries a failed dial on its own, up to five times by
		// default (a value below 1 restores that default). One attempt keeps a
		// Redis outage within a single DialTimeout.
		DialerRetries: 1,
	})

	pingCtx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		return nil, errors.Join(fmt.Errorf("ping redis: %w", err), rdb.Close())
	}
	return rdb, nil
}
