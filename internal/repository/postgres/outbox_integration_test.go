//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/repository/postgres"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

// newOrder returns an order, not yet stored, for a new confirmed reservation
// of 2 units at $49.99.
func newOrder(t *testing.T) domain.Order {
	t.Helper()
	d := createDeal(t, dealOpts{stock: 10})
	reservations := postgres.NewReservationRepo(testPool)
	res := newReservation(t, d.ID(), uniqueUser(), "k", 2)
	_, _, err := reservations.Create(t.Context(), res)
	require.NoError(t, err)
	require.NoError(t, res.Confirm(t0))
	require.NoError(t, reservations.UpdateStatus(t.Context(), res))
	price, err := domain.NewMoney(4999, "USD")
	require.NoError(t, err)
	o, err := domain.NewOrder(uniqueID(), res, price, t0)
	require.NoError(t, err)
	return o
}

func countEvents(t *testing.T, aggregateID string) int {
	t.Helper()
	var n int
	require.NoError(t, testPool.QueryRow(t.Context(),
		`SELECT count(*) FROM outbox WHERE aggregate_id = $1`, aggregateID).Scan(&n))
	return n
}

// only keeps the events of the given orders: other tests share the table.
func only(events []usecase.OutboxEvent, orders ...domain.Order) []usecase.OutboxEvent {
	return slices.DeleteFunc(events, func(e usecase.OutboxEvent) bool {
		return !slices.ContainsFunc(orders, func(o domain.Order) bool { return o.ID() == e.AggregateID })
	})
}

// lockAll locks every pending event; the limit only has to exceed what the
// parallel tests can record.
const lockAll = 100000

func TestOrderRepoRecordsEvent(t *testing.T) {
	t.Parallel()
	orders := postgres.NewOrderRepo(testPool)
	txm := postgres.NewTxManager(testPool)

	t.Run("records an order.confirmed event with the order", func(t *testing.T) {
		t.Parallel()
		o := newOrder(t)
		require.NoError(t, orders.Create(t.Context(), o))

		// Read without locking: TestOutboxRepo may hold the row right now.
		var (
			eventType string
			payloadJS []byte
			occurred  time.Time
		)
		require.NoError(t, testPool.QueryRow(t.Context(),
			`SELECT event_type, payload, occurred_at FROM outbox WHERE aggregate_id = $1`, o.ID(),
		).Scan(&eventType, &payloadJS, &occurred))
		require.Equal(t, usecase.EventOrderConfirmed, eventType)
		require.True(t, occurred.Equal(o.CreatedAt()))

		var payload map[string]any
		require.NoError(t, json.Unmarshal(payloadJS, &payload))
		require.Equal(t, map[string]any{
			"order_id": o.ID(), "reservation_id": o.ReservationID(), "deal_id": float64(o.DealID()),
			"user_id": o.UserID(), "quantity": float64(2), "total_cents": float64(9998), "currency": "USD",
			"created_at": "2026-10-15T12:00:00Z",
		}, payload)
	})

	t.Run("records no event when the order's transaction rolls back", func(t *testing.T) {
		t.Parallel()
		o := newOrder(t)
		errAbort := errors.New("abort")

		err := txm.WithinTx(t.Context(), func(ctx context.Context) error {
			require.NoError(t, orders.Create(ctx, o))
			return errAbort
		})

		require.ErrorIs(t, err, errAbort)
		require.Zero(t, countEvents(t, o.ID()))
	})

	t.Run("records no second event for a duplicate order", func(t *testing.T) {
		t.Parallel()
		o := newOrder(t)
		require.NoError(t, orders.Create(t.Context(), o))
		dup, err := domain.RestoreOrder(domain.OrderState{
			ID: uniqueID(), ReservationID: o.ReservationID(), DealID: o.DealID(), UserID: o.UserID(),
			Quantity: o.Quantity(), Total: o.Total(), CreatedAt: o.CreatedAt(),
		})
		require.NoError(t, err)

		require.ErrorIs(t, orders.Create(t.Context(), dup), domain.ErrAlreadyExists)
		require.Equal(t, 1, countEvents(t, o.ID()))
		require.Zero(t, countEvents(t, dup.ID()))
	})
}

func TestOutboxRepo(t *testing.T) {
	t.Parallel()
	orders := postgres.NewOrderRepo(testPool)
	txm := postgres.NewTxManager(testPool)
	queue := postgres.NewOutboxRepo(testPool)

	// Subtests run in order: each locks every pending event, so running them
	// in parallel would make them skip each other's events.
	t.Run("hands out events oldest first and removes them", func(t *testing.T) {
		first, second := newOrder(t), newOrder(t)
		require.NoError(t, orders.Create(t.Context(), first))
		require.NoError(t, orders.Create(t.Context(), second))

		require.NoError(t, txm.WithinTx(t.Context(), func(ctx context.Context) error {
			events, err := queue.LockPending(ctx, lockAll)
			require.NoError(t, err)
			events = only(events, first, second)
			require.Len(t, events, 2)
			require.Equal(t, first.ID(), events[0].AggregateID)
			require.Less(t, events[0].ID, events[1].ID)
			return queue.Remove(ctx, []int64{events[0].ID, events[1].ID})
		}))

		require.Zero(t, countEvents(t, first.ID()))
		require.Zero(t, countEvents(t, second.ID()))
	})

	t.Run("skips events another relay holds", func(t *testing.T) {
		o := newOrder(t)
		require.NoError(t, orders.Create(t.Context(), o))

		require.NoError(t, txm.WithinTx(t.Context(), func(ctx context.Context) error {
			held, err := queue.LockPending(ctx, lockAll)
			require.NoError(t, err)
			require.Len(t, only(held, o), 1)

			// t.Context() carries no transaction, so this is a second relay.
			return txm.WithinTx(t.Context(), func(other context.Context) error {
				seen, err := queue.LockPending(other, lockAll)
				require.NoError(t, err)
				require.Empty(t, only(seen, o), "a held event must not be published twice concurrently")
				return nil
			})
		}))
		require.Equal(t, 1, countEvents(t, o.ID()), "nothing was removed")
	})

	t.Run("reports corrupt state when an event to remove is already gone", func(t *testing.T) {
		err := txm.WithinTx(t.Context(), func(ctx context.Context) error {
			return queue.Remove(ctx, []int64{-1})
		})
		require.ErrorIs(t, err, domain.ErrCorruptState)
	})
}
