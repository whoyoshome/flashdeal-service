package worker_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/worker"
)

const relayInterval = time.Second

func startRelay(ctx context.Context, runner *fakeRunner, metrics *fakeMetrics, tracer *fakeTracer, logs *bytes.Buffer) (stop func() error) {
	ctx, cancel := context.WithCancel(ctx)
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	done := make(chan error, 1)
	go func() { done <- worker.NewOutboxRelay(runner, relayInterval, metrics, tracer, log).Run(ctx) }()
	return func() error {
		cancel()
		return <-done
	}
}

func TestOutboxRelay(t *testing.T) {
	t.Parallel()

	t.Run("relays once per interval and counts the published events", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			runner := &fakeRunner{run: func(context.Context, int64) (int, error) { return 3, nil }}
			metrics := &fakeMetrics{}
			tracer := &fakeTracer{}
			stop := startRelay(t.Context(), runner, metrics, tracer, &bytes.Buffer{})

			time.Sleep(2*relayInterval + relayInterval/2)
			synctest.Wait()
			require.NoError(t, stop(), "stopping is not a failure")

			require.EqualValues(t, 2, runner.calls.Load())
			require.EqualValues(t, 6, metrics.events.Load())
			spans, _ := tracer.ended()
			require.Equal(t, []string{"worker.outbox", "worker.outbox"}, spans)
		})
	})

	t.Run("logs a failed run and tries again on the next tick", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			runner := &fakeRunner{run: func(_ context.Context, call int64) (int, error) {
				if call == 1 {
					return 0, errors.New("redis down")
				}
				return 1, nil
			}}
			metrics := &fakeMetrics{}
			var logs bytes.Buffer
			stop := startRelay(t.Context(), runner, metrics, &fakeTracer{}, &logs)

			time.Sleep(2 * relayInterval)
			synctest.Wait()
			require.NoError(t, stop(), "a failed run must not stop the worker or the service")

			require.Contains(t, logs.String(), `level=ERROR msg="publish outbox events" error="redis down"`)
			require.EqualValues(t, 1, metrics.events.Load())
		})
	})

	t.Run("does not report an error caused by shutting down", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			runner := &fakeRunner{run: func(ctx context.Context, _ int64) (int, error) {
				cancel()
				return 0, ctx.Err()
			}}
			var logs bytes.Buffer
			stop := startRelay(ctx, runner, &fakeMetrics{}, &fakeTracer{}, &logs)

			time.Sleep(relayInterval)
			synctest.Wait()
			require.NoError(t, stop())

			require.NotContains(t, logs.String(), "level=ERROR")
		})
	})
}
