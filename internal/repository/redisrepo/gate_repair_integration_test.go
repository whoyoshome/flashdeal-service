//go:build integration

package redisrepo_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/repository/redisrepo"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

func buyersOf(t *testing.T, dealID int64) map[string]string {
	t.Helper()
	held, err := testRedis.HGetAll(t.Context(), fmt.Sprintf("deal:{%d}:buyers", dealID)).Result()
	require.NoError(t, err)
	return held
}

func TestStockGateRepair(t *testing.T) {
	t.Parallel()
	gate := redisrepo.NewStockGate(testRedis)

	t.Run("reads the stock of a loaded deal", func(t *testing.T) {
		t.Parallel()
		deal := newDeal(t, 10, 2)
		require.NoError(t, gate.Load(t.Context(), deal))
		require.NoError(t, gate.TryReserve(t.Context(), deal.ID(), "alice", 2, 2))

		stock, loaded, err := gate.Stock(t.Context(), deal.ID())

		require.NoError(t, err)
		require.True(t, loaded)
		require.Equal(t, 8, stock)
	})

	t.Run("reports a deal that is not loaded", func(t *testing.T) {
		t.Parallel()

		_, loaded, err := gate.Stock(t.Context(), newDeal(t, 10, 2).ID())

		require.NoError(t, err)
		require.False(t, loaded)
	})

	t.Run("restores the stock and rebuilds the buyers when the stock is as expected", func(t *testing.T) {
		t.Parallel()
		deal := newDeal(t, 10, 5)
		require.NoError(t, gate.Load(t.Context(), deal))
		// A release of alice's 3 units was lost: the gate shows 4 left and
		// alice still holding them, while PostgreSQL has them back on sale.
		require.NoError(t, gate.TryReserve(t.Context(), deal.ID(), "alice", 3, 5))
		require.NoError(t, gate.TryReserve(t.Context(), deal.ID(), "bob", 3, 5))
		require.NoError(t, gate.Release(t.Context(), deal.ID(), "bob", 1)) // leaves bob at 2
		ttlBefore := testRedis.PTTL(t.Context(), fmt.Sprintf("deal:{%d}:stock", deal.ID())).Val()

		applied, err := gate.Restore(t.Context(), deal.ID(), 5, usecase.GateState{Available: 8, Held: map[string]int{"bob": 2}})

		require.NoError(t, err)
		require.True(t, applied)
		require.Equal(t, 8, stockLeft(t, deal.ID()))
		require.Equal(t, map[string]string{"bob": "2"}, buyersOf(t, deal.ID()))
		stockTTL := testRedis.PTTL(t.Context(), fmt.Sprintf("deal:{%d}:stock", deal.ID())).Val()
		require.InDelta(t, ttlBefore.Milliseconds(), stockTTL.Milliseconds(), float64(time.Minute.Milliseconds()),
			"the repair keeps the expiry tied to the deal's end")
		buyersTTL := testRedis.PTTL(t.Context(), fmt.Sprintf("deal:{%d}:buyers", deal.ID())).Val()
		require.Positive(t, buyersTTL)
		require.NoError(t, gate.TryReserve(t.Context(), deal.ID(), "alice", 5, 5), "alice's limit is free again")
	})

	t.Run("refuses when the stock changed since it was read", func(t *testing.T) {
		t.Parallel()
		deal := newDeal(t, 10, 2)
		require.NoError(t, gate.Load(t.Context(), deal))
		require.NoError(t, gate.TryReserve(t.Context(), deal.ID(), "alice", 1, 2))

		applied, err := gate.Restore(t.Context(), deal.ID(), 10, usecase.GateState{Available: 10, Held: map[string]int{}})

		require.NoError(t, err)
		require.False(t, applied)
		require.Equal(t, 9, stockLeft(t, deal.ID()))
		require.Equal(t, map[string]string{"alice": "1"}, buyersOf(t, deal.ID()))
	})

	t.Run("never loads a deal that is not in the gate", func(t *testing.T) {
		t.Parallel()
		deal := newDeal(t, 10, 2)

		applied, err := gate.Restore(t.Context(), deal.ID(), 0, usecase.GateState{Available: 10, Held: map[string]int{}})

		require.NoError(t, err)
		require.False(t, applied)
		_, loaded, err := gate.Stock(t.Context(), deal.ID())
		require.NoError(t, err)
		require.False(t, loaded)
	})

	t.Run("rebuilds the buyers of a deal with thousands of them", func(t *testing.T) {
		t.Parallel()
		const buyers = 10_000
		deal := newDeal(t, buyers, 1)
		require.NoError(t, gate.Load(t.Context(), deal))
		held := make(map[string]int, buyers)
		for i := range buyers {
			held[fmt.Sprintf("user-%d", i)] = 1
		}

		applied, err := gate.Restore(t.Context(), deal.ID(), buyers, usecase.GateState{Available: 0, Held: held})

		require.NoError(t, err)
		require.True(t, applied)
		require.Len(t, buyersOf(t, deal.ID()), buyers)
	})
}

// TestStockGateRepair_NeverOverwritesConcurrentReservations races a repair
// against buyers. The repair restates the state the gate already had, so
// whichever wins, every unit a buyer took must still be accounted for:
// units left plus units held always equals the stock loaded.
func TestStockGateRepair_NeverOverwritesConcurrentReservations(t *testing.T) {
	t.Parallel()
	const stock, buyers, rounds = 100, 50, 20
	gate := redisrepo.NewStockGate(testRedis)

	for range rounds {
		deal := newDeal(t, stock, 1)
		require.NoError(t, gate.Load(t.Context(), deal))

		var wg sync.WaitGroup
		for i := range buyers {
			wg.Go(func() {
				require.NoError(t, gate.TryReserve(t.Context(), deal.ID(), fmt.Sprintf("user-%d", i), 1, 1))
			})
		}
		wg.Go(func() {
			_, err := gate.Restore(t.Context(), deal.ID(), stock, usecase.GateState{Available: stock, Held: map[string]int{}})
			require.NoError(t, err)
		})
		wg.Wait()

		require.Equal(t, stock-buyers, stockLeft(t, deal.ID()))
		require.Len(t, buyersOf(t, deal.ID()), buyers, "every buyer keeps their unit")
	}
}
