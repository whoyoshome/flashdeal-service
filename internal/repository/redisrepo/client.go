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
	})

	pingCtx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		return nil, errors.Join(fmt.Errorf("ping redis: %w", err), rdb.Close())
	}
	return rdb, nil
}
