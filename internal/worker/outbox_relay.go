package worker

import (
	"context"
	"log/slog"
	"time"
)

// RelayRunner publishes every recorded outbox event and reports how many it
// published. usecase.RelayOutbox implements it.
type RelayRunner interface {
	Execute(ctx context.Context) (int, error)
}

// OutboxMetrics counts published events. platform/observability implements
// it.
type OutboxMetrics interface {
	EventsPublished(n int)
}

// OutboxRelay periodically publishes the events recorded in the outbox.
// Every replica runs one; the outbox hands each a disjoint batch.
type OutboxRelay struct {
	relay    RelayRunner
	interval time.Duration
	metrics  OutboxMetrics
	tracer   Tracer
	log      *slog.Logger
}

// NewOutboxRelay returns an OutboxRelay that runs relay every interval.
func NewOutboxRelay(relay RelayRunner, interval time.Duration, metrics OutboxMetrics, tracer Tracer, log *slog.Logger) *OutboxRelay {
	return &OutboxRelay{relay: relay, interval: interval, metrics: metrics, tracer: tracer, log: log}
}

// Run blocks until ctx is cancelled. A failed run is logged and retried on
// the next tick: the events stay in the outbox until they are published.
func (r *OutboxRelay) Run(ctx context.Context) error {
	r.log.InfoContext(ctx, "outbox relay started", slog.String("interval", r.interval.String()))
	return runEvery(ctx, r.interval, r.tracer, "worker.outbox", r.tick)
}

func (r *OutboxRelay) tick(ctx context.Context) error {
	n, err := r.relay.Execute(ctx)
	if n > 0 {
		r.metrics.EventsPublished(n)
		r.log.DebugContext(ctx, "outbox events published", slog.Int("count", n))
	}
	if err != nil && ctx.Err() == nil {
		r.log.ErrorContext(ctx, "publish outbox events", slog.Any("error", err))
	}
	return err
}
