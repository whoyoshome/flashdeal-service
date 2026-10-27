package usecase

import (
	"context"
	"fmt"
)

// RelayOutbox publishes the events recorded in the outbox and removes them
// once published. Delivery is at least once: a crash or a failed commit
// after publishing leaves the events in place, and the next pass publishes
// them again, so consumers drop duplicates by event ID.
type RelayOutbox struct {
	queue     OutboxQueue
	publisher EventPublisher
	tx        TxManager
	batchSize int
}

// NewRelayOutbox wires the outbox relay. batchSize bounds how many events one
// transaction locks and publishes.
func NewRelayOutbox(queue OutboxQueue, publisher EventPublisher, tx TxManager, batchSize int) *RelayOutbox {
	return &RelayOutbox{queue: queue, publisher: publisher, tx: tx, batchSize: batchSize}
}

// Execute publishes every recorded event, one batch per transaction, and
// returns how many it published. It stops early when ctx is cancelled;
// whatever is left is picked up by the next run.
func (uc *RelayOutbox) Execute(ctx context.Context) (int, error) {
	total := 0
	for ctx.Err() == nil {
		n, err := uc.relayBatch(ctx)
		total += n
		if err != nil {
			return total, err
		}
		if n < uc.batchSize {
			break
		}
	}
	return total, nil
}

func (uc *RelayOutbox) relayBatch(ctx context.Context) (int, error) {
	var published int
	err := uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		events, err := uc.queue.LockPending(ctx, uc.batchSize)
		if err != nil {
			return fmt.Errorf("lock outbox events: %w", err)
		}
		if len(events) == 0 {
			return nil
		}
		// Published while the rows are locked, so another relay cannot
		// publish the same batch concurrently.
		if err := uc.publisher.Publish(ctx, events); err != nil {
			return fmt.Errorf("publish %d events: %w", len(events), err)
		}
		ids := make([]int64, len(events))
		for i, e := range events {
			ids[i] = e.ID
		}
		if err := uc.queue.Remove(ctx, ids); err != nil {
			return fmt.Errorf("remove published events: %w", err)
		}
		published = len(events)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return published, nil
}
