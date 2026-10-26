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

type fakeStock struct {
	calls atomic.Int64
	run   func(call int64) (map[int64]int, error)
}

func (f *fakeStock) Execute(context.Context) (map[int64]int, error) {
	return f.run(f.calls.Add(1))
}

func startSampler(ctx context.Context, levels *fakeStock, gauge *fakeMetrics, tracer *fakeTracer, logs *bytes.Buffer) (stop func() error) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- worker.NewStockSampler(levels, gauge, interval, tracer, slog.New(slog.NewTextHandler(logs, nil))).Run(ctx)
	}()
	return func() error {
		cancel()
		return <-done
	}
}

// See expirer_test.go: these run in a synctest bubble with virtual time.
func TestStockSampler(t *testing.T) {
	t.Parallel()

	t.Run("publishes the latest stock levels every interval", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			levels := &fakeStock{run: func(call int64) (map[int64]int, error) {
				return map[int64]int{7: 10 - int(call)}, nil
			}}
			gauge := &fakeMetrics{}
			tracer := &fakeTracer{}
			stop := startSampler(t.Context(), levels, gauge, tracer, &bytes.Buffer{})

			time.Sleep(3 * interval)
			synctest.Wait()
			require.NoError(t, stop())

			published, sets := gauge.published()
			require.Equal(t, 3, sets)
			require.Equal(t, map[int64]int{7: 7}, published)
			spans, _ := tracer.ended()
			require.Equal(t, []string{"worker.sample_stock", "worker.sample_stock", "worker.sample_stock"}, spans)
		})
	})

	t.Run("keeps the last levels and warns when a sample fails", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			levels := &fakeStock{run: func(call int64) (map[int64]int, error) {
				if call == 2 {
					return nil, errors.New("redis down")
				}
				return map[int64]int{7: 5}, nil
			}}
			gauge := &fakeMetrics{}
			var logs bytes.Buffer
			stop := startSampler(t.Context(), levels, gauge, &fakeTracer{}, &logs)

			time.Sleep(2 * interval)
			synctest.Wait()
			require.NoError(t, stop())

			published, sets := gauge.published()
			require.Equal(t, 1, sets)
			require.Equal(t, map[int64]int{7: 5}, published)
			require.Contains(t, logs.String(), `level=WARN msg="sample stock levels" error="redis down"`)
		})
	})
}
