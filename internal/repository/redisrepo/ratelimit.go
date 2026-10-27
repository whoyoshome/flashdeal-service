package redisrepo

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

//go:embed scripts/ratelimit.lua
var rateLimitLua string

// RateLimitDecision is the outcome of one Allow call.
type RateLimitDecision struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Duration
	Limit      int
}

// RateLimiter is a Redis-backed token bucket keyed by caller subject.
type RateLimiter struct {
	rdb      *redis.Client
	script   *redis.Script
	rate     float64
	capacity int
	now      func() time.Time
}

// NewRateLimiter returns a limiter that refills at rate tokens per second up
// to capacity. rate and capacity must be positive.
func NewRateLimiter(rdb *redis.Client, rate float64, capacity int) *RateLimiter {
	return &RateLimiter{
		rdb:      rdb,
		script:   redis.NewScript(rateLimitLua),
		rate:     rate,
		capacity: capacity,
		now:      time.Now,
	}
}

// Allow spends one token for subject. On Redis failure it returns an error
// so the service fails closed instead of admitting unlimited traffic.
func (l *RateLimiter) Allow(ctx context.Context, subject string) (RateLimitDecision, error) {
	nowMS := l.now().UTC().UnixMilli()
	res, err := l.script.Run(ctx, l.rdb, []string{rateLimitKey(subject)},
		l.rate, l.capacity, nowMS, 1,
	).Int64Slice()
	if err != nil {
		return RateLimitDecision{}, fmt.Errorf("rate limit %s: %w", subject, err)
	}
	if len(res) != 3 {
		return RateLimitDecision{}, fmt.Errorf("rate limit %s: unexpected reply len %d", subject, len(res))
	}
	return RateLimitDecision{
		Allowed:    res[0] == 1,
		Remaining:  int(res[1]),
		RetryAfter: time.Duration(res[2]) * time.Millisecond,
		Limit:      l.capacity,
	}, nil
}

func rateLimitKey(subject string) string {
	// Hash tag keeps the bucket on one Cluster slot.
	return "ratelimit:{" + subject + "}"
}
