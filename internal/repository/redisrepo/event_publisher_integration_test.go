//go:build integration

package redisrepo_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/repository/redisrepo"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

func TestEventPublisher(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

	t.Run("appends every event in order with its fields", func(t *testing.T) {
		t.Parallel()
		stream := "events:" + uniqueSubject()
		pub := redisrepo.NewEventPublisher(testRedis, stream, 1000)

		err := pub.Publish(t.Context(), []usecase.OutboxEvent{
			{ID: 7, Type: usecase.EventOrderConfirmed, AggregateID: "order-a", Payload: []byte(`{"n":1}`), OccurredAt: at},
			{ID: 8, Type: usecase.EventOrderConfirmed, AggregateID: "order-b", Payload: []byte(`{"n":2}`), OccurredAt: at},
		})
		require.NoError(t, err)

		entries, err := testRedis.XRange(t.Context(), stream, "-", "+").Result()
		require.NoError(t, err)
		require.Len(t, entries, 2)
		require.Equal(t, map[string]any{
			"event_id": "7", "type": "order.confirmed", "aggregate_id": "order-a",
			"occurred_at": "2026-10-15T12:00:00Z", "payload": `{"n":1}`,
		}, entries[0].Values)
		require.Equal(t, "8", entries[1].Values["event_id"])
	})

	t.Run("trims the stream to about its maximum length", func(t *testing.T) {
		t.Parallel()
		stream := "events:" + uniqueSubject()
		pub := redisrepo.NewEventPublisher(testRedis, stream, 10)
		events := make([]usecase.OutboxEvent, 1000)
		for i := range events {
			events[i] = usecase.OutboxEvent{ID: int64(i + 1), Type: "t", AggregateID: "a", Payload: []byte(`{}`), OccurredAt: at}
		}

		require.NoError(t, pub.Publish(t.Context(), events))

		n, err := testRedis.XLen(t.Context(), stream).Result()
		require.NoError(t, err)
		require.Less(t, n, int64(len(events)), "approximate trimming drops whole nodes, never all of them")
		last, err := testRedis.XRevRangeN(t.Context(), stream, "+", "-", 1).Result()
		require.NoError(t, err)
		require.Equal(t, "1000", last[0].Values["event_id"], "the newest events are kept")
	})
}
