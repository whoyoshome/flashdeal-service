package worker

import (
	"context"
	"log/slog"
	"time"
)

// ExpireRunner expires every reservation due now and reports how many it
// expired. usecase.Expire implements it.
type ExpireRunner interface {
	Execute(ctx context.Context) (int, error)
}

// ExpiryMetrics counts expired reservations. platform/observability
// implements it.
type ExpiryMetrics interface {
	ReservationsExpired(n int)
}

// Expirer periodically returns the units of abandoned reservations to their
// deals. Every replica runs one; the expiry queue hands each a disjoint
// batch, so they need no coordination.
type Expirer struct {
	expire   ExpireRunner
	interval time.Duration
	metrics  ExpiryMetrics
	tracer   Tracer
	log      *slog.Logger
}

// NewExpirer returns an Expirer that runs expire every interval.
func NewExpirer(expire ExpireRunner, interval time.Duration, metrics ExpiryMetrics, tracer Tracer, log *slog.Logger) *Expirer {
	return &Expirer{expire: expire, interval: interval, metrics: metrics, tracer: tracer, log: log}
}

// Run blocks until ctx is cancelled. A failed run is logged and retried on
// the next tick instead of being returned: a database hiccup must not take
// the whole service down with it.
func (e *Expirer) Run(ctx context.Context) error {
	e.log.InfoContext(ctx, "reservation expirer started", slog.String("interval", e.interval.String()))
	return runEvery(ctx, e.interval, e.tracer, "worker.expire", e.tick)
}

func (e *Expirer) tick(ctx context.Context) error {
	n, err := e.expire.Execute(ctx)
	if n > 0 {
		e.metrics.ReservationsExpired(n)
		e.log.InfoContext(ctx, "reservations expired", slog.Int("count", n))
	}
	// An error caused by shutdown is expected: the rolled-back batch is
	// picked up again after the restart.
	if err != nil && ctx.Err() == nil {
		e.log.ErrorContext(ctx, "expire reservations", slog.Any("error", err))
	}
	return err
}
