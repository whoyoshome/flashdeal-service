package usecase

import (
	"context"
	"fmt"
	"time"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

// activeDealsPageSize bounds how many active deals one query lists.
const activeDealsPageSize = 100

// forEachActiveDeal calls fn for every deal active at now, paging through
// them by ID, and stops at the first error.
func forEachActiveDeal(ctx context.Context, deals DealRepository, now time.Time, fn func(domain.Deal) error) error {
	for afterID := int64(0); ; {
		page, err := deals.ListActive(ctx, now, afterID, activeDealsPageSize)
		if err != nil {
			return fmt.Errorf("list active deals: %w", err)
		}
		for _, d := range page {
			if err := fn(d); err != nil {
				return err
			}
		}
		if len(page) < activeDealsPageSize {
			return nil
		}
		afterID = page[len(page)-1].ID()
	}
}
