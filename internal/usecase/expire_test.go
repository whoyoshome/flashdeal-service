package usecase_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

var errBoom = errors.New("connection reset")

type expireDeps struct {
	queue *fakeQueue
	deals *fakeDeals
	gate  *fakeGate
	tx    *fakeTx
	clock *fakeClock
}

func newExpireDeps(pending ...domain.Reservation) *expireDeps {
	return &expireDeps{
		queue: &fakeQueue{pending: pending},
		deals: newFakeDeals(),
		gate:  &fakeGate{},
		tx:    &fakeTx{},
		clock: &fakeClock{now: t0},
	}
}

func (d *expireDeps) expire(batchSize int) *usecase.Expire {
	return usecase.NewExpire(d.queue, d.deals, d.gate, d.tx, d.clock, batchSize, discardLog)
}

// pendingReservation returns a pending reservation of qty units of dealID,
// created at t0 and expiring after ttl. n makes its ID and user unique.
func pendingReservation(t *testing.T, n int, dealID int64, qty int, ttl time.Duration) domain.Reservation {
	t.Helper()
	r, err := domain.RestoreReservation(domain.ReservationState{
		ID: fmt.Sprintf("00000000-0000-7000-8000-%012d", n), DealID: dealID, UserID: fmt.Sprintf("user-%d", n),
		Quantity: qty, Status: domain.StatusPending, IdempotencyKey: fmt.Sprintf("key-%d", n),
		ExpiresAt: t0.Add(ttl), CreatedAt: t0, UpdatedAt: t0,
	})
	require.NoError(t, err)
	return r
}

func TestExpire(t *testing.T) {
	t.Parallel()

	t.Run("returns the units of a reservation to the deal and the gate once its TTL elapses", func(t *testing.T) {
		t.Parallel()
		deps := newExpireDeps(pendingReservation(t, 1, 7, 2, 5*time.Minute))
		uc := deps.expire(100)

		deps.clock.now = t0.Add(5*time.Minute - time.Second)
		n, err := uc.Execute(t.Context())
		require.NoError(t, err)
		require.Zero(t, n, "a reservation inside its TTL keeps its units")
		require.Empty(t, deps.deals.releases)
		require.Empty(t, deps.gate.releases)

		deps.clock.now = t0.Add(5 * time.Minute)
		n, err = uc.Execute(t.Context())
		require.NoError(t, err)
		require.Equal(t, 1, n)
		require.Len(t, deps.queue.marked, 1)
		require.Equal(t, domain.StatusExpired, deps.queue.marked[0].Status())
		require.Equal(t, t0.Add(5*time.Minute), deps.queue.marked[0].UpdatedAt())
		require.Equal(t, []stockMove{{dealID: 7, qty: 2}}, deps.deals.releases)
		require.Equal(t, []gateCall{{dealID: 7, userID: "user-1", qty: 2}}, deps.gate.releases)
	})

	t.Run("releases every reservation of a batch in one call", func(t *testing.T) {
		t.Parallel()
		deps := newExpireDeps(
			pendingReservation(t, 1, 9, 1, time.Minute),
			pendingReservation(t, 2, 7, 2, time.Minute),
			pendingReservation(t, 3, 9, 3, time.Minute),
		)
		deps.clock.now = t0.Add(time.Hour)

		n, err := deps.expire(100).Execute(t.Context())

		require.NoError(t, err)
		require.Equal(t, 3, n)
		require.Equal(t, 1, deps.deals.releaseCalls, "one call per batch lets the store order its locks")
		require.ElementsMatch(t, []stockMove{{dealID: 9, qty: 1}, {dealID: 7, qty: 2}, {dealID: 9, qty: 3}}, deps.deals.releases)
		require.Len(t, deps.gate.releases, 3, "the gate tracks units per buyer, so it is released per reservation")
	})

	t.Run("drains every due batch, one transaction each", func(t *testing.T) {
		t.Parallel()
		var pending []domain.Reservation
		for i := range 5 {
			pending = append(pending, pendingReservation(t, i+1, 7, 1, time.Minute))
		}
		deps := newExpireDeps(pending...)
		deps.clock.now = t0.Add(time.Hour)

		n, err := deps.expire(2).Execute(t.Context())

		require.NoError(t, err)
		require.Equal(t, 5, n)
		require.Equal(t, 3, deps.tx.calls, "batches of 2, 2 and 1")
		require.Empty(t, deps.queue.pending)
		for _, call := range deps.queue.lockCalls {
			require.Equal(t, 2, call.limit)
			require.Equal(t, t0.Add(time.Hour), call.now)
		}
	})

	t.Run("keeps the expiry when the gate cannot be released", func(t *testing.T) {
		t.Parallel()
		deps := newExpireDeps(pendingReservation(t, 1, 7, 2, time.Minute))
		deps.gate.releaseErr = domain.ErrStockUnavailable
		deps.clock.now = t0.Add(time.Hour)

		n, err := deps.expire(100).Execute(t.Context())

		require.NoError(t, err, "PostgreSQL already committed; the reconciler restores the gate")
		require.Equal(t, 1, n)
		require.Equal(t, []stockMove{{dealID: 7, qty: 2}}, deps.deals.releases)
		require.Len(t, deps.gate.releases, 1)
	})

	t.Run("does nothing when the context is already cancelled", func(t *testing.T) {
		t.Parallel()
		deps := newExpireDeps(pendingReservation(t, 1, 7, 2, time.Minute))
		deps.clock.now = t0.Add(time.Hour)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		n, err := deps.expire(100).Execute(ctx)

		require.NoError(t, err)
		require.Zero(t, n)
		require.Empty(t, deps.queue.lockCalls)
	})

	failures := []struct {
		name   string
		mutate func(*expireDeps)
	}{
		{name: "returns the error when due reservations cannot be locked", mutate: func(d *expireDeps) { d.queue.lockErr = errBoom }},
		{name: "returns the error when expirations cannot be saved", mutate: func(d *expireDeps) { d.queue.markErr = errBoom }},
		{name: "returns the error when the deal's stock cannot be released", mutate: func(d *expireDeps) { d.deals.releaseErr = errBoom }},
	}
	for _, tc := range failures {
		t.Run(tc.name+" and leaves the gate untouched", func(t *testing.T) {
			t.Parallel()
			deps := newExpireDeps(pendingReservation(t, 1, 7, 2, time.Minute))
			deps.clock.now = t0.Add(time.Hour)
			tc.mutate(deps)

			n, err := deps.expire(100).Execute(t.Context())

			require.ErrorIs(t, err, errBoom)
			require.Zero(t, n)
			require.Empty(t, deps.gate.releases, "the transaction rolled back, so no units may return to the gate")
		})
	}
}
