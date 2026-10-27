package redisrepo

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

// EventConsumer reads order.confirmed (and later) events from a Redis Stream
// with a consumer group. Delivery is at least once: Claim marks an event
// processed only after the caller's handler returns nil, and Seen reports
// whether this event_id was already handled so duplicates can be dropped
// (ADR 0012).
type EventConsumer struct {
	rdb      *redis.Client
	stream   string
	group    string
	consumer string
	seenTTL  time.Duration
}

// NewEventConsumer returns a consumer for stream in group. consumer names
// this process inside the group. seenTTL bounds how long a processed
// event_id is remembered for deduplication.
func NewEventConsumer(rdb *redis.Client, stream, group, consumer string, seenTTL time.Duration) *EventConsumer {
	return &EventConsumer{rdb: rdb, stream: stream, group: group, consumer: consumer, seenTTL: seenTTL}
}

// EnsureGroup creates the consumer group on the stream if it is missing.
// It is safe to call on every startup.
func (c *EventConsumer) EnsureGroup(ctx context.Context) error {
	err := c.rdb.XGroupCreateMkStream(ctx, c.stream, c.group, "0").Err()
	if err != nil && !isBusyGroup(err) {
		return fmt.Errorf("create consumer group %s on %s: %w", c.group, c.stream, err)
	}
	return nil
}

// Read blocks up to block for up to count new messages for this consumer.
// An empty slice means the wait timed out with nothing to do.
func (c *EventConsumer) Read(ctx context.Context, count int64, block time.Duration) ([]usecase.OutboxEvent, []string, error) {
	streams, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    c.group,
		Consumer: c.consumer,
		Streams:  []string{c.stream, ">"},
		Count:    count,
		Block:    block,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read group %s from %s: %w", c.group, c.stream, err)
	}
	var (
		events []usecase.OutboxEvent
		ids    []string
	)
	for _, s := range streams {
		for _, m := range s.Messages {
			e, err := eventFromStream(m.Values)
			if err != nil {
				return nil, nil, fmt.Errorf("stream entry %s: %w", m.ID, err)
			}
			events = append(events, e)
			ids = append(ids, m.ID)
		}
	}
	return events, ids, nil
}

// Seen reports whether eventID was already marked processed. A duplicate
// delivery should skip the handler and still Ack.
func (c *EventConsumer) Seen(ctx context.Context, eventID int64) (bool, error) {
	n, err := c.rdb.Exists(ctx, c.seenKey(eventID)).Result()
	if err != nil {
		return false, fmt.Errorf("check seen event %d: %w", eventID, err)
	}
	return n > 0, nil
}

// Claim remembers eventID as processed and acknowledges streamID so the
// group does not redeliver it.
func (c *EventConsumer) Claim(ctx context.Context, eventID int64, streamID string) error {
	pipe := c.rdb.TxPipeline()
	pipe.Set(ctx, c.seenKey(eventID), "1", c.seenTTL)
	pipe.XAck(ctx, c.stream, c.group, streamID)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("claim event %d (%s): %w", eventID, streamID, err)
	}
	return nil
}

func (c *EventConsumer) seenKey(eventID int64) string {
	return "flashdeal:events:seen:" + strconv.FormatInt(eventID, 10)
}

func eventFromStream(values map[string]any) (usecase.OutboxEvent, error) {
	id, err := strconv.ParseInt(asString(values["event_id"]), 10, 64)
	if err != nil {
		return usecase.OutboxEvent{}, fmt.Errorf("event_id: %w", err)
	}
	occurred, err := time.Parse(time.RFC3339Nano, asString(values["occurred_at"]))
	if err != nil {
		return usecase.OutboxEvent{}, fmt.Errorf("occurred_at: %w", err)
	}
	return usecase.OutboxEvent{
		ID:          id,
		Type:        asString(values["type"]),
		AggregateID: asString(values["aggregate_id"]),
		Payload:     []byte(asString(values["payload"])),
		OccurredAt:  occurred,
	}, nil
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return fmt.Sprint(v)
	}
}

func isBusyGroup(err error) bool {
	return err != nil && strings.Contains(err.Error(), "BUSYGROUP")
}
