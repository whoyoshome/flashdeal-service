package usecase

import (
	"context"
	"fmt"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

// StockLevels reports how many units each active deal has left in the stock
// gate. The gate, not PostgreSQL, is what buyers race against, so it is the
// number to watch while a deal sells out.
type StockLevels struct {
	deals DealRepository
	gate  GateRepair
	clock Clock
}

// NewStockLevels wires the stock report.
func NewStockLevels(deals DealRepository, gate GateRepair, clock Clock) *StockLevels {
	return &StockLevels{deals: deals, gate: gate, clock: clock}
}

// Execute returns the units left per active deal, keyed by deal ID. Deals
// whose stock is not in the gate (not activated yet) are left out.
func (uc *StockLevels) Execute(ctx context.Context) (map[int64]int, error) {
	levels := map[int64]int{}
	err := forEachActiveDeal(ctx, uc.deals, uc.clock.Now(), func(d domain.Deal) error {
		stock, loaded, err := uc.gate.Stock(ctx, d.ID())
		if err != nil {
			return fmt.Errorf("read gate stock of deal %d: %w", d.ID(), err)
		}
		if loaded {
			levels[d.ID()] = stock
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return levels, nil
}
