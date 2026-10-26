package postgres

import (
	"context"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

var _ usecase.StockGate = (*StockGate)(nil)

// StockGate is the phase 3 baseline gate: it admits every request and leaves
// the stock decision to the conditional UPDATE in ReservationRepo.Create.
// Measuring this PostgreSQL-only path under load is the point: phase 4
// replaces it with the Redis gate and compares the numbers. Because nothing
// is held here, cumulative per-user limits are not enforced in this phase.
type StockGate struct{}

// NewStockGate returns the baseline gate.
func NewStockGate() *StockGate { return &StockGate{} }

// Load does nothing: PostgreSQL already holds the stock counters.
func (*StockGate) Load(context.Context, domain.Deal) error { return nil }

// TryReserve admits the request; ReservationRepo.Create decides stock.
func (*StockGate) TryReserve(context.Context, int64, string, int, int) error { return nil }

// Release does nothing because TryReserve took nothing.
func (*StockGate) Release(context.Context, int64, string, int) error { return nil }
