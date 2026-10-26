package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

const reservationTTL = 5 * time.Minute

type reserveDeps struct {
	deals        *fakeDeals
	gate         *fakeGate
	reservations *fakeReservations
	ids          *fakeIDs
	clock        *fakeClock
}

func newReserveDeps(t *testing.T) *reserveDeps {
	t.Helper()
	return &reserveDeps{
		deals:        newFakeDeals(activeDeal(t)),
		gate:         &fakeGate{},
		reservations: newFakeReservations(),
		ids:          &fakeIDs{},
		clock:        &fakeClock{now: t0},
	}
}

func (d *reserveDeps) useCase() *usecase.Reserve {
	return usecase.NewReserve(d.deals, d.gate, d.reservations, d.clock, d.ids, reservationTTL, discardLog)
}

func validReserveInput() usecase.ReserveInput {
	return usecase.ReserveInput{DealID: 7, UserID: "user-1", Quantity: 2, IdempotencyKey: "key-1"}
}

func TestReserve(t *testing.T) {
	t.Parallel()

	t.Run("reserves through the gate and persists a pending reservation with the TTL", func(t *testing.T) {
		t.Parallel()
		deps := newReserveDeps(t)

		res, err := deps.useCase().Execute(t.Context(), validReserveInput())

		require.NoError(t, err)
		require.Equal(t, domain.StatusPending, res.Status())
		require.Equal(t, int64(7), res.DealID())
		require.Equal(t, "user-1", res.UserID())
		require.Equal(t, 2, res.Quantity())
		require.Equal(t, t0.Add(reservationTTL), res.ExpiresAt())
		require.Equal(t, []gateCall{{dealID: 7, userID: "user-1", qty: 2, limit: 2}}, deps.gate.reserves)
		require.Len(t, deps.reservations.created, 1)
		require.Empty(t, deps.gate.releases)
	})

	rejectedBeforeGate := []struct {
		name    string
		mutate  func(*reserveDeps, *usecase.ReserveInput)
		wantErr error
		code    domain.Code
	}{
		{
			name:    "returns ErrDealNotFound for an unknown deal",
			mutate:  func(_ *reserveDeps, in *usecase.ReserveInput) { in.DealID = 404 },
			wantErr: domain.ErrDealNotFound,
		},
		{
			name:    "returns ErrDealNotActive outside the deal window",
			mutate:  func(d *reserveDeps, _ *usecase.ReserveInput) { d.clock.now = t0.Add(2 * time.Hour) },
			wantErr: domain.ErrDealNotActive,
		},
		{
			name:    "returns ErrUserLimitReached above the per-user limit",
			mutate:  func(_ *reserveDeps, in *usecase.ReserveInput) { in.Quantity = 3 },
			wantErr: domain.ErrUserLimitReached,
		},
		{
			name:   "returns a validation error for a zero quantity",
			mutate: func(_ *reserveDeps, in *usecase.ReserveInput) { in.Quantity = 0 },
			code:   domain.CodeValidation,
		},
		{
			name:   "returns a validation error without an idempotency key",
			mutate: func(_ *reserveDeps, in *usecase.ReserveInput) { in.IdempotencyKey = "" },
			code:   domain.CodeValidation,
		},
		{
			name:   "returns a validation error without a user",
			mutate: func(_ *reserveDeps, in *usecase.ReserveInput) { in.UserID = "" },
			code:   domain.CodeValidation,
		},
	}
	for _, tc := range rejectedBeforeGate {
		t.Run(tc.name+" without consuming stock", func(t *testing.T) {
			t.Parallel()
			deps := newReserveDeps(t)
			in := validReserveInput()
			tc.mutate(deps, &in)

			_, err := deps.useCase().Execute(t.Context(), in)

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				de, ok := domain.AsDomain(err)
				require.True(t, ok)
				require.Equal(t, tc.code, de.Code)
			}
			require.Empty(t, deps.gate.reserves)
			require.Empty(t, deps.reservations.created)
		})
	}

	t.Run("returns the id generator error without consuming stock", func(t *testing.T) {
		t.Parallel()
		deps := newReserveDeps(t)
		deps.ids.err = errors.New("entropy exhausted")

		_, err := deps.useCase().Execute(t.Context(), validReserveInput())

		require.ErrorIs(t, err, deps.ids.err)
		require.Empty(t, deps.gate.reserves)
	})

	for _, gateErr := range []error{domain.ErrSoldOut, domain.ErrUserLimitReached, domain.ErrStockUnavailable} {
		t.Run("returns "+gateErr.Error()+" from the gate without persisting", func(t *testing.T) {
			t.Parallel()
			deps := newReserveDeps(t)
			deps.gate.reserveErr = gateErr

			_, err := deps.useCase().Execute(t.Context(), validReserveInput())

			require.ErrorIs(t, err, gateErr)
			require.Empty(t, deps.reservations.created)
			require.Empty(t, deps.gate.releases, "nothing was taken, so nothing is given back")
		})
	}

	t.Run("gives the units back to the gate when persisting fails", func(t *testing.T) {
		t.Parallel()
		deps := newReserveDeps(t)
		deps.reservations.createErr = domain.ErrSoldOut

		_, err := deps.useCase().Execute(t.Context(), validReserveInput())

		require.ErrorIs(t, err, domain.ErrSoldOut)
		require.Equal(t, []gateCall{{dealID: 7, userID: "user-1", qty: 2}}, deps.gate.releases)
	})

	t.Run("compensates with a live context even after the client disconnected", func(t *testing.T) {
		t.Parallel()
		deps := newReserveDeps(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel() // the request context is already dead when compensation runs
		deps.reservations.createErr = errors.New("connection reset")

		_, err := deps.useCase().Execute(ctx, validReserveInput())

		require.Error(t, err)
		require.Len(t, deps.gate.releases, 1)
		require.NoError(t, deps.gate.releases[0].ctxErr)
	})

	t.Run("still returns the persistence error when compensation fails", func(t *testing.T) {
		t.Parallel()
		deps := newReserveDeps(t)
		deps.reservations.createErr = domain.ErrSoldOut
		deps.gate.releaseErr = domain.ErrStockUnavailable

		_, err := deps.useCase().Execute(t.Context(), validReserveInput())

		require.ErrorIs(t, err, domain.ErrSoldOut)
		require.NotErrorIs(t, err, domain.ErrStockUnavailable)
	})

	t.Run("returns the stored reservation on an idempotent retry and gives back the extra units", func(t *testing.T) {
		t.Parallel()
		deps := newReserveDeps(t)
		original := storedReservation(t, domain.StatusPending)
		deps.reservations.replayOf = &original

		res, err := deps.useCase().Execute(t.Context(), validReserveInput())

		require.NoError(t, err)
		require.Equal(t, original.ID(), res.ID())
		require.Equal(t, []gateCall{{dealID: 7, userID: "user-1", qty: 2}}, deps.gate.releases)
	})
}
