package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

var _ usecase.OutboxQueue = (*OutboxRepo)(nil)

const (
	// SKIP LOCKED lets relays on every replica take disjoint batches. Events
	// are deleted once published, so the primary key index alone serves the
	// scan.
	selectPendingEvents = `SELECT id, event_type, aggregate_id, payload, occurred_at FROM outbox
ORDER BY id
LIMIT $1
FOR UPDATE SKIP LOCKED`

	deleteEvents = `DELETE FROM outbox WHERE id = ANY($1)`
)

// OutboxRepo reads and removes outbox events.
type OutboxRepo struct {
	pool *pgxpool.Pool
}

// NewOutboxRepo returns an OutboxRepo backed by pool.
func NewOutboxRepo(pool *pgxpool.Pool) *OutboxRepo {
	return &OutboxRepo{pool: pool}
}

// LockPending returns up to limit events, oldest first, locked until the
// surrounding transaction ends.
func (r *OutboxRepo) LockPending(ctx context.Context, limit int) ([]usecase.OutboxEvent, error) {
	rows, err := db(ctx, r.pool).Query(ctx, selectPendingEvents, limit)
	if err != nil {
		return nil, fmt.Errorf("query outbox: %w", err)
	}
	defer rows.Close()
	var events []usecase.OutboxEvent
	for rows.Next() {
		var row outboxRow
		if err := rows.Scan(row.scanTargets()...); err != nil {
			return nil, fmt.Errorf("scan outbox event: %w", err)
		}
		events = append(events, row.toEvent())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read outbox: %w", err)
	}
	return events, nil
}

// Remove deletes the events with the given IDs. They are locked by the
// caller's transaction, so one already gone means the state is corrupt.
func (r *OutboxRepo) Remove(ctx context.Context, ids []int64) error {
	tag, err := db(ctx, r.pool).Exec(ctx, deleteEvents, ids)
	if err != nil {
		return fmt.Errorf("delete outbox events: %w", err)
	}
	if tag.RowsAffected() != int64(len(ids)) {
		return fmt.Errorf("delete outbox events: %d of %d deleted: %w", tag.RowsAffected(), len(ids), domain.ErrCorruptState)
	}
	return nil
}
