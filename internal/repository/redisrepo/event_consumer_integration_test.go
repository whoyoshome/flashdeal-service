//go:build integration

package redisrepo_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/repository/redisrepo"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

func TestEventConsumer(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	stream := "events:" + uniqueSubject()
	group := "g-" + uniqueSubject()
	pub := redisrepo.NewEventPublisher(testRedis, stream, 1000)
	cons := redisrepo.NewEventConsumer(testRedis, stream, group, "c1", time.Hour)

	require.NoError(t, cons.EnsureGroup(t.Context()))
	require.NoError(t, cons.EnsureGroup(t.Context()), "creating an existing group is a no-op")

	require.NoError(t, pub.Publish(t.Context(), []usecase.OutboxEvent{
		{ID: 11, Type: usecase.EventOrderConfirmed, AggregateID: "ord-1", Payload: []byte(`{"n":1}`), OccurredAt: at},
	}))

	events, ids, err := cons.Read(t.Context(), 10, time.Second)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Len(t, ids, 1)
	require.Equal(t, int64(11), events[0].ID)
	require.Equal(t, "ord-1", events[0].AggregateID)

	seen, err := cons.Seen(t.Context(), 11)
	require.NoError(t, err)
	require.False(t, seen)

	require.NoError(t, cons.Claim(t.Context(), 11, ids[0]))
	seen, err = cons.Seen(t.Context(), 11)
	require.NoError(t, err)
	require.True(t, seen)

	events, _, err = cons.Read(t.Context(), 10, 50*time.Millisecond)
	require.NoError(t, err)
	require.Empty(t, events, "acknowledged messages are not delivered again")
}
