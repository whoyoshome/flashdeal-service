package worker_test

import (
	"maps"
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
