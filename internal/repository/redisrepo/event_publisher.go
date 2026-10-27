package redisrepo

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

var _ usecase.EventPublisher = (*EventPublisher)(nil)

// EventPublisher appends events to a Redis Stream. Consumers read it with
// XREADGROUP; delivery is at least once, so they drop an event whose
// event_id they have already processed.
type EventPublisher struct {
	rdb    *redis.Client
	stream string
	maxLen int64
}

// NewEventPublisher returns a publisher appending to stream. The stream is
// trimmed to about maxLen entries so it cannot grow without bound when no
// consumer reads it; a consumer further behind than that loses events.
func NewEventPublisher(rdb *redis.Client, stream string, maxLen int64) *EventPublisher {
	return &EventPublisher{rdb: rdb, stream: stream, maxLen: maxLen}
}

// Publish appends events in order, in one round trip.
func (p *EventPublisher) Publish(ctx context.Context, events []usecase.OutboxEvent) error {
	_, err := p.rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for _, e := range events {
			pipe.XAdd(ctx, &redis.XAddArgs{
				Stream: p.stream,
				MaxLen: p.maxLen,
				Approx: true,
				Values: []any{
					"event_id", strconv.FormatInt(e.ID, 10),
					"type", e.Type,
					"aggregate_id", e.AggregateID,
					"occurred_at", e.OccurredAt.UTC().Format(time.RFC3339Nano),
					"payload", string(e.Payload),
				},
			})
		}
		return nil
	})
	if err != nil {
		// Text only, so no go-redis type escapes this package.
		return errors.New("append to stream " + p.stream + ": " + err.Error())
	}
	return nil
}
