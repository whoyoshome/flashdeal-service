package worker_test

import (
	"context"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
)

// fakeMetrics records what the workers publish.
type fakeMetrics struct {
	expired  atomic.Int64
	repaired atomic.Int64

	mu     sync.Mutex
	levels map[int64]int
	sets   int
}

func (m *fakeMetrics) ReservationsExpired(n int) { m.expired.Add(int64(n)) }

func (m *fakeMetrics) StockGateRepaired(n int) { m.repaired.Add(int64(n)) }

func (m *fakeMetrics) SetStockLevels(levels map[int64]int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.levels = maps.Clone(levels)
	m.sets++
}

func (m *fakeMetrics) published() (map[int64]int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.levels, m.sets
}

// fakeTracer records the spans the workers start and how they end.
type fakeTracer struct {
	mu    sync.Mutex
	spans []string
	errs  []error
}

func (f *fakeTracer) StartSpan(ctx context.Context, name string) (context.Context, func(error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spans = append(f.spans, name)
	return ctx, func(err error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.errs = append(f.errs, err)
	}
}

func (f *fakeTracer) ended() ([]string, []error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.spans), slices.Clone(f.errs)
}
