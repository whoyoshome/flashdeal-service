package usecase_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

func outboxEvents(n int) []usecase.OutboxEvent {
	events := make([]usecase.OutboxEvent, n)
	for i := range events {
		events[i] = usecase.OutboxEvent{
			ID: int64(i + 1), Type: usecase.EventOrderConfirmed, AggregateID: "order",
			Payload: []byte(`{}`), OccurredAt: t0,
		}
	}
	return events
}

func eventIDs(events []usecase.OutboxEvent) []int64 {
	ids := make([]int64, len(events))
	for i, e := range events {
		ids[i] = e.ID
	}
	return ids
}

func TestRelayOutbox(t *testing.T) {
	t.Parallel()

	t.Run("publishes recorded events in order and removes them", func(t *testing.T) {
		t.Parallel()
		outbox := &fakeOutbox{events: outboxEvents(3)}
		pub := &fakePublisher{}

		n, err := usecase.NewRelayOutbox(outbox, pub, &fakeTx{}, 100).Execute(t.Context())

		require.NoError(t, err)
		require.Equal(t, 3, n)
		require.Equal(t, []int64{1, 2, 3}, eventIDs(pub.published))
		require.Equal(t, []int64{1, 2, 3}, outbox.removed)
		require.Empty(t, outbox.events)
	})

	t.Run("drains every batch, one transaction each", func(t *testing.T) {
		t.Parallel()
		outbox := &fakeOutbox{events: outboxEvents(5)}
		pub := &fakePublisher{}
		tx := &fakeTx{}

		n, err := usecase.NewRelayOutbox(outbox, pub, tx, 2).Execute(t.Context())

		require.NoError(t, err)
		require.Equal(t, 5, n)
		require.Equal(t, 3, tx.calls, "batches of 2, 2 and 1")
		require.Equal(t, []int{2, 2, 2}, outbox.lockCalls)
		require.Equal(t, []int64{1, 2, 3, 4, 5}, eventIDs(pub.published))
	})

	t.Run("does nothing when the outbox is empty", func(t *testing.T) {
		t.Parallel()
		pub := &fakePublisher{}

		n, err := usecase.NewRelayOutbox(&fakeOutbox{}, pub, &fakeTx{}, 100).Execute(t.Context())

		require.NoError(t, err)
		require.Zero(t, n)
		require.Empty(t, pub.published)
	})

	t.Run("keeps the events when publishing fails, so the next run retries them", func(t *testing.T) {
		t.Parallel()
		outbox := &fakeOutbox{events: outboxEvents(2)}

		n, err := usecase.NewRelayOutbox(outbox, &fakePublisher{err: errBoom}, &fakeTx{}, 100).Execute(t.Context())

		require.ErrorIs(t, err, errBoom)
		require.Zero(t, n)
		require.Empty(t, outbox.removed)
		require.Len(t, outbox.events, 2)
	})

	t.Run("returns the error when events cannot be locked", func(t *testing.T) {
		t.Parallel()
		pub := &fakePublisher{}

		n, err := usecase.NewRelayOutbox(&fakeOutbox{lockErr: errBoom}, pub, &fakeTx{}, 100).Execute(t.Context())

		require.ErrorIs(t, err, errBoom)
		require.Zero(t, n)
		require.Empty(t, pub.published)
	})

	t.Run("reports nothing published when published events cannot be removed", func(t *testing.T) {
		t.Parallel()
		outbox := &fakeOutbox{events: outboxEvents(2), removeErr: errBoom}

		n, err := usecase.NewRelayOutbox(outbox, &fakePublisher{}, &fakeTx{}, 100).Execute(t.Context())

		require.ErrorIs(t, err, errBoom, "the transaction rolls back and the events are published again")
		require.Zero(t, n)
	})

	t.Run("does nothing when the context is already cancelled", func(t *testing.T) {
		t.Parallel()
		outbox := &fakeOutbox{events: outboxEvents(1)}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		n, err := usecase.NewRelayOutbox(outbox, &fakePublisher{}, &fakeTx{}, 100).Execute(ctx)

		require.NoError(t, err)
		require.Zero(t, n)
		require.Empty(t, outbox.lockCalls)
	})
}
