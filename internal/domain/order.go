package domain

import (
	"errors"
	"fmt"
	"time"
)

// OrderState is an order as persisted, used to rebuild the entity from
// storage.
type OrderState struct {
	ID            string
	ReservationID string
	DealID        int64
	UserID        string
	Quantity      int
	Total         Money
	CreatedAt     time.Time
}

// Order is the purchase produced by confirming a reservation. Payment is out
// of scope: confirmation assumes it was approved.
type Order struct {
	id            string
	reservationID string
	dealID        int64
	userID        string
	quantity      int
	total         Money
	createdAt     time.Time
}

// NewOrder builds the order for a confirmed reservation, pricing it at
// unitPrice. Passing a reservation in any other state is a bug in the caller,
// so that error carries no client-facing code.
func NewOrder(id string, r Reservation, unitPrice Money, now time.Time) (Order, error) {
	if id == "" {
		return Order{}, NewValidation("order id is required")
	}
	if r.Status() != StatusConfirmed {
		return Order{}, fmt.Errorf("new order for reservation %s: status is %s, want %s",
			r.ID(), r.Status(), StatusConfirmed)
	}
	if unitPrice.Amount() <= 0 {
		return Order{}, NewValidation("unit price must be positive")
	}
	total, err := unitPrice.Mul(r.Quantity())
	if err != nil {
		return Order{}, fmt.Errorf("order total: %w", err)
	}
	return Order{
		id:            id,
		reservationID: r.ID(),
		dealID:        r.DealID(),
		userID:        r.UserID(),
		quantity:      r.Quantity(),
		total:         total,
		createdAt:     now.UTC(),
	}, nil
}

// RestoreOrder rebuilds an order from storage. Invariant violations are
// reported as ErrCorruptState.
func RestoreOrder(s OrderState) (Order, error) {
	var err error
	switch {
	case s.ID == "" || s.ReservationID == "" || s.UserID == "":
		err = errors.New("id, reservation_id and user_id are required")
	case s.DealID <= 0:
		err = errors.New("deal_id must be positive")
	case s.Quantity <= 0:
		err = errors.New("quantity must be positive")
	case s.Total.Amount() <= 0:
		err = errors.New("total must be positive")
	}
	if err != nil {
		return Order{}, corrupt("order", s.ID, err)
	}
	return Order{
		id:            s.ID,
		reservationID: s.ReservationID,
		dealID:        s.DealID,
		userID:        s.UserID,
		quantity:      s.Quantity,
		total:         s.Total,
		createdAt:     s.CreatedAt.UTC(),
	}, nil
}

// ID returns the order identity.
func (o Order) ID() string { return o.id }

// ReservationID returns the reservation this order confirmed.
func (o Order) ReservationID() string { return o.reservationID }

// DealID returns the deal the units were bought from.
func (o Order) DealID() int64 { return o.dealID }

// UserID returns the buyer.
func (o Order) UserID() string { return o.userID }

// Quantity returns the number of units bought.
func (o Order) Quantity() int { return o.quantity }

// Total returns the amount charged: unit price times quantity.
func (o Order) Total() Money { return o.total }

// CreatedAt returns when the order was placed.
func (o Order) CreatedAt() time.Time { return o.createdAt }
