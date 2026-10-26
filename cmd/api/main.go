// Command api runs the FlashDeal HTTP service. This file is the composition
// root: it loads configuration, wires dependencies and owns the lifecycle.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/whoyoshome/flashdeal-service/internal/delivery/httpapi"
	"github.com/whoyoshome/flashdeal-service/internal/platform/clock"
	"github.com/whoyoshome/flashdeal-service/internal/platform/config"
	"github.com/whoyoshome/flashdeal-service/internal/platform/logger"
	"github.com/whoyoshome/flashdeal-service/internal/platform/uuidgen"
	"github.com/whoyoshome/flashdeal-service/internal/repository/postgres"
	"github.com/whoyoshome/flashdeal-service/internal/repository/redisrepo"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
	"github.com/whoyoshome/flashdeal-service/internal/worker"
)

// Server timeouts protect against slow clients holding connections open.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 10 * time.Second
	idleTimeout       = 60 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Environ(), os.Stdout)
	stop()
	if err != nil {
		fmt.Fprintf(os.Stderr, "flashdeal: %v\n", err)
		os.Exit(1)
	}
}

// run wires and starts the service and blocks until ctx is cancelled or a
// component fails. Returning an error instead of exiting keeps every defer
// running and makes startup testable.
func run(ctx context.Context, environ []string, stdout io.Writer) error {
	cfg, err := config.Load(environ)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	log := logger.New(stdout, cfg.LogLevel)

	// Infrastructure
	pool, err := postgres.NewPool(ctx, cfg.Postgres)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pool.Close()

	rdb, err := redisrepo.NewClient(ctx, cfg.Redis)
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	defer func() {
		if err := rdb.Close(); err != nil {
			log.WarnContext(ctx, "close redis client", slog.Any("error", err))
		}
	}()

	// Adapters
	clk := clock.System{}
	ids := uuidgen.V7{}
	txm := postgres.NewTxManager(pool)
	dealRepo := postgres.NewDealRepo(pool)
	resRepo := postgres.NewReservationRepo(pool)
	orderRepo := postgres.NewOrderRepo(pool)
	// Redis rejects most requests in memory; only as many as there are units
	// reach PostgreSQL, whose constraints remain the source of truth.
	gate := redisrepo.NewStockGate(rdb)

	// Use cases
	// Hot single-deal reads go through the cache; activation and confirmation
	// read the database directly because they need current data.
	dealCache := redisrepo.NewDealCache(rdb, dealRepo, cfg.DealCacheTTL)
	manageDeals := usecase.NewManageDeals(dealRepo, gate, clk)
	dealQueries := usecase.NewDealQueries(dealCache, dealRepo, clk)
	reserve := usecase.NewReserve(dealCache, gate, resRepo, clk, ids, cfg.ReservationTTL, log)
	confirm := usecase.NewConfirm(dealRepo, dealRepo, resRepo, orderRepo, txm, clk, ids)
	cancelReservation := usecase.NewCancel(dealRepo, resRepo, gate, txm, clk, log)
	expire := usecase.NewExpire(resRepo, dealRepo, gate, txm, clk, cfg.ExpireBatchSize, log)

	// Delivery
	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpapi.NewRouter(httpapi.Deps{
			Log:            log,
			Deals:          manageDeals,
			Queries:        dealQueries,
			Reserve:        reserve,
			Confirm:        confirm,
			Cancel:         cancelReservation,
			Idempotency:    redisrepo.NewIdempotencyStore(rdb),
			IdempotencyTTL: cfg.IdempotencyTTL,
			RequestTimeout: cfg.RequestTimeout,
			ReadinessChecks: []httpapi.ReadinessCheck{
				{Name: "postgres", Pinger: pool},
				{Name: "redis", Pinger: redisrepo.NewPinger(rdb)},
			},
			ReadinessTimeout: cfg.ReadinessTimeout,
		}),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}

	// Lifecycle: the server and the workers share gctx. If any component
	// fails, gctx is cancelled and the rest shut down in order.
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		log.InfoContext(gctx, "http server listening", slog.String("addr", cfg.HTTPAddr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	})
	g.Go(func() error { return worker.NewExpirer(expire, cfg.ExpireEvery, log).Run(gctx) })
	g.Go(func() error {
		<-gctx.Done()
		log.InfoContext(gctx, "shutting down", slog.String("timeout", cfg.ShutdownTimeout.String()))
		// gctx is already cancelled; in-flight requests get their own budget.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(gctx), cfg.ShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("http shutdown: %w", err)
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		return err
	}
	log.InfoContext(ctx, "stopped")
	return nil
}
