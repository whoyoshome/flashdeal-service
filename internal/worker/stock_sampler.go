package worker

import (
	"context"
	"log/slog"
	"time"
)

// StockReader reports the units left per active deal. usecase.StockLevels
// implements it.
type StockReader interface {
	Execute(ctx context.Context) (map[int64]int, error)
}

// StockGauge publishes the units left per deal. platform/observability
// implements it.
type StockGauge interface {
	SetStockLevels(levels map[int64]int)
}

// StockSampler periodically publishes how many units each active deal has
// left, so a dashboard can show a deal selling out. Sampling on a timer
// keeps the cost fixed (one read per deal per interval) no matter how many
// reservations arrive or how often metrics are scraped.
type StockSampler struct {
	levels   StockReader
	gauge    StockGauge
	interval time.Duration
	log      *slog.Logger
}

// NewStockSampler returns a StockSampler that samples every interval.
func NewStockSampler(levels StockReader, gauge StockGauge, interval time.Duration, log *slog.Logger) *StockSampler {
	return &StockSampler{levels: levels, gauge: gauge, interval: interval, log: log}
}

// Run blocks until ctx is cancelled. A failed sample keeps the previous
// values and is logged; the next tick tries again.
func (s *StockSampler) Run(ctx context.Context) error {
	return runEvery(ctx, s.interval, s.tick)
}

func (s *StockSampler) tick(ctx context.Context) {
	levels, err := s.levels.Execute(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.WarnContext(ctx, "sample stock levels", slog.Any("error", err))
		}
		return
	}
	s.gauge.SetStockLevels(levels)
}
