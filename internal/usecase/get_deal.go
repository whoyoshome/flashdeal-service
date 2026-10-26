package usecase

import (
	"context"
	"fmt"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

// Page size bounds for listing deals. The cap protects the database from
// clients asking for unbounded pages.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// DealPage is one page of active deals. NextCursor is the value to pass as
// the next request's cursor, or 0 when there are no more pages.
type DealPage struct {
	Deals      []domain.Deal
	NextCursor int64
}

// DealQueries answers read-only questions about deals.
type DealQueries struct {
	reader DealReader
	deals  DealRepository
	clock  Clock
}

// NewDealQueries wires the deal read operations. Single-deal reads go through
// reader, which may be a cache: they are the hot read when a deal opens.
// Listing always reads deals directly.
func NewDealQueries(reader DealReader, deals DealRepository, clock Clock) *DealQueries {
	return &DealQueries{reader: reader, deals: deals, clock: clock}
}

// Get returns one deal by ID.
func (uc *DealQueries) Get(ctx context.Context, id int64) (domain.Deal, error) {
	d, err := uc.reader.GetByID(ctx, id)
	if err != nil {
		return domain.Deal{}, fmt.Errorf("load deal %d: %w", id, err)
	}
	return d, nil
}

// ListActive returns deals active now, after the cursor. A non-positive
// limit means DefaultPageSize; larger limits are capped at MaxPageSize.
func (uc *DealQueries) ListActive(ctx context.Context, cursor int64, limit int) (DealPage, error) {
	if cursor < 0 {
		return DealPage{}, domain.NewValidation("cursor must not be negative")
	}
	switch {
	case limit <= 0:
		limit = DefaultPageSize
	case limit > MaxPageSize:
		limit = MaxPageSize
	}

	deals, err := uc.deals.ListActive(ctx, uc.clock.Now(), cursor, limit)
	if err != nil {
		return DealPage{}, fmt.Errorf("list active deals: %w", err)
	}
	page := DealPage{Deals: deals}
	// A full page may have a successor; a short page is the last one.
	if len(deals) == limit {
		page.NextCursor = deals[len(deals)-1].ID()
	}
	return page, nil
}
