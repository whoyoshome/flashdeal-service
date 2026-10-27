package worker_test

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/usecase"
	"github.com/whoyoshome/flashdeal-service/internal/worker"
)

type fakeStream struct {
	mu      sync.Mutex
	events  []usecase.OutboxEvent
	ids     []string
	seen    map[int64]bool
	claimed []string
	reads   int
}

func (f *fakeStream) EnsureGroup(context.Context) error { return nil }

func (f *fakeStream) Read(_ context.Context, _ int64, block time.Duration) ([]usecase.OutboxEvent, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if len(f.events) == 0 {
		time.Sleep(block)
		return nil, nil, nil
	}
	events, ids := f.events, f.ids
	f.events, f.ids = nil, nil
	return events, ids, nil
}

func (f *fakeStream) Seen(_ context.Context, id int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen[id], nil
}

func (f *fakeStream) Claim(_ context.Context, id int64, streamID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seen == nil {
		f.seen = map[int64]bool{}
	}
	f.seen[id] = true
	f.claimed = append(f.claimed, streamID)
	return nil
}

type recordingHandler struct {
	mu     sync.Mutex
	events []usecase.OutboxEvent
}

func (h *recordingHandler) Handle(_ context.Context, e usecase.OutboxEvent) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, e)
	return nil
}

func TestEventConsumer(t *testing.T) {
	t.Parallel()

	t.Run("handles a new event once and claims it", func(t *testing.T) {
		t.Parallel()
		stream := &fakeStream{
			events: []usecase.OutboxEvent{{ID: 9, Type: usecase.EventOrderConfirmed, AggregateID: "o1"}},
			ids:    []string{"1-0"},
			seen:   map[int64]bool{},
		}
		handler := &recordingHandler{}
		metrics := &fakeMetrics{}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			done <- worker.NewEventConsumer(stream, handler, 10, 10*time.Millisecond, metrics, &fakeTracer{}, slog.Default()).Run(ctx)
		}()

		require.Eventually(t, func() bool {
			handler.mu.Lock()
			defer handler.mu.Unlock()
			return len(handler.events) == 1
		}, time.Second, 5*time.Millisecond)
		cancel()
		require.NoError(t, <-done)
		require.Equal(t, []string{"1-0"}, stream.claimed)
		require.EqualValues(t, 1, metrics.events.Load())
	})

	t.Run("skips the handler for a duplicate event_id but still claims", func(t *testing.T) {
		t.Parallel()
		stream := &fakeStream{
			events: []usecase.OutboxEvent{{ID: 9, Type: usecase.EventOrderConfirmed, AggregateID: "o1"}},
			ids:    []string{"2-0"},
			seen:   map[int64]bool{9: true},
		}
		handler := &recordingHandler{}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			done <- worker.NewEventConsumer(stream, handler, 10, 10*time.Millisecond, nil, &fakeTracer{}, slog.Default()).Run(ctx)
		}()

		require.Eventually(t, func() bool {
			stream.mu.Lock()
			defer stream.mu.Unlock()
			return len(stream.claimed) == 1
		}, time.Second, 5*time.Millisecond)
		cancel()
		require.NoError(t, <-done)
		require.Empty(t, handler.events)
	})
}

func TestLoggingHandler(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	require.NoError(t, worker.NewLoggingHandler(log).Handle(t.Context(), usecase.OutboxEvent{
		ID: 3, Type: usecase.EventOrderConfirmed, AggregateID: "ord-1", OccurredAt: time.Unix(0, 0).UTC(),
	}))
	require.Contains(t, buf.String(), "event_id=3")
	require.Contains(t, buf.String(), "aggregate_id=ord-1")
}
