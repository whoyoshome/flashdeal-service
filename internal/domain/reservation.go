package domain

import (
	"errors"
	"time"
)

// ReservationStatus is the lifecycle state of a reservation. The values are
// part of the persisted data and must not change.
type ReservationStatus string

// Reservation states. A reservation starts pending and moves exactly once to
// one of the terminal states.
const (
	StatusPending   ReservationStatus = "pending"
	StatusConfirmed ReservationStatus = "confirmed"
	StatusExpired   ReservationStatus = "expired"
	StatusCancelled ReservationStatus = "cancelled"
)

func (s ReservationStatus) valid() bool {
	switch s {
	case StatusPending, StatusConfirmed, StatusExpired, StatusCancelled:
		return true
	}
	return false
}

// ReservationSpec holds what is needed to open a new reservation.
type ReservationSpec struct {
	ID             string
	DealID         int64
	UserID         string
	Quantity       int
	IdempotencyKey string
	ExpiresAt      time.Time
}

// ReservationState is a reservation as persisted, used to rebuild the entity
// from storage.
type ReservationState struct {
	ID             string
	DealID         int64
	UserID         string
	Quantity       int
	Status         ReservationStatus
	IdempotencyKey string
	ExpiresAt      time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Reservation holds units of a deal for a user until it is confirmed,
// cancelled or expires. Its status only changes through Confirm, Cancel and
// Expire, which enforce the allowed transitions.
type Reservation struct {
	id             string
	dealID         int64
	userID         string
	quantity       int
	status         ReservationStatus
	idempotencyKey string
	expiresAt      time.Time
	createdAt      time.Time
	updatedAt      time.Time
}

// NewReservation validates spec and returns a pending reservation created at
// now. The ID comes from an injected generator, never from the domain.
func NewReservation(spec ReservationSpec, now time.Time) (Reservation, error) {
	if err := spec.validate(); err != nil {
		return Reservation{}, err
	}
	if !spec.ExpiresAt.After(now) {
		return Reservation{}, NewValidation("expires_at must be after the creation time")
	}
	now = now.UTC()
	return Reservation{
		id:             spec.ID,
		dealID:         spec.DealID,
		userID:         spec.UserID,
		quantity:       spec.Quantity,
		status:         StatusPending,
		idempotencyKey: spec.IdempotencyKey,
		expiresAt:      spec.ExpiresAt.UTC(),
		createdAt:      now,
		updatedAt:      now,
	}, nil
}

// RestoreReservation rebuilds a reservation from storage. Invariant
// violations are reported as ErrCorruptState.
func RestoreReservation(s ReservationState) (Reservation, error) {
	spec := ReservationSpec{
		ID:             s.ID,
		DealID:         s.DealID,
		UserID:         s.UserID,
		Quantity:       s.Quantity,
		IdempotencyKey: s.IdempotencyKey,
		ExpiresAt:      s.ExpiresAt,
	}
	if err := spec.validate(); err != nil {
		return Reservation{}, corrupt("reservation", s.ID, err)
	}
	if !s.Status.valid() {
		return Reservation{}, corrupt("reservation", s.ID, errors.New("unknown status "+string(s.Status)))
	}
	return Reservation{
		id:             s.ID,
		dealID:         s.DealID,
		userID:         s.UserID,
		quantity:       s.Quantity,
		status:         s.Status,
		idempotencyKey: s.IdempotencyKey,
		expiresAt:      s.ExpiresAt.UTC(),
		createdAt:      s.CreatedAt.UTC(),
		updatedAt:      s.UpdatedAt.UTC(),
	}, nil
}

func (s ReservationSpec) validate() error {
	switch {
	case s.ID == "":
		return NewValidation("reservation id is required")
	case s.DealID <= 0:
		return NewValidation("deal_id must be positive")
	case s.UserID == "":
		return NewValidation("user_id is required")
	case s.Quantity <= 0:
		return NewValidation("quantity must be positive")
	case s.IdempotencyKey == "":
		return NewValidation("idempotency key is required")
	}
	return nil
}

// ID returns the reservation identity.
func (r Reservation) ID() string { return r.id }

// DealID returns the deal the units belong to.
func (r Reservation) DealID() int64 { return r.dealID }

// UserID returns the user holding the units.
func (r Reservation) UserID() string { return r.userID }

// Quantity returns the number of units held.
func (r Reservation) Quantity() int { return r.quantity }

// Status returns the lifecycle state.
func (r Reservation) Status() ReservationStatus { return r.status }

// IdempotencyKey returns the client key that made the request retry-safe.
func (r Reservation) IdempotencyKey() string { return r.idempotencyKey }

// ExpiresAt returns when the hold lapses if not confirmed.
func (r Reservation) ExpiresAt() time.Time { return r.expiresAt }

// CreatedAt returns when the reservation was opened.
func (r Reservation) CreatedAt() time.Time { return r.createdAt }

// UpdatedAt returns when the status last changed.
func (r Reservation) UpdatedAt() time.Time { return r.updatedAt }

// IsExpired reports whether the TTL has elapsed at now, regardless of
// whether the status has caught up yet.
func (r Reservation) IsExpired(now time.Time) bool { return !now.Before(r.expiresAt) }

// Confirm turns a pending, unexpired reservation into a purchase. A
// reservation past its TTL cannot be confirmed even if the expiry worker has
// not processed it yet: its units may already be promised to someone else.
func (r *Reservation) Confirm(now time.Time) error {
	if r.status == StatusExpired {
		return ErrReservationExpired
	}
	if r.status != StatusPending {
		return ErrReservationNotPending
	}
	if r.IsExpired(now) {
		return ErrReservationExpired
	}
	r.transition(StatusConfirmed, now)
	return nil
}

// Cancel releases a pending reservation at the user's request. It is allowed
// even past the TTL while still pending: either way the units return to the
// deal, so the outcome is the same as expiring it.
func (r *Reservation) Cancel(now time.Time) error {
	if r.status != StatusPending {
		return ErrReservationNotPending
	}
	r.transition(StatusCancelled, now)
	return nil
}

// Expire marks a pending reservation whose TTL has elapsed. Calling it early
// is a bug in the caller and returns ErrReservationNotDue.
func (r *Reservation) Expire(now time.Time) error {
	if r.status != StatusPending {
		return ErrReservationNotPending
	}
	if !r.IsExpired(now) {
		return ErrReservationNotDue
	}
	r.transition(StatusExpired, now)
	return nil
}

func (r *Reservation) transition(to ReservationStatus, now time.Time) {
	r.status = to
	r.updatedAt = now.UTC()
}
