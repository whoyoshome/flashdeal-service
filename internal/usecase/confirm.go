package usecase

import (
	"context"
	"fmt"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

// ConfirmInput identifies the reservation to confirm and who asks.
type ConfirmInput struct {
	ReservationID string
	UserID        string
}

// Confirm turns a pending reservation into an order after the payment
// gateway accepts the charge (ADR 0014).
type Confirm struct {
	deals        DealReader
	stock        DealStock
	reservations ReservationRepository
	orders       OrderRepository
	payments     PaymentGateway
	tx           TxManager
	clock        Clock
	ids          IDGenerator
}

// NewConfirm wires the confirmation use case.
func NewConfirm(
	deals DealReader, stock DealStock, reservations ReservationRepository,
	orders OrderRepository, payments PaymentGateway, tx TxManager, clock Clock, ids IDGenerator,
) *Confirm {
	return &Confirm{
		deals: deals, stock: stock, reservations: reservations, orders: orders,
		payments: payments, tx: tx, clock: clock, ids: ids,
	}
}

// Execute charges the buyer, then confirms the reservation and creates its
// order in one transaction. The charge runs outside the transaction so a
// slow payment provider never holds a row lock. The reservation row stays
// locked until commit, so a concurrent cancel or expiry waits and then sees
// it is no longer pending.
func (uc *Confirm) Execute(ctx context.Context, in ConfirmInput) (domain.Order, error) {
	res, err := uc.reservations.Get(ctx, in.ReservationID)
	if err != nil {
		return domain.Order{}, fmt.Errorf("load reservation: %w", err)
	}
	if res.UserID() != in.UserID {
		return domain.Order{}, domain.ErrReservationNotFound
	}
	deal, err := uc.deals.GetByID(ctx, res.DealID())
	if err != nil {
		return domain.Order{}, fmt.Errorf("load deal %d: %w", res.DealID(), err)
	}
	total, err := deal.Price().Mul(res.Quantity())
	if err != nil {
		return domain.Order{}, fmt.Errorf("order total: %w", err)
	}
	// Idempotent on reservation ID: a retry after a successful charge and a
	// failed write does not bill the buyer twice (ADR 0014).
	if err := uc.payments.Charge(ctx, PaymentCharge{
		ReservationID: res.ID(),
		UserID:        res.UserID(),
		AmountCents:   total.Amount(),
		Currency:      total.Currency(),
	}); err != nil {
		return domain.Order{}, fmt.Errorf("charge reservation %s: %w", res.ID(), err)
	}

	var order domain.Order
	err = uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		res, err := lockOwnedReservation(ctx, uc.reservations, in.ReservationID, in.UserID)
		if err != nil {
			return err
		}
		now := uc.clock.Now()
		if err := res.Confirm(now); err != nil {
			return err
		}
		if err := uc.reservations.UpdateStatus(ctx, res); err != nil {
			return fmt.Errorf("save confirmation: %w", err)
		}
		if err := uc.stock.CommitReserved(ctx, res); err != nil {
			return fmt.Errorf("commit stock: %w", err)
		}

		id, err := uc.ids.New()
		if err != nil {
			return fmt.Errorf("order id: %w", err)
		}
		order, err = domain.NewOrder(id, res, deal.Price(), now)
		if err != nil {
			return fmt.Errorf("build order: %w", err)
		}
		if err := uc.orders.Create(ctx, order); err != nil {
			return fmt.Errorf("store order: %w", err)
		}
		return nil
	})
	if err != nil {
		// The charge already succeeded. A real provider would void or refund
		// by reservation ID; AlwaysApprove has nothing to undo (ADR 0014).
		return domain.Order{}, err
	}
	return order, nil
}

// lockOwnedReservation loads and locks a reservation, reporting another
// user's reservation as not found so callers cannot probe which IDs exist.
func lockOwnedReservation(ctx context.Context, repo ReservationRepository, id, userID string) (domain.Reservation, error) {
	res, err := repo.GetForUpdate(ctx, id)
	if err != nil {
		return domain.Reservation{}, fmt.Errorf("load reservation: %w", err)
	}
	if res.UserID() != userID {
		return domain.Reservation{}, domain.ErrReservationNotFound
	}
	return res, nil
}
