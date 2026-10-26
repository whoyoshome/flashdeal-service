package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

// compensationTimeout bounds giving stock back to the gate after a failure.
// It is detached from the request, so it needs its own deadline.
const compensationTimeout = 2 * time.Second

// ReserveInput is a request to hold units of a deal.
type ReserveInput struct {
	DealID         int64
	UserID         string
	Quantity       int
	IdempotencyKey string
}

// Reserve holds units of a deal for a user for a limited time.
type Reserve struct {
	deals        DealReader
	gate         StockGate
	reservations ReservationRepository
	clock        Clock
	ids          IDGenerator
	ttl          time.Duration
	log          *slog.Logger
}

// NewReserve wires the reservation use case. ttl is how long a reservation
// holds its units before it expires.
func NewReserve(
	deals DealReader, gate StockGate, reservations ReservationRepository,
	clock Clock, ids IDGenerator, ttl time.Duration, log *slog.Logger,
) *Reserve {
	return &Reserve{deals: deals, gate: gate, reservations: reservations, clock: clock, ids: ids, ttl: ttl, log: log}
}

// Execute validates the request against the deal, takes the units from the
// stock gate and persists the reservation. If persisting fails, the units
// are given back to the gate. A retry with the same idempotency key returns
// the original reservation.
func (uc *Reserve) Execute(ctx context.Context, in ReserveInput) (domain.Reservation, error) {
	deal, err := uc.deals.GetByID(ctx, in.DealID)
	if err != nil {
		return domain.Reservation{}, fmt.Errorf("load deal %d: %w", in.DealID, err)
	}
	now := uc.clock.Now()
	if err := deal.CanReserve(now, in.Quantity); err != nil {
		return domain.Reservation{}, err
	}

	// Build and validate the reservation before touching the gate, so an
	// invalid request never consumes stock.
	id, err := uc.ids.New()
	if err != nil {
		return domain.Reservation{}, fmt.Errorf("reservation id: %w", err)
	}
	res, err := domain.NewReservation(domain.ReservationSpec{
		ID:             id,
		DealID:         deal.ID(),
		UserID:         in.UserID,
		Quantity:       in.Quantity,
		IdempotencyKey: in.IdempotencyKey,
		ExpiresAt:      now.Add(uc.ttl),
	}, now)
	if err != nil {
		return domain.Reservation{}, err
	}

	if err := uc.gate.TryReserve(ctx, deal.ID(), in.UserID, in.Quantity, deal.PerUserLimit()); err != nil {
		return domain.Reservation{}, fmt.Errorf("stock gate: %w", err)
	}

	stored, created, err := uc.reservations.Create(ctx, res)
	if err != nil {
		uc.compensate(ctx, deal.ID(), in)
		return domain.Reservation{}, fmt.Errorf("persist reservation: %w", err)
	}
	if !created {
		// A retry: the original request already holds the units, so the
		// ones this attempt took from the gate go back.
		uc.compensate(ctx, deal.ID(), in)
	}
	return stored, nil
}

// compensate gives units back to the gate. It must survive the client
// disconnecting, so it runs detached from the request's cancellation. A
// failure is logged here because the caller is already returning another
// outcome; the reconciler repairs any drift.
func (uc *Reserve) compensate(ctx context.Context, dealID int64, in ReserveInput) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), compensationTimeout)
	defer cancel()
	if err := uc.gate.Release(ctx, dealID, in.UserID, in.Quantity); err != nil {
		uc.log.ErrorContext(ctx, "stock compensation failed, reconciler will fix it",
			slog.Int64("deal_id", dealID), slog.String("user_id", in.UserID), slog.Any("error", err))
	}
}
