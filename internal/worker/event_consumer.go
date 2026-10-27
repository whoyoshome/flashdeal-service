package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

// StreamReader delivers and acknowledges outbox events from a Redis Stream
// consumer group. redisrepo.EventConsumer implements it.
type StreamReader interface {
	EnsureGroup(ctx context.Context) error
	Read(ctx context.Context, count int64, block time.Duration) (events []usecase.OutboxEvent, streamIDs []string, err error)
	Seen(ctx context.Context, eventID int64) (bool, error)
	Claim(ctx context.Context, eventID int64, streamID string) error
}

// EventHandler is called once per new event_id. Returning an error leaves
// the message pending for another delivery.
type EventHandler interface {
	Handle(ctx context.Context, e usecase.OutboxEvent) error
}

// EventConsumerMetrics counts handled events. Optional; may be nil.
type EventConsumerMetrics interface {
	EventsConsumed(n int)
}

// EventConsumer runs a Redis Stream consumer group loop: read, drop
// duplicates by event_id, handle, then claim.
type EventConsumer struct {
	reader  StreamReader
	handler EventHandler
	batch   int64
	block   time.Duration
	metrics EventConsumerMetrics
	tracer  Tracer
	log     *slog.Logger
}

// NewEventConsumer returns a consumer that pulls up to batch events, waiting
// up to block when the stream is idle.
func NewEventConsumer(
	reader StreamReader, handler EventHandler, batch int64, block time.Duration,
	metrics EventConsumerMetrics, tracer Tracer, log *slog.Logger,
) *EventConsumer {
	return &EventConsumer{
		reader: reader, handler: handler, batch: batch, block: block,
		metrics: metrics, tracer: tracer, log: log,
	}
}

// Run creates the group if needed and blocks until ctx is cancelled.
func (c *EventConsumer) Run(ctx context.Context) error {
	if err := c.reader.EnsureGroup(ctx); err != nil {
		return err
	}
	c.log.InfoContext(ctx, "event consumer started",
		slog.Int64("batch", c.batch), slog.String("block", c.block.String()))
	for {
		select {
		case <-ctx.Done():
			// Stopping is the normal end of a worker, not a failure.
			return nil
		default:
		}
		if err := c.tick(ctx); err != nil && ctx.Err() == nil {
			c.log.ErrorContext(ctx, "consume events", slog.Any("error", err))
		}
	}
}

func (c *EventConsumer) tick(ctx context.Context) error {
	ctx, end := c.tracer.StartSpan(ctx, "worker.event_consumer")
	var tickErr error
	defer func() { end(tickErr) }()

	events, ids, err := c.reader.Read(ctx, c.batch, c.block)
	if err != nil {
		tickErr = err
		return err
	}
	handled := 0
	for i, e := range events {
		seen, err := c.reader.Seen(ctx, e.ID)
		if err != nil {
			tickErr = err
			return err
		}
		if !seen {
			if err := c.handler.Handle(ctx, e); err != nil {
				tickErr = err
				return err
			}
			handled++
		}
		if err := c.reader.Claim(ctx, e.ID, ids[i]); err != nil {
			tickErr = err
			return err
		}
	}
	if handled > 0 && c.metrics != nil {
		c.metrics.EventsConsumed(handled)
	}
	return nil
}
