// Command api runs the FlashDeal HTTP service. This file is the composition
// root: it loads configuration, wires dependencies and owns the lifecycle.
//
// Subcommands reuse the same binary in images without a shell:
// `healthcheck` probes the local liveness endpoint, and `migrate` applies
// the embedded schema migrations.
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
	"github.com/whoyoshome/flashdeal-service/internal/delivery/httpapi/middleware"
	"github.com/whoyoshome/flashdeal-service/internal/platform/auth"
	"github.com/whoyoshome/flashdeal-service/internal/platform/clock"
	"github.com/whoyoshome/flashdeal-service/internal/platform/config"
	"github.com/whoyoshome/flashdeal-service/internal/platform/logger"
	"github.com/whoyoshome/flashdeal-service/internal/platform/observability"
	"github.com/whoyoshome/flashdeal-service/internal/platform/uuidgen"
	"github.com/whoyoshome/flashdeal-service/internal/repository/payment"
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
	// adminWriteTimeout leaves room for a CPU profile, 30s by default.
	adminWriteTimeout = 65 * time.Second
	// tracingFlushTimeout bounds exporting the last buffered spans at exit.
	tracingFlushTimeout = 5 * time.Second
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}

	command := run
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		command = migrateUp
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := command(ctx, os.Environ(), os.Stdout)
	stop()
	if err != nil {
		fmt.Fprintf(os.Stderr, "flashdeal: %v\n", err)
		os.Exit(1)
	}
}

// healthcheck probes the local liveness endpoint so Docker/Kubernetes can
// use the same binary as the process (distroless has no curl). The target is
// fixed to loopback:8080 — the only listener the container exposes for the API.
func healthcheck() int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8080/healthz", nil)
	if err != nil {
		return 1
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 1
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 1
	}
	return 0
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

	tokens, err := newAuthenticator(cfg.Auth)
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	// Telemetry. Tracing is set up first and shut down last, so spans from
	// the final requests and worker passes are still flushed.
	tracing, err := observability.NewTracing(ctx, cfg.Tracing)
	if err != nil {
		return fmt.Errorf("tracing: %w", err)
	}
	observability.ReportErrorsTo(log)
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tracingFlushTimeout)
		defer cancel()
		if err := tracing.Shutdown(flushCtx); err != nil {
			log.WarnContext(ctx, "flush traces", slog.Any("error", err))
		}
	}()
	metrics := observability.NewMetrics()

	// Infrastructure
	pool, err := postgres.NewPool(ctx, cfg.Postgres, tracing.PgxTracer())
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pool.Close()
	metrics.WatchPool(func() observability.PoolStats { return pool.Stat() })

	rdb, err := redisrepo.NewClient(ctx, cfg.Redis)
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	defer func() {
		if err := rdb.Close(); err != nil {
			log.WarnContext(ctx, "close redis client", slog.Any("error", err))
		}
	}()
	rdb.AddHook(tracing.RedisHook())

	// Adapters
	clk := clock.System{}
	ids := uuidgen.V7{}
	txm := postgres.NewTxManager(pool)
	dealRepo := postgres.NewDealRepo(pool, cfg.StockShards)
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
	confirm := usecase.NewConfirm(dealRepo, dealRepo, resRepo, orderRepo, payment.NewAlwaysApprove(), txm, clk, ids)
	cancelReservation := usecase.NewCancel(dealRepo, resRepo, gate, txm, clk, log)
	expire := usecase.NewExpire(resRepo, dealRepo, gate, txm, clk, cfg.ExpireBatchSize, log)
	reconcile := usecase.NewReconcile(dealRepo, dealRepo, gate, clk)
	stockLevels := usecase.NewStockLevels(dealRepo, gate, clk)
	relayOutbox := usecase.NewRelayOutbox(postgres.NewOutboxRepo(pool),
		redisrepo.NewEventPublisher(rdb, cfg.Outbox.Stream, cfg.Outbox.StreamMaxLen), txm, cfg.Outbox.BatchSize)

	var limiter middleware.RateLimiter
	if cfg.RateLimitRPS > 0 {
		limiter = rateLimitBridge{redisrepo.NewRateLimiter(rdb, cfg.RateLimitRPS, cfg.RateLimitBurst)}
	}

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
			Tokens:         tokens,
			RateLimiter:    limiter,
			Idempotency:    redisrepo.NewIdempotencyStore(rdb),
			IdempotencyTTL: cfg.IdempotencyTTL,
			RequestTimeout: cfg.RequestTimeout,
			Metrics:        metrics,
			MetricsHandler: metrics.Handler(),
			Tracing:        tracing.HTTPMiddleware,
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
	// Profiling lives on its own listener so it can stay private while the API
	// is public.
	admin := &http.Server{
		Addr:              cfg.AdminAddr,
		Handler:           observability.NewAdminHandler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      adminWriteTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}

	// Lifecycle: the servers and the workers share gctx. If any component
	// fails, gctx is cancelled and the rest shut down in order.
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return listen(gctx, log, "http", srv) })
	g.Go(func() error { return listen(gctx, log, "admin", admin) })
	g.Go(func() error { return worker.NewExpirer(expire, cfg.ExpireEvery, metrics, tracing, log).Run(gctx) })
	g.Go(func() error {
		return worker.NewReconciler(reconcile, cfg.ReconcileEvery, metrics, tracing, log).Run(gctx)
	})
	g.Go(func() error {
		return worker.NewStockSampler(stockLevels, metrics, cfg.StockSampleEvery, tracing, log).Run(gctx)
	})
	g.Go(func() error {
		return worker.NewOutboxRelay(relayOutbox, cfg.Outbox.Every, metrics, tracing, log).Run(gctx)
	})
	g.Go(func() error {
		<-gctx.Done()
		log.InfoContext(gctx, "shutting down", slog.String("timeout", cfg.ShutdownTimeout.String()))
		// gctx is already cancelled; in-flight requests get their own budget.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(gctx), cfg.ShutdownTimeout)
		defer cancel()
		return errors.Join(shutdown(shutdownCtx, "http", srv), shutdown(shutdownCtx, "admin", admin))
	})

	if err := g.Wait(); err != nil {
		return err
	}
	log.InfoContext(ctx, "stopped")
	return nil
}

// listen serves srv until it is shut down.
func listen(ctx context.Context, log *slog.Logger, name string, srv *http.Server) error {
	log.InfoContext(ctx, "server listening", slog.String("server", name), slog.String("addr", srv.Addr))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("%s server: %w", name, err)
	}
	return nil
}

func shutdown(ctx context.Context, name string, srv *http.Server) error {
	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("%s shutdown: %w", name, err)
	}
	return nil
}

func newAuthenticator(cfg config.Auth) (middleware.Authenticator, error) {
	if cfg.JWKSURL != "" {
		return auth.NewJWKS(auth.JWKSConfig{URL: cfg.JWKSURL, Issuer: cfg.Issuer})
	}
	return auth.New(auth.Config{Secret: cfg.Secret, Issuer: cfg.Issuer})
}

// rateLimitBridge adapts the Redis token bucket to the HTTP middleware port
// without letting delivery import redisrepo (or the reverse).
type rateLimitBridge struct{ lim *redisrepo.RateLimiter }

func (b rateLimitBridge) Allow(ctx context.Context, subject string) (middleware.RateLimitResult, error) {
	d, err := b.lim.Allow(ctx, subject)
	if err != nil {
		return middleware.RateLimitResult{}, err
	}
	return middleware.RateLimitResult{
		Allowed:    d.Allowed,
		Remaining:  d.Remaining,
		RetryAfter: d.RetryAfter,
		Limit:      d.Limit,
	}, nil
}
