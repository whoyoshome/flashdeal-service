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

// Clock tells the current time. Injected so time-dependent rules (windows,
// TTLs) are testable without sleeping.
type Clock interface {
	Now() time.Time
}

// IDGenerator creates identifiers for new reservations and orders.
type IDGenerator interface {
	New() (string, error)
}

// StockGate admits or rejects reservations before they reach PostgreSQL,
// so most rejections never touch the database. It is the first line of
// defense; PostgreSQL constraints remain the source of truth.
type StockGate interface {
	// Load opens a deal's stock for reservations.
	Load(ctx context.Context, d domain.Deal) error
	// TryReserve takes qty units for userID, enforcing perUserLimit. It
	// returns domain.ErrSoldOut, domain.ErrUserLimitReached or, when the
	// gate is unreachable, domain.ErrStockUnavailable.
	TryReserve(ctx context.Context, dealID int64, userID string, qty, perUserLimit int) error
	// Release gives back units taken by TryReserve.
	Release(ctx context.Context, dealID int64, userID string, qty int) error
}

// IdempotencyState says what an idempotency key already stands for.
type IdempotencyState int

// Idempotency key states.
const (
	// IdempotencyNew means the caller now owns the key and must run the
	// request, then Complete or Release it.
	IdempotencyNew IdempotencyState = iota
	// IdempotencyInProgress means another request holds the key right now.
	IdempotencyInProgress
	// IdempotencyDone means the key has a stored final response.
	IdempotencyDone
)

// IdempotencyClaim is the outcome of claiming an idempotency key.
type IdempotencyClaim struct {
	State IdempotencyState
	// Fingerprint identifies the request that owns the key, so a key reused
	// for a different request can be refused.
	Fingerprint string
	// Status and Body hold the stored response when State is IdempotencyDone.
	Status int
	Body   []byte
}

// IdempotencyRef names an idempotency key. Keys are scoped per user, so one
// client can never replay another's response.
type IdempotencyRef struct {
	UserID string
	Key    string
}

// IdempotencyStore remembers the outcome of requests by idempotency key so a
// retry is answered with the original response instead of running again.
type IdempotencyStore interface {
	// Begin atomically claims ref for the request identified by fingerprint
	// and token, holding the claim for lockTTL. If the key is already claimed
	// or done it returns that state and claims nothing.
	Begin(ctx context.Context, ref IdempotencyRef, fingerprint, token string, lockTTL time.Duration) (IdempotencyClaim, error)
	// Complete stores the final response for ref for ttl.
	Complete(ctx context.Context, ref IdempotencyRef, fingerprint string, status int, body []byte, ttl time.Duration) error
	// Release drops the in-progress claim made with token, so a retry runs
	// the request again. Another request's claim is left untouched.
	Release(ctx context.Context, ref IdempotencyRef, token string) error
}

// DealReader loads a single deal. It is split from DealRepository so the
// reservation hot path can be served by a cache later.
type DealReader interface {
	// GetByID returns domain.ErrDealNotFound when the deal does not exist.
	GetByID(ctx context.Context, id int64) (domain.Deal, error)
}

// DealRepository stores and reads deals.
type DealRepository interface {
	DealReader
	// Create persists a new deal and returns it with its assigned ID.
	Create(ctx context.Context, d domain.Deal) (domain.Deal, error)
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

// ExpiryQueue hands out reservations whose TTL has elapsed so their units
// can return to the deal.
type ExpiryQueue interface {
	// LockDue returns up to limit pending reservations whose TTL elapsed at
	// now, oldest first, locked until the surrounding transaction ends. Rows
	// another transaction holds are skipped, so workers on any number of
	// replicas take disjoint batches without coordinating.
	LockDue(ctx context.Context, now time.Time, limit int) ([]domain.Reservation, error)
	// MarkExpired persists the transitions made by Reservation.Expire. It
	// returns domain.ErrReservationNotPending if any of them is no longer
	// pending in storage.
	MarkExpired(ctx context.Context, rs []domain.Reservation) error
}

// OrderRepository stores orders.
type OrderRepository interface {
	// Create persists o; it returns domain.ErrAlreadyExists when the
	// reservation already has an order.
	Create(ctx context.Context, o domain.Order) error
}
