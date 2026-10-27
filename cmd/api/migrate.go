package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5:// driver
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/whoyoshome/flashdeal-service/internal/platform/config"
	"github.com/whoyoshome/flashdeal-service/internal/platform/logger"
	"github.com/whoyoshome/flashdeal-service/migrations"
)

// migrateLockTimeout bounds the wait for the advisory lock another migrate
// run holds, so a second Job fails instead of hanging.
const migrateLockTimeout = 30 * time.Second

// migrateUp applies every pending migration embedded in the binary, so a
// Kubernetes Job can run the same distroless image as the API.
func migrateUp(ctx context.Context, environ []string, stdout io.Writer) (err error) {
	cfg, err := config.LoadMigrate(environ)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	log := logger.New(stdout, cfg.LogLevel)

	dsn, err := migrateDSN(cfg.Postgres)
	if err != nil {
		return err
	}
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("open migrations: %w", err)
	}
	mig, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return fmt.Errorf("init migrate: %w", err)
	}
	defer func() {
		srcErr, dbErr := mig.Close()
		err = errors.Join(err, srcErr, dbErr)
	}()
	mig.LockTimeout = migrateLockTimeout

	// golang-migrate takes no context. On SIGTERM it is asked to stop after
	// the migration in progress, never in the middle of one.
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		select {
		case <-ctx.Done():
			mig.GracefulStop <- true
		case <-done:
		}
	})
	upErr := mig.Up()
	close(done)
	wg.Wait()

	if upErr != nil && !errors.Is(upErr, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", upErr)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("migrate up interrupted: %w", err)
	}
	version, dirty, err := mig.Version()
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	log.InfoContext(ctx, "migrations applied",
		slog.Uint64("version", uint64(version)),
		slog.Bool("dirty", dirty),
		slog.Bool("changed", upErr == nil),
	)
	return nil
}

// migrateDSN turns the service's postgres:// URL into the pgx5:// URL the
// migrate driver expects, carrying the configured connect and statement
// timeouts so no migration step can block forever.
func migrateDSN(cfg config.Postgres) (string, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return "", errors.New("parse POSTGRES_URL: invalid URL")
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", fmt.Errorf("POSTGRES_URL scheme %q: want postgres or postgresql", u.Scheme)
	}
	u.Scheme = "pgx5"
	q := u.Query()
	q.Set("connect_timeout", strconv.Itoa(int(math.Ceil(cfg.ConnectTimeout.Seconds()))))
	q.Set("x-statement-timeout", strconv.FormatInt(cfg.StatementTimeout.Milliseconds(), 10))
	u.RawQuery = q.Encode()
	return u.String(), nil
}
