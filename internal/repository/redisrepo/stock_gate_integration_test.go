//go:build integration

package redisrepo_test

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/repository/redisrepo"
)

// TestStockGate_NoOversell is the most important test in the repository:
// 1000 concurrent buyers compete for 100 units and exactly 100 succeed.
func TestStockGate_NoOversell(t *testing.T) {
	t.Parallel()
	const stock, buyers = 100, 1000
	gate := redisrepo.NewStockGate(testRedis)
	deal := newDeal(t, stock, 1)
	require.NoError(t, gate.Load(t.Context(), deal))

	var ok, soldOut atomic.Int64
	var wg sync.WaitGroup
	for i := range buyers {
		wg.Go(func() {
			err := gate.TryReserve(t.Context(), deal.ID(), fmt.Sprintf("user-%d", i), 1, 1)
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, domain.ErrSoldOut):
				soldOut.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
	wg.Wait()

	require.EqualValues(t, stock, ok.Load())
	require.EqualValues(t, buyers-stock, soldOut.Load())
	require.Zero(t, stockLeft(t, deal.ID()))
}

// TestStockGate_PerUserLimit checks the cumulative limit holds when one user
// fires requests in parallel, which a per-request check cannot guarantee.
func TestStockGate_PerUserLimit(t *testing.T) {
	t.Parallel()
	const limit, attempts = 2, 50
	gate := redisrepo.NewStockGate(testRedis)
	deal := newDeal(t, 100, limit)
	require.NoError(t, gate.Load(t.Context(), deal))

	var ok, limited atomic.Int64
	var wg sync.WaitGroup
	for range attempts {
		wg.Go(func() {
			err := gate.TryReserve(t.Context(), deal.ID(), "greedy-user", 1, limit)
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, domain.ErrUserLimitReached):
				limited.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
	wg.Wait()

	require.EqualValues(t, limit, ok.Load())
	require.EqualValues(t, attempts-limit, limited.Load())
	require.Equal(t, 100-limit, stockLeft(t, deal.ID()))
}

func TestStockGate(t *testing.T) {
	t.Parallel()
	gate := redisrepo.NewStockGate(testRedis)

	t.Run("returns ErrDealNotActive before the deal is loaded", func(t *testing.T) {
		t.Parallel()
		deal := newDeal(t, 10, 1)

		err := gate.TryReserve(t.Context(), deal.ID(), "u", 1, 1)

		require.ErrorIs(t, err, domain.ErrDealNotActive)
	})

	t.Run("loads the units PostgreSQL reports available, expiring after the deal ends", func(t *testing.T) {
		t.Parallel()
		deal := newDeal(t, 10, 1)

		require.NoError(t, gate.Load(t.Context(), deal))

		require.Equal(t, 10, stockLeft(t, deal.ID()))
		ttl, err := testRedis.TTL(t.Context(), fmt.Sprintf("deal:{%d}:stock", deal.ID())).Result()
		require.NoError(t, err)
		require.InDelta(t, (2 * time.Hour).Seconds(), ttl.Seconds(), 60, "ends_at plus a one-hour margin")
	})

	t.Run("does not reset a live counter when activated again", func(t *testing.T) {
		t.Parallel()
		deal := newDeal(t, 10, 5)
		require.NoError(t, gate.Load(t.Context(), deal))
		require.NoError(t, gate.TryReserve(t.Context(), deal.ID(), "u", 3, 5))

		require.NoError(t, gate.Load(t.Context(), deal))

		require.Equal(t, 7, stockLeft(t, deal.ID()))
	})

	t.Run("rejects a request larger than the remaining stock without taking anything", func(t *testing.T) {
		t.Parallel()
		deal := newDeal(t, 2, 5)
		require.NoError(t, gate.Load(t.Context(), deal))

		require.ErrorIs(t, gate.TryReserve(t.Context(), deal.ID(), "u", 3, 5), domain.ErrSoldOut)

		require.Equal(t, 2, stockLeft(t, deal.ID()))
	})

	t.Run("gives the buyer's hash the deal's expiry", func(t *testing.T) {
		t.Parallel()
		deal := newDeal(t, 10, 1)
		require.NoError(t, gate.Load(t.Context(), deal))

		require.NoError(t, gate.TryReserve(t.Context(), deal.ID(), "u", 1, 1))

		ttl, err := testRedis.TTL(t.Context(), fmt.Sprintf("deal:{%d}:buyers", deal.ID())).Result()
		require.NoError(t, err)
		require.Positive(t, ttl, "the buyers hash must not live forever")
	})

	t.Run("release returns the units and lets the buyer reserve again", func(t *testing.T) {
		t.Parallel()
		deal := newDeal(t, 10, 1)
		require.NoError(t, gate.Load(t.Context(), deal))
		require.NoError(t, gate.TryReserve(t.Context(), deal.ID(), "u", 1, 1))
		require.ErrorIs(t, gate.TryReserve(t.Context(), deal.ID(), "u", 1, 1), domain.ErrUserLimitReached)

		require.NoError(t, gate.Release(t.Context(), deal.ID(), "u", 1))

		require.Equal(t, 10, stockLeft(t, deal.ID()))
		require.NoError(t, gate.TryReserve(t.Context(), deal.ID(), "u", 1, 1))
	})

	t.Run("release on a deal that is not loaded is a no-op", func(t *testing.T) {
		t.Parallel()
		deal := newDeal(t, 10, 1)

		require.NoError(t, gate.Release(t.Context(), deal.ID(), "u", 1))

		exists, err := testRedis.Exists(t.Context(), fmt.Sprintf("deal:{%d}:stock", deal.ID())).Result()
		require.NoError(t, err)
		require.Zero(t, exists, "release must not create the stock key")
	})

	t.Run("still works after the script cache is flushed", func(t *testing.T) {
		deal := newDeal(t, 10, 1)
		require.NoError(t, gate.Load(t.Context(), deal))
		require.NoError(t, testRedis.ScriptFlush(t.Context()).Err())

		require.NoError(t, gate.TryReserve(t.Context(), deal.ID(), "u", 1, 1))
	})
}

// TestStockGate_FailsClosed checks that an unreachable Redis rejects every
// operation with ErrStockUnavailable instead of letting sales through.
func TestStockGate_FailsClosed(t *testing.T) {
	t.Parallel()
	down := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1", // nothing listens here
		DialTimeout: 200 * time.Millisecond,
		MaxRetries:  -1,
	})
	t.Cleanup(func() { _ = down.Close() })
	gate := redisrepo.NewStockGate(down)
	deal := newDeal(t, 10, 1)

	require.ErrorIs(t, gate.Load(t.Context(), deal), domain.ErrStockUnavailable)
	require.ErrorIs(t, gate.TryReserve(t.Context(), deal.ID(), "u", 1, 1), domain.ErrStockUnavailable)
	require.ErrorIs(t, gate.Release(t.Context(), deal.ID(), "u", 1), domain.ErrStockUnavailable)
}
