//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5:// driver
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/platform/config"
	"github.com/whoyoshome/flashdeal-service/internal/repository/postgres"
	"github.com/whoyoshome/flashdeal-service/migrations"
)

// testPool is shared by every test in the package. Tests isolate themselves
// by creating their own deals and users instead of truncating tables, so they
// can run in parallel against one container.
var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	code, err := runIntegration(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration setup: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runIntegration(m *testing.M) (int, error) {
	ctx := context.Background()

	ctr, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("flashdeal"),
		tcpostgres.WithUsername("flashdeal"),
		tcpostgres.WithPassword("flashdeal"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		return 0, fmt.Errorf("start postgres: %w", err)
	}
	defer func() { _ = ctr.Terminate(context.Background()) }()

	url, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return 0, fmt.Errorf("connection string: %w", err)
	}
	if err := migrateUp(url); err != nil {
		return 0, err
	}

	testPool, err = postgres.NewPool(ctx, config.Postgres{
		URL:              url,
		MaxConns:         50,
		MinConns:         1,
		ConnectTimeout:   5 * time.Second,
		StatementTimeout: 5 * time.Second,
	})
	if err != nil {
		return 0, fmt.Errorf("open pool: %w", err)
	}
	defer testPool.Close()

	return m.Run(), nil
}

func migrateUp(url string) (err error) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("open migrations: %w", err)
	}
	mig, err := migrate.NewWithSourceInstance("iofs", src, strings.Replace(url, "postgres://", "pgx5://", 1))
	if err != nil {
		return fmt.Errorf("init migrate: %w", err)
	}
	defer func() {
		srcErr, dbErr := mig.Close()
		err = errors.Join(err, srcErr, dbErr)
	}()
	if err := mig.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// t0 anchors deal windows; deals are always active around it.
var t0 = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

var idSeq atomic.Int64

// uniqueID returns a unique, valid UUID string for reservations and orders.
func uniqueID() string {
	return fmt.Sprintf("00000000-0000-7000-8000-%012d", idSeq.Add(1))
}

// uniqueUser returns a user ID no other test uses.
func uniqueUser() string { return fmt.Sprintf("user-%d", idSeq.Add(1)) }

type dealOpts struct {
	stock    int
	limit    int
	startsAt time.Time
	endsAt   time.Time
}

// createDeal stores a deal (active around t0 unless overridden).
func createDeal(t *testing.T, opts dealOpts) domain.Deal {
	t.Helper()
	if opts.stock == 0 {
		opts.stock = 100
	}
	if opts.limit == 0 {
		opts.limit = 5
	}
	if opts.startsAt.IsZero() {
		opts.startsAt = t0.Add(-time.Hour)
	}
	if opts.endsAt.IsZero() {
		opts.endsAt = t0.Add(time.Hour)
	}
	price, err := domain.NewMoney(4999, "USD")
	require.NoError(t, err)
	d, err := domain.NewDeal(domain.DealSpec{
		ProductID:    "sku-" + uniqueUser(),
		Title:        "Integration deal",
		Price:        price,
		TotalStock:   opts.stock,
		PerUserLimit: opts.limit,
		StartsAt:     opts.startsAt,
		EndsAt:       opts.endsAt,
	})
	require.NoError(t, err)

	stored, err := postgres.NewDealRepo(testPool).Create(t.Context(), d)
	require.NoError(t, err)
	return stored
}

// newReservation builds a pending reservation created at t0.
func newReservation(t *testing.T, dealID int64, userID, key string, qty int) domain.Reservation {
	t.Helper()
	r, err := domain.NewReservation(domain.ReservationSpec{
		ID:             uniqueID(),
		DealID:         dealID,
		UserID:         userID,
		Quantity:       qty,
		IdempotencyKey: key,
		ExpiresAt:      t0.Add(5 * time.Minute),
	}, t0)
	require.NoError(t, err)
	return r
}

// reloadDeal reads the deal's current counters.
func reloadDeal(t *testing.T, id int64) domain.Deal {
	t.Helper()
	d, err := postgres.NewDealRepo(testPool).GetByID(t.Context(), id)
	require.NoError(t, err)
	return d
}
