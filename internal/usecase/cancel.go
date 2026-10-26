package usecase

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

// CancelInput identifies the reservation to cancel and who asks.
type CancelInput struct {
	ReservationID string
	UserID        string
}

// Cancel releases a pending reservation at the user's request.
type Cancel struct {
	stock        DealStock
	reservations ReservationRepository
	gate         StockGate
	tx           TxManager
	clock        Clock
	log          *slog.Logger
}

// NewCancel wires the cancellation use case.
func NewCancel(
	stock DealStock, reservations ReservationRepository, gate StockGate,
	tx TxManager, clock Clock, log *slog.Logger,
) *Cancel {
	return &Cancel{stock: stock, reservations: reservations, gate: gate, tx: tx, clock: clock, log: log}
}

// Execute cancels the reservation and returns its units to the deal in one
// transaction, then gives them back to the stock gate.
func (uc *Cancel) Execute(ctx context.Context, in CancelInput) error {
	var res domain.Reservation
	err := uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		var err error
		res, err = lockOwnedReservation(ctx, uc.reservations, in.ReservationID, in.UserID)
		if err != nil {
			return err
		}
		if err := res.Cancel(uc.clock.Now()); err != nil {
			return err
		}
		if err := uc.reservations.UpdateStatus(ctx, res); err != nil {
			return fmt.Errorf("save cancellation: %w", err)
		}
		if err := uc.stock.ReleaseReserved(ctx, res.DealID(), res.Quantity()); err != nil {
			return fmt.Errorf("release stock: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// The cancellation is committed and PostgreSQL is the source of truth,
	// so a gate failure must not fail the request: it is logged and the
	// reconciler (phase 5) restores the gate from PostgreSQL.
	gateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), compensationTimeout)
	defer cancel()
	if err := uc.gate.Release(gateCtx, res.DealID(), res.UserID(), res.Quantity()); err != nil {
		uc.log.ErrorContext(gateCtx, "stock gate release after cancel failed, reconciler will fix it",
			slog.String("reservation_id", res.ID()), slog.Int64("deal_id", res.DealID()), slog.Any("error", err))
	}
	return nil
}
