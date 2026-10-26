package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

// ReconcileRunner runs one reconciliation pass. usecase.Reconcile
// implements it.
type ReconcileRunner interface {
	Execute(ctx context.Context) (usecase.ReconcileResult, error)
}

// RepairMetrics counts stock gate repairs. platform/observability implements
// it.
type RepairMetrics interface {
	StockGateRepaired(n int)
}

// Reconciler periodically corrects the stock gate from PostgreSQL. Its
// interval is part of the correctness argument: passes must be further
// apart than any reservation, cancellation or expiry can take (see
// usecase.Reconcile).
type Reconciler struct {
	reconcile ReconcileRunner
	interval  time.Duration
	metrics   RepairMetrics
	log       *slog.Logger
}

// NewReconciler returns a Reconciler that runs reconcile every interval.
func NewReconciler(reconcile ReconcileRunner, interval time.Duration, metrics RepairMetrics, log *slog.Logger) *Reconciler {
	return &Reconciler{reconcile: reconcile, interval: interval, metrics: metrics, log: log}
}

// Run blocks until ctx is cancelled. A failed pass is logged and retried on
// the next tick.
func (r *Reconciler) Run(ctx context.Context) error {
	r.log.InfoContext(ctx, "stock reconciler started", slog.String("interval", r.interval.String()))
	return runEvery(ctx, r.interval, r.tick)
}

func (r *Reconciler) tick(ctx context.Context) {
	res, err := r.reconcile.Execute(ctx)
	if len(res.Repaired) > 0 {
		r.metrics.StockGateRepaired(len(res.Repaired))
	}
	// A repair means a gate update was lost somewhere: worth a warning even
	// though it is now fixed.
	for _, fix := range res.Repaired {
		r.log.WarnContext(ctx, "stock gate drift repaired",
			slog.Int64("deal_id", fix.DealID), slog.Int("gate_stock", fix.From), slog.Int("available", fix.To))
	}
	if err != nil && ctx.Err() == nil {
		r.log.ErrorContext(ctx, "reconcile stock gate", slog.Any("error", err))
	}
}
