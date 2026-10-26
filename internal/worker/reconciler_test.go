package worker_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/usecase"
	"github.com/whoyoshome/flashdeal-service/internal/worker"
)

type fakeReconcile struct {
	calls atomic.Int64
	run   func(ctx context.Context, call int64) (usecase.ReconcileResult, error)
}

func (f *fakeReconcile) Execute(ctx context.Context) (usecase.ReconcileResult, error) {
	call := f.calls.Add(1)
	if f.run == nil {
		return usecase.ReconcileResult{}, nil
	}
	return f.run(ctx, call)
}

func startReconciler(ctx context.Context, runner *fakeReconcile, metrics *fakeMetrics, logs *bytes.Buffer) (stop func() error) {
	ctx, cancel := context.WithCancel(ctx)
	log := slog.New(slog.NewTextHandler(logs, nil))
	done := make(chan error, 1)
	go func() { done <- worker.NewReconciler(runner, interval, metrics, log).Run(ctx) }()
	return func() error {
		cancel()
		return <-done
	}
}

// See expirer_test.go: these run in a synctest bubble with virtual time.
func TestReconciler(t *testing.T) {
	t.Parallel()

	t.Run("runs a pass once per interval until it is stopped", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			runner := &fakeReconcile{}
			stop := startReconciler(t.Context(), runner, &fakeMetrics{}, &bytes.Buffer{})

			time.Sleep(2*interval + time.Second)
			synctest.Wait()
			require.EqualValues(t, 2, runner.calls.Load())

			require.NoError(t, stop())
		})
	})

	t.Run("warns about and counts every repaired deal", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			runner := &fakeReconcile{run: func(context.Context, int64) (usecase.ReconcileResult, error) {
				return usecase.ReconcileResult{Checked: 3, Drifting: 1, Repaired: []usecase.StockRepair{{DealID: 7, From: 3, To: 5}}}, nil
			}}
			metrics := &fakeMetrics{}
			var logs bytes.Buffer
			stop := startReconciler(t.Context(), runner, metrics, &logs)

			time.Sleep(interval)
			synctest.Wait()
			require.NoError(t, stop())

			require.Contains(t, logs.String(), `level=WARN msg="stock gate drift repaired" deal_id=7 gate_stock=3 available=5`)
			require.EqualValues(t, 1, metrics.repaired.Load())
		})
	})

	t.Run("logs a failed pass, keeps its repairs and tries again on the next tick", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			runner := &fakeReconcile{run: func(_ context.Context, call int64) (usecase.ReconcileResult, error) {
				if call == 1 {
					return usecase.ReconcileResult{Repaired: []usecase.StockRepair{{DealID: 7, From: 3, To: 5}}}, errors.New("redis down")
				}
				return usecase.ReconcileResult{}, nil
			}}
			var logs bytes.Buffer
			stop := startReconciler(t.Context(), runner, &fakeMetrics{}, &logs)

			time.Sleep(2 * interval)
			synctest.Wait()
			require.NoError(t, stop())

			require.EqualValues(t, 2, runner.calls.Load())
			require.Contains(t, logs.String(), `msg="stock gate drift repaired" deal_id=7`)
			require.Contains(t, logs.String(), `level=ERROR msg="reconcile stock gate" error="redis down"`)
		})
	})

	t.Run("does not report an error caused by shutting down", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			runner := &fakeReconcile{run: func(ctx context.Context, _ int64) (usecase.ReconcileResult, error) {
				cancel()
				return usecase.ReconcileResult{}, ctx.Err()
			}}
			var logs bytes.Buffer
			stop := startReconciler(ctx, runner, &fakeMetrics{}, &logs)

			time.Sleep(interval)
			synctest.Wait()
			require.NoError(t, stop())

			require.NotContains(t, logs.String(), "level=ERROR")
		})
	})
}
