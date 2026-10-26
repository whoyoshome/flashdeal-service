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

// Confirm turns a pending reservation into an order. Payment is assumed
// approved (out of scope).
type Confirm struct {
	deals        DealReader
	stock        DealStock
	reservations ReservationRepository
	orders       OrderRepository
	tx           TxManager
	clock        Clock
	ids          IDGenerator
}

// NewConfirm wires the confirmation use case.
func NewConfirm(
	deals DealReader, stock DealStock, reservations ReservationRepository,
	orders OrderRepository, tx TxManager, clock Clock, ids IDGenerator,
) *Confirm {
	return &Confirm{deals: deals, stock: stock, reservations: reservations, orders: orders, tx: tx, clock: clock, ids: ids}
}

// Execute confirms the reservation and creates its order in one
// transaction. The reservation row stays locked until commit, so a
// concurrent cancel or expiry waits and then sees it is no longer pending.
func (uc *Confirm) Execute(ctx context.Context, in ConfirmInput) (domain.Order, error) {
	var order domain.Order
	err := uc.tx.WithinTx(ctx, func(ctx context.Context) error {
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
		if err := uc.stock.CommitReserved(ctx, res.DealID(), res.Quantity()); err != nil {
			return fmt.Errorf("commit stock: %w", err)
		}

		deal, err := uc.deals.GetByID(ctx, res.DealID())
		if err != nil {
			return fmt.Errorf("load deal %d: %w", res.DealID(), err)
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
