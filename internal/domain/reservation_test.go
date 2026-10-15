package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

func TestNewReservation(t *testing.T) {
	t.Parallel()

	t.Run("opens a pending reservation stamped at creation time", func(t *testing.T) {
		t.Parallel()
		spec := validReservationSpec()

		r, err := domain.NewReservation(spec, t0)

		require.NoError(t, err)
		require.Equal(t, domain.StatusPending, r.Status())
		require.Equal(t, spec.ID, r.ID())
		require.Equal(t, spec.DealID, r.DealID())
		require.Equal(t, spec.UserID, r.UserID())
		require.Equal(t, spec.Quantity, r.Quantity())
		require.Equal(t, spec.IdempotencyKey, r.IdempotencyKey())
		require.Equal(t, spec.ExpiresAt, r.ExpiresAt())
		require.Equal(t, t0, r.CreatedAt())
		require.Equal(t, t0, r.UpdatedAt())
	})

	t.Run("stores times in UTC", func(t *testing.T) {
		t.Parallel()
		bogota := time.FixedZone("COT", -5*60*60)
		spec := validReservationSpec()
		spec.ExpiresAt = spec.ExpiresAt.In(bogota)

		r, err := domain.NewReservation(spec, t0.In(bogota))

		require.NoError(t, err)
		require.Equal(t, time.UTC, r.ExpiresAt().Location())
		require.Equal(t, time.UTC, r.CreatedAt().Location())
	})

	tests := []struct {
		name   string
		mutate func(*domain.ReservationSpec)
	}{
		{name: "rejects a missing id", mutate: func(s *domain.ReservationSpec) { s.ID = "" }},
		{name: "rejects a non-positive deal id", mutate: func(s *domain.ReservationSpec) { s.DealID = 0 }},
		{name: "rejects a missing user id", mutate: func(s *domain.ReservationSpec) { s.UserID = "" }},
		{name: "rejects zero quantity", mutate: func(s *domain.ReservationSpec) { s.Quantity = 0 }},
		{name: "rejects a missing idempotency key", mutate: func(s *domain.ReservationSpec) { s.IdempotencyKey = "" }},
		{name: "rejects an expiry equal to the creation time", mutate: func(s *domain.ReservationSpec) { s.ExpiresAt = t0 }},
		{name: "rejects an expiry before the creation time", mutate: func(s *domain.ReservationSpec) { s.ExpiresAt = t0.Add(-time.Second) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := validReservationSpec()
			tc.mutate(&spec)

			_, err := domain.NewReservation(spec, t0)

			requireCode(t, err, domain.CodeValidation)
		})
	}
}

func TestRestoreReservation(t *testing.T) {
	t.Parallel()

	t.Run("rebuilds a reservation in a terminal state", func(t *testing.T) {
		t.Parallel()

		r := reservationIn(t, domain.StatusConfirmed)

		require.Equal(t, domain.StatusConfirmed, r.Status())
		require.Equal(t, t0, r.UpdatedAt())
	})

	t.Run("accepts an expiry in the past, as old rows have", func(t *testing.T) {
		t.Parallel()
		spec := validReservationSpec()

		_, err := domain.RestoreReservation(domain.ReservationState{
			ID: spec.ID, DealID: spec.DealID, UserID: spec.UserID, Quantity: spec.Quantity,
			Status: domain.StatusExpired, IdempotencyKey: spec.IdempotencyKey,
			ExpiresAt: t0.Add(-24 * time.Hour), CreatedAt: t0.Add(-25 * time.Hour), UpdatedAt: t0,
		})

		require.NoError(t, err)
	})

	tests := []struct {
		name   string
		mutate func(*domain.ReservationState)
	}{
		{name: "rejects an unknown status", mutate: func(s *domain.ReservationState) { s.Status = "refunded" }},
		{name: "rejects an empty status", mutate: func(s *domain.ReservationState) { s.Status = "" }},
		{name: "rejects zero quantity", mutate: func(s *domain.ReservationState) { s.Quantity = 0 }},
		{name: "rejects a missing user id", mutate: func(s *domain.ReservationState) { s.UserID = "" }},
	}
	for _, tc := range tests {
		t.Run(tc.name+" as corrupt state, not a client error", func(t *testing.T) {
			t.Parallel()
			spec := validReservationSpec()
			s := domain.ReservationState{
				ID: spec.ID, DealID: spec.DealID, UserID: spec.UserID, Quantity: spec.Quantity,
				Status: domain.StatusPending, IdempotencyKey: spec.IdempotencyKey,
				ExpiresAt: spec.ExpiresAt, CreatedAt: t0, UpdatedAt: t0,
			}
			tc.mutate(&s)

			_, err := domain.RestoreReservation(s)

			require.ErrorIs(t, err, domain.ErrCorruptState)
			_, isDomain := domain.AsDomain(err)
			require.False(t, isDomain, "corrupt data must not surface as a client-facing code")
		})
	}
}

func TestReservationIsExpired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		now  time.Time
		want bool
	}{
		{name: "is not expired before the TTL elapses", now: t0.Add(5*time.Minute - time.Nanosecond), want: false},
		{name: "is expired exactly when the TTL elapses", now: t0.Add(5 * time.Minute), want: true},
		{name: "is expired after the TTL elapses", now: t0.Add(time.Hour), want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, pendingReservation(t).IsExpired(tc.now))
		})
	}
}

type transition func(r *domain.Reservation, now time.Time) error

var (
	confirm transition = (*domain.Reservation).Confirm
	cancel  transition = (*domain.Reservation).Cancel
	expire  transition = (*domain.Reservation).Expire
)

// TestReservationTransitions covers every (state, action) pair: only
// pending -> confirmed | cancelled | expired is allowed.
func TestReservationTransitions(t *testing.T) {
	t.Parallel()

	beforeTTL := t0.Add(time.Minute)
	atTTL := t0.Add(5 * time.Minute)
	afterTTL := t0.Add(10 * time.Minute)

	tests := []struct {
		name       string
		from       domain.ReservationStatus
		action     transition
		now        time.Time
		wantStatus domain.ReservationStatus
		wantErr    error
	}{
		// From pending.
		{name: "confirms a pending reservation before its TTL", from: domain.StatusPending, action: confirm, now: beforeTTL, wantStatus: domain.StatusConfirmed},
		{name: "returns ErrReservationExpired when confirming exactly at the TTL", from: domain.StatusPending, action: confirm, now: atTTL, wantStatus: domain.StatusPending, wantErr: domain.ErrReservationExpired},
		{name: "returns ErrReservationExpired when confirming a pending reservation past its TTL", from: domain.StatusPending, action: confirm, now: afterTTL, wantStatus: domain.StatusPending, wantErr: domain.ErrReservationExpired},
		{name: "cancels a pending reservation before its TTL", from: domain.StatusPending, action: cancel, now: beforeTTL, wantStatus: domain.StatusCancelled},
		{name: "cancels a pending reservation past its TTL", from: domain.StatusPending, action: cancel, now: afterTTL, wantStatus: domain.StatusCancelled},
		{name: "expires a pending reservation exactly at its TTL", from: domain.StatusPending, action: expire, now: atTTL, wantStatus: domain.StatusExpired},
		{name: "expires a pending reservation past its TTL", from: domain.StatusPending, action: expire, now: afterTTL, wantStatus: domain.StatusExpired},
		{name: "returns ErrReservationNotDue when expiring before the TTL", from: domain.StatusPending, action: expire, now: beforeTTL, wantStatus: domain.StatusPending, wantErr: domain.ErrReservationNotDue},

		// From confirmed: terminal.
		{name: "returns ErrReservationNotPending when confirming twice", from: domain.StatusConfirmed, action: confirm, now: beforeTTL, wantStatus: domain.StatusConfirmed, wantErr: domain.ErrReservationNotPending},
		{name: "returns ErrReservationNotPending when cancelling a confirmed reservation", from: domain.StatusConfirmed, action: cancel, now: beforeTTL, wantStatus: domain.StatusConfirmed, wantErr: domain.ErrReservationNotPending},
		{name: "returns ErrReservationNotPending when expiring a confirmed reservation", from: domain.StatusConfirmed, action: expire, now: afterTTL, wantStatus: domain.StatusConfirmed, wantErr: domain.ErrReservationNotPending},

		// From expired: terminal.
		{name: "returns ErrReservationExpired when confirming an expired reservation", from: domain.StatusExpired, action: confirm, now: afterTTL, wantStatus: domain.StatusExpired, wantErr: domain.ErrReservationExpired},
		{name: "returns ErrReservationNotPending when cancelling an expired reservation", from: domain.StatusExpired, action: cancel, now: afterTTL, wantStatus: domain.StatusExpired, wantErr: domain.ErrReservationNotPending},
		{name: "returns ErrReservationNotPending when expiring twice", from: domain.StatusExpired, action: expire, now: afterTTL, wantStatus: domain.StatusExpired, wantErr: domain.ErrReservationNotPending},

		// From cancelled: terminal.
		{name: "returns ErrReservationNotPending when confirming a cancelled reservation", from: domain.StatusCancelled, action: confirm, now: beforeTTL, wantStatus: domain.StatusCancelled, wantErr: domain.ErrReservationNotPending},
		{name: "returns ErrReservationNotPending when cancelling twice", from: domain.StatusCancelled, action: cancel, now: beforeTTL, wantStatus: domain.StatusCancelled, wantErr: domain.ErrReservationNotPending},
		{name: "returns ErrReservationNotPending when expiring a cancelled reservation", from: domain.StatusCancelled, action: expire, now: afterTTL, wantStatus: domain.StatusCancelled, wantErr: domain.ErrReservationNotPending},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := reservationIn(t, tc.from)

			err := tc.action(&r, tc.now)

			require.Equal(t, tc.wantStatus, r.Status())
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				require.Equal(t, t0, r.UpdatedAt(), "a rejected transition must not touch the reservation")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.now, r.UpdatedAt())
		})
	}
}

func TestReservationTransitionErrorCodes(t *testing.T) {
	t.Parallel()

	t.Run("an invalid transition maps to CONFLICT", func(t *testing.T) {
		t.Parallel()
		r := reservationIn(t, domain.StatusCancelled)

		requireCode(t, r.Confirm(t0), domain.CodeConflict)
	})

	t.Run("confirming after the TTL maps to RESERVATION_EXPIRED", func(t *testing.T) {
		t.Parallel()
		r := pendingReservation(t)

		requireCode(t, r.Confirm(t0.Add(time.Hour)), domain.CodeReservationExpired)
	})

	t.Run("expiring too early is a bug and carries no client-facing code", func(t *testing.T) {
		t.Parallel()
		r := pendingReservation(t)

		_, isDomain := domain.AsDomain(r.Expire(t0))
		require.False(t, isDomain)
	})
}
