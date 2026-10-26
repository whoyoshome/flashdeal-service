package usecase_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

type flowDeps struct {
	deals        *fakeDeals
	reservations *fakeReservations
	orders       *fakeOrders
	gate         *fakeGate
	tx           *fakeTx
	clock        *fakeClock
	ids          *fakeIDs
}

func newFlowDeps(t *testing.T, status domain.ReservationStatus) *flowDeps {
	t.Helper()
	return &flowDeps{
		deals:        newFakeDeals(activeDeal(t)),
		reservations: newFakeReservations(storedReservation(t, status)),
		orders:       &fakeOrders{},
		gate:         &fakeGate{},
		tx:           &fakeTx{},
		clock:        &fakeClock{now: t0.Add(time.Minute)},
		ids:          &fakeIDs{},
	}
}

func (d *flowDeps) confirm() *usecase.Confirm {
	return usecase.NewConfirm(d.deals, d.deals, d.reservations, d.orders, d.tx, d.clock, d.ids)
}

func (d *flowDeps) cancel() *usecase.Cancel {
	return usecase.NewCancel(d.deals, d.reservations, d.gate, d.tx, d.clock, discardLog)
}

const reservationID = "00000000-0000-7000-8000-999999999999"

func ownInput() usecase.ConfirmInput {
	return usecase.ConfirmInput{ReservationID: reservationID, UserID: "user-1"}
}

func TestConfirm(t *testing.T) {
	t.Parallel()

	t.Run("confirms the reservation, moves stock to sold and creates the order in one transaction", func(t *testing.T) {
		t.Parallel()
		deps := newFlowDeps(t, domain.StatusPending)

		order, err := deps.confirm().Execute(t.Context(), ownInput())

		require.NoError(t, err)
		require.Equal(t, 1, deps.tx.calls)
		require.Equal(t, domain.StatusConfirmed, deps.reservations.byID[reservationID].Status())
		require.Equal(t, []stockMove{{dealID: 7, qty: 2}}, deps.deals.commits)
		require.Equal(t, reservationID, order.ReservationID())
		require.Equal(t, usd(t, 9998), order.Total())
		require.Equal(t, t0.Add(time.Minute), order.CreatedAt())
		require.Len(t, deps.orders.created, 1)
	})

	failures := []struct {
		name    string
		status  domain.ReservationStatus
		mutate  func(*flowDeps, *usecase.ConfirmInput)
		wantErr error
	}{
		{
			name:    "returns ErrReservationNotFound for an unknown reservation",
			status:  domain.StatusPending,
			mutate:  func(_ *flowDeps, in *usecase.ConfirmInput) { in.ReservationID = "00000000-0000-7000-8000-000000000404" },
			wantErr: domain.ErrReservationNotFound,
		},
		{
			name:    "returns ErrReservationNotFound for another user's reservation",
			status:  domain.StatusPending,
			mutate:  func(_ *flowDeps, in *usecase.ConfirmInput) { in.UserID = "intruder" },
			wantErr: domain.ErrReservationNotFound,
		},
		{
			name:    "returns ErrReservationExpired once the TTL has elapsed",
			status:  domain.StatusPending,
			mutate:  func(d *flowDeps, _ *usecase.ConfirmInput) { d.clock.now = t0.Add(10 * time.Minute) },
			wantErr: domain.ErrReservationExpired,
		},
		{
			name:    "returns ErrReservationNotPending for a cancelled reservation",
			status:  domain.StatusCancelled,
			mutate:  func(*flowDeps, *usecase.ConfirmInput) {},
			wantErr: domain.ErrReservationNotPending,
		},
		{
			name:    "returns ErrReservationExpired for an expired reservation",
			status:  domain.StatusExpired,
			mutate:  func(*flowDeps, *usecase.ConfirmInput) {},
			wantErr: domain.ErrReservationExpired,
		},
	}
	for _, tc := range failures {
		t.Run(tc.name+" without moving stock or creating an order", func(t *testing.T) {
			t.Parallel()
			deps := newFlowDeps(t, tc.status)
			in := ownInput()
			tc.mutate(deps, &in)

			_, err := deps.confirm().Execute(t.Context(), in)

			require.ErrorIs(t, err, tc.wantErr)
			require.Empty(t, deps.reservations.updates)
			require.Empty(t, deps.deals.commits)
			require.Empty(t, deps.orders.created)
		})
	}

	t.Run("returns ErrReservationNotPending when storage lost a race", func(t *testing.T) {
		t.Parallel()
		deps := newFlowDeps(t, domain.StatusPending)
		deps.reservations.updateErr = domain.ErrReservationNotPending

		_, err := deps.confirm().Execute(t.Context(), ownInput())

		require.ErrorIs(t, err, domain.ErrReservationNotPending)
		require.Empty(t, deps.deals.commits)
	})

	t.Run("propagates a stock move failure so the transaction rolls back", func(t *testing.T) {
		t.Parallel()
		deps := newFlowDeps(t, domain.StatusPending)
		deps.deals.commitErr = domain.ErrCorruptState

		_, err := deps.confirm().Execute(t.Context(), ownInput())

		require.ErrorIs(t, err, domain.ErrCorruptState)
		require.Empty(t, deps.orders.created)
	})

	t.Run("propagates an order storage failure so the transaction rolls back", func(t *testing.T) {
		t.Parallel()
		deps := newFlowDeps(t, domain.StatusPending)
		deps.orders.err = domain.ErrAlreadyExists

		_, err := deps.confirm().Execute(t.Context(), ownInput())

		require.ErrorIs(t, err, domain.ErrAlreadyExists)
	})

	t.Run("propagates an order id generation failure", func(t *testing.T) {
		t.Parallel()
		deps := newFlowDeps(t, domain.StatusPending)
		deps.ids.err = errors.New("entropy exhausted")

		_, err := deps.confirm().Execute(t.Context(), ownInput())

		require.ErrorIs(t, err, deps.ids.err)
		require.Empty(t, deps.orders.created)
	})
}

func TestCancel(t *testing.T) {
	t.Parallel()

	cancelInput := func() usecase.CancelInput {
		return usecase.CancelInput{ReservationID: reservationID, UserID: "user-1"}
	}

	t.Run("cancels the reservation and returns its units to the deal and the gate", func(t *testing.T) {
		t.Parallel()
		deps := newFlowDeps(t, domain.StatusPending)

		err := deps.cancel().Execute(t.Context(), cancelInput())

		require.NoError(t, err)
		require.Equal(t, 1, deps.tx.calls)
		require.Equal(t, domain.StatusCancelled, deps.reservations.byID[reservationID].Status())
		require.Equal(t, []stockMove{{dealID: 7, qty: 2}}, deps.deals.releases)
		require.Equal(t, []gateCall{{dealID: 7, userID: "user-1", qty: 2}}, deps.gate.releases)
	})

	t.Run("cancels a pending reservation even after its TTL", func(t *testing.T) {
		t.Parallel()
		deps := newFlowDeps(t, domain.StatusPending)
		deps.clock.now = t0.Add(time.Hour)

		require.NoError(t, deps.cancel().Execute(t.Context(), cancelInput()))
	})

	failures := []struct {
		name    string
		status  domain.ReservationStatus
		userID  string
		wantErr error
	}{
		{name: "returns ErrReservationNotFound for another user's reservation", status: domain.StatusPending, userID: "intruder", wantErr: domain.ErrReservationNotFound},
		{name: "returns ErrReservationNotPending for a confirmed reservation", status: domain.StatusConfirmed, userID: "user-1", wantErr: domain.ErrReservationNotPending},
		{name: "returns ErrReservationNotPending for a cancelled reservation", status: domain.StatusCancelled, userID: "user-1", wantErr: domain.ErrReservationNotPending},
	}
	for _, tc := range failures {
		t.Run(tc.name+" without releasing stock", func(t *testing.T) {
			t.Parallel()
			deps := newFlowDeps(t, tc.status)
			in := cancelInput()
			in.UserID = tc.userID

			err := deps.cancel().Execute(t.Context(), in)

			require.ErrorIs(t, err, tc.wantErr)
			require.Empty(t, deps.deals.releases)
			require.Empty(t, deps.gate.releases)
		})
	}

	t.Run("propagates a stock release failure without touching the gate", func(t *testing.T) {
		t.Parallel()
		deps := newFlowDeps(t, domain.StatusPending)
		deps.deals.releaseErr = domain.ErrCorruptState

		err := deps.cancel().Execute(t.Context(), cancelInput())

		require.ErrorIs(t, err, domain.ErrCorruptState)
		require.Empty(t, deps.gate.releases)
	})

	t.Run("succeeds when the gate release fails after the cancellation committed", func(t *testing.T) {
		t.Parallel()
		deps := newFlowDeps(t, domain.StatusPending)
		deps.gate.releaseErr = domain.ErrStockUnavailable

		err := deps.cancel().Execute(t.Context(), cancelInput())

		require.NoError(t, err)
		require.Equal(t, domain.StatusCancelled, deps.reservations.byID[reservationID].Status())
	})
}
