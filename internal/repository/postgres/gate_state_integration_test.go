//go:build integration

package postgres_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/repository/postgres"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

func TestDealRepoGateState(t *testing.T) {
	t.Parallel()
	deals := postgres.NewDealRepo(testPool, testShards)
	reservations := postgres.NewReservationRepo(testPool)

	t.Run("sums pending and confirmed units per buyer and ignores released ones", func(t *testing.T) {
		t.Parallel()
		d := createDeal(t, dealOpts{stock: 20, limit: 10})
		alice, bob, carol := uniqueUser(), uniqueUser(), uniqueUser()
		store := func(user string, qty int) domain.Reservation {
			r := newReservation(t, d.ID(), user, uniqueID(), qty)
			_, _, err := reservations.Create(t.Context(), r)
			require.NoError(t, err)
			return r
		}
		store(alice, 2)
		confirmed := store(alice, 3)
		require.NoError(t, confirmed.Confirm(t0.Add(time.Minute)))
		require.NoError(t, reservations.UpdateStatus(t.Context(), confirmed))
		require.NoError(t, deals.CommitReserved(t.Context(), confirmed))
		store(bob, 4)
		cancelled := store(carol, 1)
		require.NoError(t, cancelled.Cancel(t0.Add(time.Minute)))
		require.NoError(t, reservations.UpdateStatus(t.Context(), cancelled))
		require.NoError(t, deals.ReleaseReserved(t.Context(), []domain.Reservation{cancelled}))

		state, err := deals.GateState(t.Context(), d.ID())

		require.NoError(t, err)
		require.Equal(t, usecase.GateState{Available: 11, Held: map[string]int{alice: 5, bob: 4}}, state)
	})

	t.Run("returns every unit and no buyers for an untouched deal", func(t *testing.T) {
		t.Parallel()
		d := createDeal(t, dealOpts{stock: 7})

		state, err := deals.GateState(t.Context(), d.ID())

		require.NoError(t, err)
		require.Equal(t, usecase.GateState{Available: 7, Held: map[string]int{}}, state)
	})

	t.Run("returns ErrDealNotFound for an unknown deal", func(t *testing.T) {
		t.Parallel()

		_, err := deals.GateState(t.Context(), 987654321)

		require.ErrorIs(t, err, domain.ErrDealNotFound)
	})
}
