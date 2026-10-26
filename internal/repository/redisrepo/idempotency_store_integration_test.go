//go:build integration

package redisrepo_test

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/repository/redisrepo"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

var keySeq atomic.Int64

func uniqueRef() usecase.IdempotencyRef {
	return usecase.IdempotencyRef{UserID: fmt.Sprintf("user-%d", keySeq.Add(1)), Key: "key"}
}

func TestIdempotencyStore(t *testing.T) {
	t.Parallel()
	store := redisrepo.NewIdempotencyStore(testRedis)

	t.Run("claims a new key and reports it as in progress to the next caller", func(t *testing.T) {
		t.Parallel()
		ref := uniqueRef()

		first, err := store.Begin(t.Context(), ref, "fp-1", "tok-1", time.Minute)
		require.NoError(t, err)
		require.Equal(t, usecase.IdempotencyNew, first.State)

		second, err := store.Begin(t.Context(), ref, "fp-1", "tok-2", time.Minute)
		require.NoError(t, err)
		require.Equal(t, usecase.IdempotencyInProgress, second.State)
		require.Equal(t, "fp-1", second.Fingerprint)
	})

	t.Run("replays the completed response with its fingerprint", func(t *testing.T) {
		t.Parallel()
		ref := uniqueRef()
		_, err := store.Begin(t.Context(), ref, "fp-1", "tok-1", time.Minute)
		require.NoError(t, err)

		require.NoError(t, store.Complete(t.Context(), ref, "fp-1", 201, []byte(`{"id":"r-1"}`), time.Hour))

		got, err := store.Begin(t.Context(), ref, "fp-1", "tok-2", time.Minute)
		require.NoError(t, err)
		require.Equal(t, usecase.IdempotencyClaim{
			State: usecase.IdempotencyDone, Fingerprint: "fp-1", Status: 201, Body: []byte(`{"id":"r-1"}`),
		}, got)
		ttl, err := testRedis.TTL(t.Context(), redisKey(ref)).Result()
		require.NoError(t, err)
		require.InDelta(t, time.Hour.Seconds(), ttl.Seconds(), 5)
	})

	t.Run("release lets the next caller claim the key again", func(t *testing.T) {
		t.Parallel()
		ref := uniqueRef()
		_, err := store.Begin(t.Context(), ref, "fp-1", "tok-1", time.Minute)
		require.NoError(t, err)

		require.NoError(t, store.Release(t.Context(), ref, "tok-1"))

		got, err := store.Begin(t.Context(), ref, "fp-1", "tok-2", time.Minute)
		require.NoError(t, err)
		require.Equal(t, usecase.IdempotencyNew, got.State)
	})

	t.Run("release never drops another request's claim", func(t *testing.T) {
		t.Parallel()
		ref := uniqueRef()
		_, err := store.Begin(t.Context(), ref, "fp-1", "tok-owner", time.Minute)
		require.NoError(t, err)

		require.NoError(t, store.Release(t.Context(), ref, "tok-stale"))

		got, err := store.Begin(t.Context(), ref, "fp-1", "tok-3", time.Minute)
		require.NoError(t, err)
		require.Equal(t, usecase.IdempotencyInProgress, got.State)
	})

	t.Run("release never drops a completed response", func(t *testing.T) {
		t.Parallel()
		ref := uniqueRef()
		_, err := store.Begin(t.Context(), ref, "fp-1", "tok-1", time.Minute)
		require.NoError(t, err)
		require.NoError(t, store.Complete(t.Context(), ref, "fp-1", 201, []byte(`{}`), time.Hour))

		require.NoError(t, store.Release(t.Context(), ref, "tok-1"))

		got, err := store.Begin(t.Context(), ref, "fp-1", "tok-2", time.Minute)
		require.NoError(t, err)
		require.Equal(t, usecase.IdempotencyDone, got.State)
	})

	t.Run("the claim expires after its lock TTL", func(t *testing.T) {
		t.Parallel()
		ref := uniqueRef()

		_, err := store.Begin(t.Context(), ref, "fp-1", "tok-1", 1500*time.Millisecond)
		require.NoError(t, err)

		pttl, err := testRedis.PTTL(t.Context(), redisKey(ref)).Result()
		require.NoError(t, err)
		require.Positive(t, pttl)
		require.LessOrEqual(t, pttl, 1500*time.Millisecond)
	})
}

// TestIdempotencyStore_ConcurrentClaims checks that exactly one of many
// simultaneous retries owns the key.
func TestIdempotencyStore_ConcurrentClaims(t *testing.T) {
	t.Parallel()
	const retries = 100
	store := redisrepo.NewIdempotencyStore(testRedis)
	ref := uniqueRef()

	var owners, waiting atomic.Int64
	var wg sync.WaitGroup
	for i := range retries {
		wg.Go(func() {
			claim, err := store.Begin(t.Context(), ref, "fp", fmt.Sprintf("tok-%d", i), time.Minute)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			switch claim.State {
			case usecase.IdempotencyNew:
				owners.Add(1)
			case usecase.IdempotencyInProgress:
				waiting.Add(1)
			default:
				t.Errorf("unexpected state %v", claim.State)
			}
		})
	}
	wg.Wait()

	require.EqualValues(t, 1, owners.Load())
	require.EqualValues(t, retries-1, waiting.Load())
}

func TestIdempotencyStore_FailsClosed(t *testing.T) {
	t.Parallel()
	down := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { _ = down.Close() })
	store := redisrepo.NewIdempotencyStore(down)

	_, err := store.Begin(t.Context(), usecase.IdempotencyRef{UserID: "u", Key: "k"}, "fp", "tok", time.Minute)
	require.ErrorIs(t, err, domain.ErrStockUnavailable)
	require.ErrorIs(t, store.Complete(t.Context(), usecase.IdempotencyRef{UserID: "u", Key: "k"}, "fp", 201, nil, time.Hour), domain.ErrStockUnavailable)
	require.ErrorIs(t, store.Release(t.Context(), usecase.IdempotencyRef{UserID: "u", Key: "k"}, "tok"), domain.ErrStockUnavailable)
}

// redisKey mirrors the store's key layout to inspect TTLs directly.
func redisKey(ref usecase.IdempotencyRef) string {
	return fmt.Sprintf("idem:{%s}:%s", ref.UserID, ref.Key)
}
