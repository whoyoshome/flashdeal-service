//go:build integration

package redisrepo_test

import (
	"context"
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

// countingSource is the database stand-in behind the cache. When release is
// set, every load waits on it, so concurrent callers pile up behind one load.
type countingSource struct {
	deals   map[int64]domain.Deal
	calls   atomic.Int64
	release chan struct{}
	// ctxErr records the context error seen by the load.
	ctxErr atomic.Value
}

func (s *countingSource) GetByID(ctx context.Context, id int64) (domain.Deal, error) {
	s.calls.Add(1)
	if s.release != nil {
		<-s.release
	}
	s.ctxErr.Store(fmt.Sprint(ctx.Err()))
	d, ok := s.deals[id]
	if !ok {
		return domain.Deal{}, domain.ErrDealNotFound
	}
	return d, nil
}

func newSource(deals ...domain.Deal) *countingSource {
	s := &countingSource{deals: map[int64]domain.Deal{}}
	for _, d := range deals {
		s.deals[d.ID()] = d
	}
	return s
}

func TestDealCache(t *testing.T) {
	t.Parallel()

	t.Run("loads a miss from the source once and serves the next read from Redis", func(t *testing.T) {
		t.Parallel()
		deal := newDeal(t, 10, 2)
		src := newSource(deal)
		cache := redisrepo.NewDealCache(testRedis, src, time.Minute)

		first, err := cache.GetByID(t.Context(), deal.ID())
		require.NoError(t, err)
		second, err := cache.GetByID(t.Context(), deal.ID())
		require.NoError(t, err)

		require.EqualValues(t, 1, src.calls.Load())
		require.Equal(t, deal.ID(), second.ID())
		require.Equal(t, deal.Price(), second.Price())
		require.Equal(t, deal.PerUserLimit(), second.PerUserLimit())
		require.True(t, deal.StartsAt().Equal(second.StartsAt()))
		require.True(t, deal.EndsAt().Equal(second.EndsAt()))
		require.Equal(t, time.UTC, second.EndsAt().Location())
		require.Equal(t, first.Available(), second.Available())
	})

	t.Run("stores snapshots with the configured TTL", func(t *testing.T) {
		t.Parallel()
		deal := newDeal(t, 10, 2)
		cache := redisrepo.NewDealCache(testRedis, newSource(deal), 3*time.Second)

		_, err := cache.GetByID(t.Context(), deal.ID())
		require.NoError(t, err)

		pttl, err := testRedis.PTTL(t.Context(), fmt.Sprintf("deal:{%d}:cache", deal.ID())).Result()
		require.NoError(t, err)
		require.Positive(t, pttl)
		require.LessOrEqual(t, pttl, 3*time.Second)
	})

	t.Run("returns ErrDealNotFound and caches nothing for an unknown deal", func(t *testing.T) {
		t.Parallel()
		src := newSource()
		cache := redisrepo.NewDealCache(testRedis, src, time.Minute)

		_, err := cache.GetByID(t.Context(), 424242)
		require.ErrorIs(t, err, domain.ErrDealNotFound)
		_, err = cache.GetByID(t.Context(), 424242)
		require.ErrorIs(t, err, domain.ErrDealNotFound)

		require.EqualValues(t, 2, src.calls.Load())
	})

	t.Run("treats a corrupt entry as a miss", func(t *testing.T) {
		t.Parallel()
		deal := newDeal(t, 10, 2)
		src := newSource(deal)
		cache := redisrepo.NewDealCache(testRedis, src, time.Minute)
		require.NoError(t, testRedis.Set(t.Context(), fmt.Sprintf("deal:{%d}:cache", deal.ID()), "{not json", time.Minute).Err())

		got, err := cache.GetByID(t.Context(), deal.ID())

		require.NoError(t, err)
		require.Equal(t, deal.ID(), got.ID())
		require.EqualValues(t, 1, src.calls.Load())
	})

	t.Run("falls back to the source when Redis is down", func(t *testing.T) {
		t.Parallel()
		down := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond, MaxRetries: -1})
		t.Cleanup(func() { _ = down.Close() })
		deal := newDeal(t, 10, 2)
		cache := redisrepo.NewDealCache(down, newSource(deal), time.Minute)

		got, err := cache.GetByID(t.Context(), deal.ID())

		require.NoError(t, err)
		require.Equal(t, deal.ID(), got.ID())
	})
}

// TestDealCache_CollapsesConcurrentMisses is the thundering-herd guarantee:
// many simultaneous reads of a cold deal cause exactly one source load.
// Callers that arrive while the load runs join it; any that arrive after it
// finished find the snapshot in Redis.
func TestDealCache_CollapsesConcurrentMisses(t *testing.T) {
	t.Parallel()
	const readers = 200
	deal := newDeal(t, 10, 2)
	src := newSource(deal)
	src.release = make(chan struct{})
	cache := redisrepo.NewDealCache(testRedis, src, time.Minute)

	var failures atomic.Int64
	var wg sync.WaitGroup
	for range readers {
		wg.Go(func() {
			if _, err := cache.GetByID(t.Context(), deal.ID()); err != nil {
				failures.Add(1)
			}
		})
	}
	require.Eventually(t, func() bool { return src.calls.Load() == 1 }, 5*time.Second, time.Millisecond)
	close(src.release)
	wg.Wait()

	require.Zero(t, failures.Load())
	require.EqualValues(t, 1, src.calls.Load())
}

// TestDealCache_LoadSurvivesCallerCancellation checks that the caller who
// starts a shared load cannot cancel it for everyone else.
func TestDealCache_LoadSurvivesCallerCancellation(t *testing.T) {
	t.Parallel()
	deal := newDeal(t, 10, 2)
	src := newSource(deal)
	cache := redisrepo.NewDealCache(testRedis, src, time.Minute)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	got, err := cache.GetByID(ctx, deal.ID())

	require.NoError(t, err)
	require.Equal(t, deal.ID(), got.ID())
	require.Equal(t, "<nil>", src.ctxErr.Load(), "the shared load must run on a live context")
}
