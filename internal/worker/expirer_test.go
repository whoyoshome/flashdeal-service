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

	"github.com/whoyoshome/flashdeal-service/internal/worker"
)

// These tests run inside a synctest bubble: time is virtual and only moves
// when every goroutine in the bubble is blocked, so time.Sleep below advances
// the fake clock instantly and deterministically instead of waiting.

const interval = 5 * time.Second

type fakeRunner struct {
	calls atomic.Int64
	run   func(ctx context.Context, call int64) (int, error)
}

func (f *fakeRunner) Execute(ctx context.Context) (int, error) {
	call := f.calls.Add(1)
	if f.run == nil {
		return 0, nil
	}
	return f.run(ctx, call)
}

// startExpirer runs an Expirer until stop is called, which cancels it and
// returns what Run returned.
func startExpirer(ctx context.Context, runner *fakeRunner, metrics *fakeMetrics, logs *bytes.Buffer) (stop func() error) {
	ctx, cancel := context.WithCancel(ctx)
	log := slog.New(slog.NewTextHandler(logs, nil))
	done := make(chan error, 1)
	go func() { done <- worker.NewExpirer(runner, interval, metrics, log).Run(ctx) }()
	return func() error {
		cancel()
		return <-done
	}
}

func TestExpirer(t *testing.T) {
	t.Parallel()

	t.Run("runs the expiry once per interval until it is stopped", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			runner := &fakeRunner{}
			stop := startExpirer(t.Context(), runner, &fakeMetrics{}, &bytes.Buffer{})

			time.Sleep(3*interval + time.Second)
			synctest.Wait()
			require.EqualValues(t, 3, runner.calls.Load())

			require.NoError(t, stop(), "stopping is not a failure")
			require.EqualValues(t, 3, runner.calls.Load())
		})
	})

	t.Run("logs and counts how many reservations a run expired", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			runner := &fakeRunner{run: func(context.Context, int64) (int, error) { return 4, nil }}
			metrics := &fakeMetrics{}
			var logs bytes.Buffer
			stop := startExpirer(t.Context(), runner, metrics, &logs)

			time.Sleep(interval)
			synctest.Wait()
			require.NoError(t, stop())

			require.Contains(t, logs.String(), `msg="reservations expired" count=4`)
			require.EqualValues(t, 4, metrics.expired.Load())
		})
	})

	t.Run("logs a failed run and tries again on the next tick", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			runner := &fakeRunner{run: func(_ context.Context, call int64) (int, error) {
				if call == 1 {
					return 0, errors.New("connection reset")
				}
				return 0, nil
			}}
			var logs bytes.Buffer
			stop := startExpirer(t.Context(), runner, &fakeMetrics{}, &logs)

			time.Sleep(2 * interval)
			synctest.Wait()
			require.NoError(t, stop(), "a failed run must not stop the worker or the service")

			require.EqualValues(t, 2, runner.calls.Load())
			require.Contains(t, logs.String(), `level=ERROR msg="expire reservations" error="connection reset"`)
		})
	})

	t.Run("does not report an error caused by shutting down", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			runner := &fakeRunner{run: func(ctx context.Context, _ int64) (int, error) {
				cancel() // shutdown arrives while the batch runs
				return 0, ctx.Err()
			}}
			var logs bytes.Buffer
			stop := startExpirer(ctx, runner, &fakeMetrics{}, &logs)

			time.Sleep(interval)
			synctest.Wait()
			require.NoError(t, stop())

			require.NotContains(t, logs.String(), "level=ERROR")
		})
	})
}
