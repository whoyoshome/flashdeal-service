//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/repository/postgres"
)

var errBoom = errors.New("boom")

func TestTxManager(t *testing.T) {
	t.Parallel()
	txm := postgres.NewTxManager(testPool)
	deals := postgres.NewDealRepo(testPool)

	t.Run("commits every statement when fn succeeds", func(t *testing.T) {
		t.Parallel()
		d := createDeal(t, dealOpts{stock: 10})
		reservations := postgres.NewReservationRepo(testPool)

		err := txm.WithinTx(t.Context(), func(ctx context.Context) error {
			_, _, err := reservations.Create(ctx, newReservation(t, d.ID(), uniqueUser(), "k", 2))
			return err
		})

		require.NoError(t, err)
		require.Equal(t, 2, reloadDeal(t, d.ID()).Reserved())
	})

	t.Run("rolls back every statement when fn fails", func(t *testing.T) {
		t.Parallel()
		d := createDeal(t, dealOpts{stock: 10})
		reservations := postgres.NewReservationRepo(testPool)

		err := txm.WithinTx(t.Context(), func(ctx context.Context) error {
			if _, _, err := reservations.Create(ctx, newReservation(t, d.ID(), uniqueUser(), "k", 2)); err != nil {
				return err
			}
			return errBoom
		})

		require.ErrorIs(t, err, errBoom)
		require.Zero(t, reloadDeal(t, d.ID()).Reserved())
	})

	t.Run("commits once fn succeeded even if the caller's context is cancelled", func(t *testing.T) {
		t.Parallel()
		d := createDeal(t, dealOpts{stock: 10})
		reservations := postgres.NewReservationRepo(testPool)
		res := newReservation(t, d.ID(), uniqueUser(), "k", 1)
		ctx, cancel := context.WithCancel(t.Context())

		err := txm.WithinTx(ctx, func(ctx context.Context) error {
			if _, _, err := reservations.Create(ctx, res); err != nil {
				return err
			}
			cancel() // the client gives up after the work is done, before COMMIT
			return nil
		})

		require.NoError(t, err)
		require.Equal(t, 1, reloadDeal(t, d.ID()).Reserved())
		_, err = reservations.GetForUpdate(t.Context(), res.ID())
		require.NoError(t, err, "the committed reservation must exist")
	})

	t.Run("joins the outer transaction instead of committing a nested one", func(t *testing.T) {
		t.Parallel()
		d := createDeal(t, dealOpts{stock: 10})
		reservations := postgres.NewReservationRepo(testPool)

		err := txm.WithinTx(t.Context(), func(ctx context.Context) error {
			inner := txm.WithinTx(ctx, func(ctx context.Context) error {
				_, _, err := reservations.Create(ctx, newReservation(t, d.ID(), uniqueUser(), "k", 1))
				return err
			})
			require.NoError(t, inner)
			return deals.CommitReserved(ctx, d.ID(), 1)
		})

		require.NoError(t, err)
		got := reloadDeal(t, d.ID())
		require.Zero(t, got.Reserved())
		require.Equal(t, 1, got.Sold())
	})
}

func TestDealRepo(t *testing.T) {
	t.Parallel()
	repo := postgres.NewDealRepo(testPool)

	t.Run("creates a deal and reads it back unchanged", func(t *testing.T) {
		t.Parallel()
		created := createDeal(t, dealOpts{stock: 7, limit: 3})

		got, err := repo.GetByID(t.Context(), created.ID())

		require.NoError(t, err)
		require.Positive(t, got.ID())
		require.Equal(t, 7, got.TotalStock())
		require.Equal(t, 3, got.PerUserLimit())
		require.Equal(t, int64(4999), got.Price().Amount())
		require.Equal(t, "USD", got.Price().Currency())
		require.True(t, got.StartsAt().Equal(created.StartsAt()))
		require.Equal(t, time.UTC, got.StartsAt().Location())
		require.False(t, got.CreatedAt().IsZero())
	})

	t.Run("returns ErrDealNotFound for an unknown id", func(t *testing.T) {
		t.Parallel()

		_, err := repo.GetByID(t.Context(), 999_999_999)

		require.ErrorIs(t, err, domain.ErrDealNotFound)
	})

	t.Run("lists only deals active at now, paginated by id", func(t *testing.T) {
		t.Parallel()
		// A window far from every other test's deals keeps this list isolated.
		now := t0.Add(1000 * time.Hour)
		active1 := createDeal(t, dealOpts{startsAt: now.Add(-time.Hour), endsAt: now.Add(time.Hour)})
		active2 := createDeal(t, dealOpts{startsAt: now, endsAt: now.Add(time.Minute)})
		createDeal(t, dealOpts{startsAt: now.Add(time.Second), endsAt: now.Add(time.Hour)}) // not started
		createDeal(t, dealOpts{startsAt: now.Add(-time.Hour), endsAt: now})                 // just ended

		page1, err := repo.ListActive(t.Context(), now, 0, 1)
		require.NoError(t, err)
		require.Len(t, page1, 1)
		require.Equal(t, active1.ID(), page1[0].ID())

		page2, err := repo.ListActive(t.Context(), now, page1[0].ID(), 10)
		require.NoError(t, err)
		require.Len(t, page2, 1)
		require.Equal(t, active2.ID(), page2[0].ID())
	})

	t.Run("commits reserved units into sold", func(t *testing.T) {
		t.Parallel()
		d := createDeal(t, dealOpts{stock: 10})
		_, _, err := postgres.NewReservationRepo(testPool).Create(t.Context(), newReservation(t, d.ID(), uniqueUser(), "k", 3))
		require.NoError(t, err)

		require.NoError(t, repo.CommitReserved(t.Context(), d.ID(), 3))

		got := reloadDeal(t, d.ID())
		require.Zero(t, got.Reserved())
		require.Equal(t, 3, got.Sold())
	})

	t.Run("releases reserved units back to the deal", func(t *testing.T) {
		t.Parallel()
		d := createDeal(t, dealOpts{stock: 10})
		_, _, err := postgres.NewReservationRepo(testPool).Create(t.Context(), newReservation(t, d.ID(), uniqueUser(), "k", 3))
		require.NoError(t, err)

		require.NoError(t, repo.ReleaseReserved(t.Context(), d.ID(), 2))

		require.Equal(t, 1, reloadDeal(t, d.ID()).Reserved())
	})

	t.Run("reports corrupt state when moving more units than are reserved", func(t *testing.T) {
		t.Parallel()
		d := createDeal(t, dealOpts{stock: 10})

		require.ErrorIs(t, repo.CommitReserved(t.Context(), d.ID(), 1), domain.ErrCorruptState)
		require.ErrorIs(t, repo.ReleaseReserved(t.Context(), d.ID(), 1), domain.ErrCorruptState)
	})
}

func TestReservationRepoCreate(t *testing.T) {
	t.Parallel()
	repo := postgres.NewReservationRepo(testPool)

	t.Run("stores the reservation and reserves its units", func(t *testing.T) {
		t.Parallel()
		d := createDeal(t, dealOpts{stock: 10})
		res := newReservation(t, d.ID(), uniqueUser(), "k", 2)

		stored, created, err := repo.Create(t.Context(), res)

		require.NoError(t, err)
		require.True(t, created)
		require.Equal(t, res.ID(), stored.ID())
		require.Equal(t, 2, reloadDeal(t, d.ID()).Reserved())
	})

	t.Run("returns ErrSoldOut and stores nothing when stock is short", func(t *testing.T) {
		t.Parallel()
		d := createDeal(t, dealOpts{stock: 1})
		res := newReservation(t, d.ID(), uniqueUser(), "k", 2)

		_, _, err := repo.Create(t.Context(), res)

		require.ErrorIs(t, err, domain.ErrSoldOut)
		require.Zero(t, reloadDeal(t, d.ID()).Reserved())
		_, err = repo.GetForUpdate(t.Context(), res.ID())
		require.ErrorIs(t, err, domain.ErrReservationNotFound, "the insert must roll back with the stock update")
	})

	t.Run("returns ErrDealNotFound for an unknown deal", func(t *testing.T) {
		t.Parallel()

		_, _, err := repo.Create(t.Context(), newReservation(t, 999_999_999, uniqueUser(), "k", 1))

		require.ErrorIs(t, err, domain.ErrDealNotFound)
	})

	t.Run("returns the stored reservation on an idempotent retry without reserving again", func(t *testing.T) {
		t.Parallel()
		d := createDeal(t, dealOpts{stock: 10})
		user := uniqueUser()
		first := newReservation(t, d.ID(), user, "same-key", 2)
		_, _, err := repo.Create(t.Context(), first)
		require.NoError(t, err)

		stored, created, err := repo.Create(t.Context(), newReservation(t, d.ID(), user, "same-key", 2))

		require.NoError(t, err)
		require.False(t, created)
		require.Equal(t, first.ID(), stored.ID())
		require.Equal(t, 2, reloadDeal(t, d.ID()).Reserved())
	})

	t.Run("returns ErrIdempotencyKeyReused when the key comes with a different request", func(t *testing.T) {
		t.Parallel()
		d := createDeal(t, dealOpts{stock: 10})
		user := uniqueUser()
		_, _, err := repo.Create(t.Context(), newReservation(t, d.ID(), user, "same-key", 1))
		require.NoError(t, err)

		_, _, err = repo.Create(t.Context(), newReservation(t, d.ID(), user, "same-key", 3))

		require.ErrorIs(t, err, domain.ErrIdempotencyKeyReused)
		require.Equal(t, 1, reloadDeal(t, d.ID()).Reserved())
	})
}

func TestReservationRepoStatus(t *testing.T) {
	t.Parallel()
	repo := postgres.NewReservationRepo(testPool)
	txm := postgres.NewTxManager(testPool)

	t.Run("persists a confirmation decided under the row lock", func(t *testing.T) {
		t.Parallel()
		d := createDeal(t, dealOpts{stock: 10})
		res := newReservation(t, d.ID(), uniqueUser(), "k", 1)
		_, _, err := repo.Create(t.Context(), res)
		require.NoError(t, err)

		err = txm.WithinTx(t.Context(), func(ctx context.Context) error {
			locked, err := repo.GetForUpdate(ctx, res.ID())
			if err != nil {
				return err
			}
			if err := locked.Confirm(t0.Add(time.Minute)); err != nil {
				return err
			}
			return repo.UpdateStatus(ctx, locked)
		})
		require.NoError(t, err)

		got, err := repo.GetForUpdate(t.Context(), res.ID())
		require.NoError(t, err)
		require.Equal(t, domain.StatusConfirmed, got.Status())
		require.True(t, got.UpdatedAt().Equal(t0.Add(time.Minute)))
	})

	t.Run("returns ErrReservationNotPending when the stored row already left pending", func(t *testing.T) {
		t.Parallel()
		d := createDeal(t, dealOpts{stock: 10})
		res := newReservation(t, d.ID(), uniqueUser(), "k", 1)
		_, _, err := repo.Create(t.Context(), res)
		require.NoError(t, err)
		cancelled := res
		require.NoError(t, cancelled.Cancel(t0))
		require.NoError(t, repo.UpdateStatus(t.Context(), cancelled))

		// A stale copy still believes it is pending.
		confirmed := res
		require.NoError(t, confirmed.Confirm(t0))

		require.ErrorIs(t, repo.UpdateStatus(t.Context(), confirmed), domain.ErrReservationNotPending)
	})

	t.Run("returns ErrReservationNotFound for an unknown id", func(t *testing.T) {
		t.Parallel()

		_, err := repo.GetForUpdate(t.Context(), uniqueID())

		require.ErrorIs(t, err, domain.ErrReservationNotFound)
	})

	t.Run("returns a validation error for a malformed id", func(t *testing.T) {
		t.Parallel()

		_, err := repo.GetForUpdate(t.Context(), "not-a-uuid")

		de, ok := domain.AsDomain(err)
		require.True(t, ok)
		require.Equal(t, domain.CodeValidation, de.Code)
	})
}

func TestOrderRepo(t *testing.T) {
	t.Parallel()
	orders := postgres.NewOrderRepo(testPool)
	reservations := postgres.NewReservationRepo(testPool)

	confirmedReservation := func(t *testing.T) domain.Reservation {
		t.Helper()
		d := createDeal(t, dealOpts{stock: 10})
		res := newReservation(t, d.ID(), uniqueUser(), "k", 2)
		_, _, err := reservations.Create(t.Context(), res)
		require.NoError(t, err)
		require.NoError(t, res.Confirm(t0))
		require.NoError(t, reservations.UpdateStatus(t.Context(), res))
		return res
	}

	t.Run("stores an order for a confirmed reservation", func(t *testing.T) {
		t.Parallel()
		res := confirmedReservation(t)
		price, err := domain.NewMoney(4999, "USD")
		require.NoError(t, err)
		o, err := domain.NewOrder(uniqueID(), res, price, t0)
		require.NoError(t, err)

		require.NoError(t, orders.Create(t.Context(), o))
	})

	t.Run("returns ErrAlreadyExists for a second order on the same reservation", func(t *testing.T) {
		t.Parallel()
		res := confirmedReservation(t)
		price, err := domain.NewMoney(4999, "USD")
		require.NoError(t, err)
		first, err := domain.NewOrder(uniqueID(), res, price, t0)
		require.NoError(t, err)
		second, err := domain.NewOrder(uniqueID(), res, price, t0)
		require.NoError(t, err)
		require.NoError(t, orders.Create(t.Context(), first))

		require.ErrorIs(t, orders.Create(t.Context(), second), domain.ErrAlreadyExists)
	})
}

// TestReservationRepoNoOversell is the PostgreSQL half of the zero-oversell
// guarantee: even without the Redis gate, concurrent reservations can never
// reserve more than total_stock.
func TestReservationRepoNoOversell(t *testing.T) {
	t.Parallel()
	const stock, buyers = 20, 200
	repo := postgres.NewReservationRepo(testPool)
	d := createDeal(t, dealOpts{stock: stock, limit: 1})

	attempts := make([]domain.Reservation, buyers)
	for i := range attempts {
		attempts[i] = newReservation(t, d.ID(), uniqueUser(), "k", 1)
	}

	var ok, soldOut atomic.Int64
	var wg sync.WaitGroup
	for _, res := range attempts {
		wg.Go(func() {
			_, _, err := repo.Create(t.Context(), res)
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, domain.ErrSoldOut):
				soldOut.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
	wg.Wait()

	require.EqualValues(t, stock, ok.Load())
	require.EqualValues(t, buyers-stock, soldOut.Load())
	got := reloadDeal(t, d.ID())
	require.Equal(t, stock, got.Reserved())
	require.Zero(t, got.Available())
}

// TestReservationRepoConcurrentRetries checks that a burst of retries with
// one idempotency key creates exactly one reservation and reserves once.
func TestReservationRepoConcurrentRetries(t *testing.T) {
	t.Parallel()
	const retries = 20
	repo := postgres.NewReservationRepo(testPool)
	d := createDeal(t, dealOpts{stock: 10})
	user := uniqueUser()

	attempts := make([]domain.Reservation, retries)
	for i := range attempts {
		attempts[i] = newReservation(t, d.ID(), user, "retry-key", 2)
	}

	var created atomic.Int64
	ids := make([]string, retries)
	var wg sync.WaitGroup
	for i, res := range attempts {
		wg.Go(func() {
			stored, isNew, err := repo.Create(t.Context(), res)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if isNew {
				created.Add(1)
			}
			ids[i] = stored.ID()
		})
	}
	wg.Wait()

	require.EqualValues(t, 1, created.Load())
	for _, id := range ids {
		require.Equal(t, ids[0], id, "every retry must see the same reservation")
	}
	require.Equal(t, 2, reloadDeal(t, d.ID()).Reserved())
}
