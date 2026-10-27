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

// DealStock moves a reservation's units between a deal's reserved and sold
// counters with conditional updates, so concurrent replicas can never break
// the totals. It takes reservations rather than bare quantities because the
// units must move on the same stock they were reserved from.
type DealStock interface {
	// CommitReserved moves r's units from reserved to sold (confirmation).
	CommitReserved(ctx context.Context, r domain.Reservation) error
	// ReleaseReserved returns the units of every reservation in rs to their
	// deals (cancellation or expiry), locking stock in a fixed order so
	// concurrent callers cannot deadlock.
	ReleaseReserved(ctx context.Context, rs []domain.Reservation) error
}

// ReservationRepository stores reservations.
type ReservationRepository interface {
	// Create inserts r and reserves its units on the deal atomically. It
	// returns domain.ErrSoldOut when the deal lacks stock and
	// domain.ErrDealNotFound when the deal does not exist. When the user
	// already sent the same idempotency key for the same request it returns
	// the stored reservation with created false and reserves nothing.
	Create(ctx context.Context, r domain.Reservation) (stored domain.Reservation, created bool, err error)
	// Get loads a reservation without locking it. It returns
	// domain.ErrReservationNotFound when absent.
	Get(ctx context.Context, id string) (domain.Reservation, error)
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

// GateState is what the stock gate should hold for a deal according to
// PostgreSQL: the units still available and the units each buyer holds.
type GateState struct {
	Available int
	// Held maps a user ID to the units of their pending and confirmed
	// reservations. Users holding nothing are absent.
	Held map[string]int
}

// StockLedger reads, from the source of truth, the gate state a deal
// should have.
type StockLedger interface {
	// GateState returns the deal's available units and per-buyer holdings,
	// both read from one snapshot so they agree with each other. It returns
	// domain.ErrDealNotFound when the deal does not exist.
	GateState(ctx context.Context, dealID int64) (GateState, error)
}

// GateRepair inspects and repairs the stock gate when it has drifted from
// PostgreSQL, for example after a release that failed.
type GateRepair interface {
	// Stock returns the units the gate has left for dealID. loaded is false
	// when the deal's stock is not in the gate (never activated or expired).
	Stock(ctx context.Context, dealID int64) (stock int, loaded bool, err error)
	// Restore replaces the deal's stock and per-buyer holdings with want, but
	// only if the gate still holds expected units, so a reservation taken in
	// the meantime is never overwritten. It reports whether it applied.
	Restore(ctx context.Context, dealID int64, expected int, want GateState) (bool, error)
}

// OrderRepository stores orders.
type OrderRepository interface {
	// Create persists o together with its EventOrderConfirmed outbox event,
	// atomically, so no order exists without its event. It returns
	// domain.ErrAlreadyExists when the reservation already has an order.
	Create(ctx context.Context, o domain.Order) error
}

// EventOrderConfirmed is the type of the event recorded for every order.
const EventOrderConfirmed = "order.confirmed"

// OutboxEvent is an event recorded in the same transaction as the change it
// describes and not yet published. Payload is the encoded event body, opaque
// to the use cases.
type OutboxEvent struct {
	// ID orders events and lets consumers drop a redelivered one.
	ID          int64
	Type        string
	AggregateID string
	Payload     []byte
	OccurredAt  time.Time
}

// OutboxQueue hands out recorded events for publishing.
type OutboxQueue interface {
	// LockPending returns up to limit events, oldest first, locked until the
	// surrounding transaction ends. Events another transaction holds are
	// skipped, so relays on any number of replicas take disjoint batches.
	LockPending(ctx context.Context, limit int) ([]OutboxEvent, error)
	// Remove deletes published events. It returns domain.ErrCorruptState if
	// any of them is already gone.
	Remove(ctx context.Context, ids []int64) error
}

// EventPublisher delivers events to consumers outside the service.
type EventPublisher interface {
	// Publish delivers every event in events, in order. On error some of
	// them may have been delivered.
	Publish(ctx context.Context, events []OutboxEvent) error
}

// PaymentCharge is what Confirm asks the payment gateway to take before it
// writes the order (ADR 0014).
type PaymentCharge struct {
	ReservationID string
	UserID        string
	AmountCents   int64
	Currency      string
}

// PaymentGateway charges the buyer for a reservation. Confirm calls it
// outside the database transaction so a slow provider never holds a row
// lock. Implementations must be idempotent on ReservationID.
type PaymentGateway interface {
	// Charge takes the payment for the reservation. A retry with the same
	// ReservationID must not charge again. It returns domain.ErrPaymentDeclined
	// when the provider refuses the charge.
	Charge(ctx context.Context, in PaymentCharge) error
}
