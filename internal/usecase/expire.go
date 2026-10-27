package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

// Expire returns the units of reservations whose TTL elapsed to their deals,
// so abandoned reservations never keep stock locked.
type Expire struct {
	queue     ExpiryQueue
	stock     DealStock
	gate      StockGate
	tx        TxManager
	clock     Clock
	batchSize int
	log       *slog.Logger
}

// NewExpire wires the expiry use case. batchSize bounds how many
// reservations one transaction expires, which keeps row locks short.
func NewExpire(
	queue ExpiryQueue, stock DealStock, gate StockGate,
	tx TxManager, clock Clock, batchSize int, log *slog.Logger,
) *Expire {
	return &Expire{queue: queue, stock: stock, gate: gate, tx: tx, clock: clock, batchSize: batchSize, log: log}
}

// Execute expires every reservation due now, one batch per transaction,
// and returns how many it expired. It stops early when ctx is cancelled;
// whatever is left is picked up by the next run.
func (uc *Expire) Execute(ctx context.Context) (int, error) {
	now := uc.clock.Now()
	total := 0
	for ctx.Err() == nil {
		n, err := uc.expireBatch(ctx, now)
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

func (uc *Expire) expireBatch(ctx context.Context, now time.Time) (int, error) {
	var expired []domain.Reservation
	err := uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		due, err := uc.queue.LockDue(ctx, now, uc.batchSize)
		if err != nil {
			return fmt.Errorf("lock due reservations: %w", err)
		}
		if len(due) == 0 {
			return nil
		}
		for i := range due {
			if err := due[i].Expire(now); err != nil {
				return fmt.Errorf("expire reservation %s: %w", due[i].ID(), err)
			}
		}
		if err := uc.queue.MarkExpired(ctx, due); err != nil {
			return fmt.Errorf("save expirations: %w", err)
		}
		// One call for the batch: the store locks stock in a fixed order, so
		// concurrent workers holding batches over the same deals cannot
		// deadlock.
		if err := uc.stock.ReleaseReserved(ctx, due); err != nil {
			return fmt.Errorf("release stock: %w", err)
		}
		expired = due
		return nil
	})
	if err != nil {
		return 0, err
	}
	uc.releaseGate(ctx, expired)
	return len(expired), nil
}

// releaseGate gives the expired units back to the stock gate. The expiry is
// already committed and PostgreSQL is the source of truth, so a failure here
// is logged and left to the reconciler instead of failing the batch.
func (uc *Expire) releaseGate(ctx context.Context, expired []domain.Reservation) {
	if len(expired) == 0 {
		return
	}
	// Detached so a shutdown between commit and release does not strand the
	// units; one budget covers the whole batch.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), compensationTimeout)
	defer cancel()
	for _, r := range expired {
		if err := uc.gate.Release(ctx, r.DealID(), r.UserID(), r.Quantity()); err != nil {
			uc.log.ErrorContext(ctx, "stock gate release after expiry failed, reconciler will fix it",
				slog.String("reservation_id", r.ID()), slog.Int64("deal_id", r.DealID()), slog.Any("error", err))
		}
	}
}
