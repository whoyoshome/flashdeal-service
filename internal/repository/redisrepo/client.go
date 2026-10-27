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
// REDIS_URL (redis:// or rediss://) wins over Addr/Password/DB so managed
// Redis (Upstash) works without splitting the connection string.
func NewClient(ctx context.Context, cfg config.Redis) (*redis.Client, error) {
	opt, err := redisOptions(cfg)
	if err != nil {
		return nil, err
	}
	rdb := redis.NewClient(opt)

	pingCtx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		return nil, errors.Join(fmt.Errorf("ping redis: %w", err), rdb.Close())
	}
	return rdb, nil
}

func redisOptions(cfg config.Redis) (*redis.Options, error) {
	var opt *redis.Options
	if cfg.URL != "" {
		parsed, err := redis.ParseURL(cfg.URL)
		if err != nil {
			return nil, fmt.Errorf("parse REDIS_URL: %w", err)
		}
		opt = parsed
	} else {
		opt = &redis.Options{
			Addr:     cfg.Addr,
			Password: cfg.Password,
			DB:       cfg.DB,
		}
	}
	opt.PoolSize = cfg.PoolSize
	opt.DialTimeout = cfg.DialTimeout
	opt.ReadTimeout = cfg.ReadTimeout
	opt.WriteTimeout = cfg.WriteTimeout
	// Honor caller deadlines in addition to the socket timeouts.
	opt.ContextTimeoutEnabled = true
	// No retries. With Redis down, each retry waited out another dial
	// timeout, so reservations hung until the request timeout (5 s) before
	// failing closed. A retry after a lost reply could also run a
	// reservation script twice, taking the buyer's units from the gate twice.
	// A failed call surfaces as ErrStockUnavailable and the client retries
	// the whole request with its Idempotency-Key.
	opt.MaxRetries = -1
	// go-redis also retries a failed dial on its own, up to five times by
	// default (a value below 1 restores that default). One attempt keeps a
	// Redis outage within a single DialTimeout.
	opt.DialerRetries = 1
	return opt, nil
}
