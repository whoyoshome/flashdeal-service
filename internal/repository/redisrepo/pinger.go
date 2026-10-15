package redisrepo

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// Pinger adapts a Redis client to a plain context-aware health check, so the
// HTTP layer can probe Redis without importing go-redis.
type Pinger struct {
	rdb *redis.Client
}

// NewPinger returns a Pinger backed by rdb.
func NewPinger(rdb *redis.Client) *Pinger {
	return &Pinger{rdb: rdb}
}

// Ping returns nil when Redis answers PING before ctx expires.
func (p *Pinger) Ping(ctx context.Context) error {
	if err := p.rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("ping redis: %w", err)
	}
	return nil
}
