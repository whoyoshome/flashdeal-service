//go:build integration

package redisrepo_test

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/repository/redisrepo"
)

var subjectSeq atomic.Int64

func uniqueSubject() string {
	return fmt.Sprintf("user-%d", subjectSeq.Add(1))
}

func TestRateLimiter(t *testing.T) {
	t.Parallel()

	// One token per 100 s: no refill can land between the calls of a
	// subtest, however slowly they run under -race.
	lim := redisrepo.NewRateLimiter(testRedis, 0.01, 3)

	t.Run("admits up to the burst and then rejects", func(t *testing.T) {
		subject := uniqueSubject()
		for i := range 3 {
			res, err := lim.Allow(t.Context(), subject)
			require.NoError(t, err, "call %d", i)
			require.True(t, res.Allowed, "call %d", i)
			require.Equal(t, 3, res.Limit)
		}
		res, err := lim.Allow(t.Context(), subject)
		require.NoError(t, err)
		require.False(t, res.Allowed)
		require.GreaterOrEqual(t, res.RetryAfter, time.Millisecond)
	})

	t.Run("isolates subjects from each other", func(t *testing.T) {
		a, b := uniqueSubject(), uniqueSubject()
		for range 3 {
			_, err := lim.Allow(t.Context(), a)
			require.NoError(t, err)
		}
		res, err := lim.Allow(t.Context(), b)
		require.NoError(t, err)
		require.True(t, res.Allowed)
	})
}
