package usecase

import (
	"context"
	"time"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

// Ports are declared here, next to the use cases that consume them, and
// implemented by adapters in internal/repository. Keep each one small.

// TxManager runs fn inside a database transaction. The transaction travels
// in the context passed to fn, so repositories called with that context join
// it; nested calls join the outer transaction instead of opening a new one.
// Returning an error from fn rolls everything back.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// DealRepository stores and reads deals.
type DealRepository interface {
	// Create persists a new deal and returns it with its assigned ID.
	Create(ctx context.Context, d domain.Deal) (domain.Deal, error)
	// GetByID returns domain.ErrDealNotFound when the deal does not exist.
	GetByID(ctx context.Context, id int64) (domain.Deal, error)
	// ListActive returns deals whose window contains now, ordered by ID,
	// starting after the afterID cursor (0 for the first page).
	ListActive(ctx context.Context, now time.Time, afterID int64, limit int) ([]domain.Deal, error)
}

// DealStock moves units between a deal's reserved and sold counters with
// conditional updates, so concurrent replicas can never break the totals.
type DealStock interface {
	// CommitReserved moves qty units from reserved to sold (confirmation).
	CommitReserved(ctx context.Context, dealID int64, qty int) error
	// ReleaseReserved returns qty reserved units to the deal (cancellation
	// or expiry).
	ReleaseReserved(ctx context.Context, dealID int64, qty int) error
}

// ReservationRepository stores reservations.
type ReservationRepository interface {
	// Create inserts r and reserves its units on the deal atomically. It
	// returns domain.ErrSoldOut when the deal lacks stock and
	// domain.ErrDealNotFound when the deal does not exist. When the user
	// already sent the same idempotency key for the same request it returns
	// the stored reservation with created false and reserves nothing.
	Create(ctx context.Context, r domain.Reservation) (stored domain.Reservation, created bool, err error)
	// GetForUpdate loads a reservation and locks it until the surrounding
	// transaction ends. It returns domain.ErrReservationNotFound when absent.
	GetForUpdate(ctx context.Context, id string) (domain.Reservation, error)
	// UpdateStatus persists a status change made by a domain transition. It
	// only applies to a reservation still pending in storage and returns
	// domain.ErrReservationNotPending otherwise.
	UpdateStatus(ctx context.Context, r domain.Reservation) error
}

// OrderRepository stores orders.
type OrderRepository interface {
	// Create persists o; it returns domain.ErrAlreadyExists when the
	// reservation already has an order.
	Create(ctx context.Context, o domain.Order) error
}
