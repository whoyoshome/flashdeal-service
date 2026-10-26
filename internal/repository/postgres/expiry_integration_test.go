//go:build integration

package postgres_test

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/repository/postgres"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

// The expiry queue is global: it hands out every due reservation in the
// table, not only those of one deal. These tests therefore do not call
// t.Parallel (Go runs them before any parallel test resumes), and each one
// works in its own era far in the past, earlier for every call. A test at
// era E only asks for rows due by E plus minutes, so it never sees rows of
// earlier tests (later eras) nor those of the parallel tests (2026).
var eraSeq atomic.Int64

func nextEra() time.Time {
	return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(-int(eraSeq.Add(1)), 0, 0)
}

// storeDue stores a pending reservation of qty units created at era and
// expiring after ttl, reserving its units on the deal.
func storeDue(t *testing.T, dealID int64, era time.Time, ttl time.Duration, qty int) domain.Reservation {
	t.Helper()
	r, err := domain.NewReservation(domain.ReservationSpec{
		ID:             uniqueID(),
		DealID:         dealID,
		UserID:         uniqueUser(),
		Quantity:       qty,
		IdempotencyKey: "k",
		ExpiresAt:      era.Add(ttl),
	}, era)
	require.NoError(t, err)
	_, created, err := postgres.NewReservationRepo(testPool).Create(t.Context(), r)
	require.NoError(t, err)
	require.True(t, created)
	return r
}

func ids(rs []domain.Reservation) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ID()
	}
	return out
}

func lockDue(ctx context.Context, t *testing.T, now time.Time, limit int) []domain.Reservation {
	t.Helper()
	due, err := postgres.NewReservationRepo(testPool).LockDue(ctx, now, limit)
	require.NoError(t, err)
	return due
}

func TestExpiryQueue(t *testing.T) {
	txm := postgres.NewTxManager(testPool)
	repo := postgres.NewReservationRepo(testPool)

	t.Run("locks only pending reservations due at now, oldest first, up to the limit", func(t *testing.T) {
		era := nextEra()
		d := createDeal(t, dealOpts{stock: 10})
		second := storeDue(t, d.ID(), era, 2*time.Minute, 1)
		first := storeDue(t, d.ID(), era, time.Minute, 1)
		dueExactlyNow := storeDue(t, d.ID(), era, 3*time.Minute, 1)
		storeDue(t, d.ID(), era, 10*time.Minute, 1) // not due yet
		confirmed := storeDue(t, d.ID(), era, 90*time.Second, 1)
		require.NoError(t, confirmed.Confirm(era.Add(time.Second)))
		require.NoError(t, repo.UpdateStatus(t.Context(), confirmed))
		now := era.Add(3 * time.Minute)

		require.NoError(t, txm.WithinTx(t.Context(), func(ctx context.Context) error {
			require.Equal(t, ids([]domain.Reservation{first, second, dueExactlyNow}), ids(lockDue(ctx, t, now, 10)))
			return nil
		}))
		require.NoError(t, txm.WithinTx(t.Context(), func(ctx context.Context) error {
			require.Equal(t, ids([]domain.Reservation{first, second}), ids(lockDue(ctx, t, now, 2)))
			return nil
		}))
	})

	t.Run("skips reservations another transaction holds instead of waiting", func(t *testing.T) {
		era := nextEra()
		d := createDeal(t, dealOpts{stock: 10})
		for range 5 {
			storeDue(t, d.ID(), era, time.Minute, 1)
		}
		now := era.Add(time.Hour)

		require.NoError(t, txm.WithinTx(t.Context(), func(ctx context.Context) error {
			held := lockDue(ctx, t, now, 3)
			require.Len(t, held, 3)
			// A second worker, in its own transaction, while the first still
			// holds its rows.
			return txm.WithinTx(t.Context(), func(other context.Context) error {
				rest := lockDue(other, t, now, 5)
				require.Len(t, rest, 2)
				require.NotContains(t, ids(held), rest[0].ID())
				require.NotContains(t, ids(held), rest[1].ID())
				return nil
			})
		}))
	})

	t.Run("persists a batch of expirations decided by the domain", func(t *testing.T) {
		era := nextEra()
		d := createDeal(t, dealOpts{stock: 10})
		storeDue(t, d.ID(), era, time.Minute, 1)
		storeDue(t, d.ID(), era, time.Minute, 2)
		now := era.Add(time.Hour)

		var expired []domain.Reservation
		require.NoError(t, txm.WithinTx(t.Context(), func(ctx context.Context) error {
			expired = lockDue(ctx, t, now, 10)
			for i := range expired {
				require.NoError(t, expired[i].Expire(now))
			}
			return repo.MarkExpired(ctx, expired)
		}))

		require.Len(t, expired, 2)
		for _, r := range expired {
			stored, err := repo.GetForUpdate(t.Context(), r.ID())
			require.NoError(t, err)
			require.Equal(t, domain.StatusExpired, stored.Status())
			require.True(t, now.Equal(stored.UpdatedAt()))
		}
	})

	t.Run("returns ErrReservationNotPending when a row already left pending", func(t *testing.T) {
		era := nextEra()
		d := createDeal(t, dealOpts{stock: 10})
		stale := storeDue(t, d.ID(), era, time.Minute, 1)
		confirmed := stale
		require.NoError(t, confirmed.Confirm(era.Add(time.Second)))
		require.NoError(t, repo.UpdateStatus(t.Context(), confirmed))
		require.NoError(t, stale.Expire(era.Add(time.Hour)))

		err := repo.MarkExpired(t.Context(), []domain.Reservation{stale})

		require.ErrorIs(t, err, domain.ErrReservationNotPending)
		stored, err := repo.GetForUpdate(t.Context(), stale.ID())
		require.NoError(t, err)
		require.Equal(t, domain.StatusConfirmed, stored.Status())
	})
}

// recordingGate counts releases per buyer. Every test buyer holds a single
// reservation, so a count above one means a reservation was expired twice.
type recordingGate struct {
	mu       sync.Mutex
	released map[string]int
}

var _ usecase.StockGate = (*recordingGate)(nil)

func (g *recordingGate) Load(context.Context, domain.Deal) error { return nil }

func (g *recordingGate) TryReserve(context.Context, int64, string, int, int) error { return nil }

func (g *recordingGate) Release(_ context.Context, _ int64, userID string, _ int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.released[userID]++
	return nil
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

// TestExpire_ConcurrentWorkersNeverShareRows is the phase 5 guarantee: two
// expirers racing over the same backlog, as two replicas would, expire every
// reservation exactly once and return exactly the units they held.
func TestExpire_ConcurrentWorkersNeverShareRows(t *testing.T) {
	const perDeal, workers, batch = 60, 2, 5
	era := nextEra()
	deals := []domain.Deal{
		createDeal(t, dealOpts{stock: perDeal}),
		createDeal(t, dealOpts{stock: perDeal}),
		createDeal(t, dealOpts{stock: perDeal}),
	}
	var users []string
	for i := range perDeal {
		for _, d := range deals {
			users = append(users, storeDue(t, d.ID(), era, time.Duration(i+1)*time.Second, 1).UserID())
		}
	}

	gate := &recordingGate{released: map[string]int{}}
	clock := fixedClock{now: era.Add(time.Hour)}
	var expired atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range workers {
		uc := usecase.NewExpire(
			postgres.NewReservationRepo(testPool), postgres.NewDealRepo(testPool), gate,
			postgres.NewTxManager(testPool), clock, batch, slog.New(slog.DiscardHandler),
		)
		wg.Go(func() {
			<-start
			n, err := uc.Execute(t.Context())
			require.NoError(t, err)
			expired.Add(int64(n))
		})
	}
	close(start)
	wg.Wait()

	require.EqualValues(t, len(users), expired.Load())
	require.Len(t, gate.released, len(users))
	for _, u := range users {
		require.Equal(t, 1, gate.released[u], "user %s released %d times", u, gate.released[u])
	}
	for _, d := range deals {
		require.Zero(t, reloadDeal(t, d.ID()).Reserved(), "deal %d", d.ID())
	}
	var pending int
	require.NoError(t, testPool.QueryRow(t.Context(),
		`SELECT count(*) FROM reservations WHERE deal_id = ANY($1) AND status = 'pending'`,
		[]int64{deals[0].ID(), deals[1].ID(), deals[2].ID()},
	).Scan(&pending))
	require.Zero(t, pending)
}
