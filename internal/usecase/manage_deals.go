package usecase

import (
	"context"
	"fmt"
	"time"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

// CreateDealInput is what an admin submits to create a deal.
type CreateDealInput struct {
	ProductID    string
	Title        string
	PriceCents   int64
	Currency     string
	TotalStock   int
	PerUserLimit int
	StartsAt     time.Time
	EndsAt       time.Time
}

// ManageDeals holds the admin operations on deals.
type ManageDeals struct {
	deals DealRepository
	gate  StockGate
	clock Clock
}

// NewManageDeals wires the admin deal operations.
func NewManageDeals(deals DealRepository, gate StockGate, clock Clock) *ManageDeals {
	return &ManageDeals{deals: deals, gate: gate, clock: clock}
}

// Create validates in and stores a new deal.
func (uc *ManageDeals) Create(ctx context.Context, in CreateDealInput) (domain.Deal, error) {
	price, err := domain.NewMoney(in.PriceCents, in.Currency)
	if err != nil {
		return domain.Deal{}, err
	}
	d, err := domain.NewDeal(domain.DealSpec{
		ProductID:    in.ProductID,
		Title:        in.Title,
		Price:        price,
		TotalStock:   in.TotalStock,
		PerUserLimit: in.PerUserLimit,
		StartsAt:     in.StartsAt,
		EndsAt:       in.EndsAt,
	})
	if err != nil {
		return domain.Deal{}, err
	}
	stored, err := uc.deals.Create(ctx, d)
	if err != nil {
		return domain.Deal{}, fmt.Errorf("store deal: %w", err)
	}
	return stored, nil
}

// Activate opens a deal's stock in the gate. A deal whose window already
// closed cannot be activated and returns domain.ErrDealNotActive. Activating
// twice is safe: loading the gate is idempotent.
func (uc *ManageDeals) Activate(ctx context.Context, id int64) (domain.Deal, error) {
	d, err := uc.deals.GetByID(ctx, id)
	if err != nil {
		return domain.Deal{}, fmt.Errorf("load deal %d: %w", id, err)
	}
	if !uc.clock.Now().Before(d.EndsAt()) {
		return domain.Deal{}, domain.ErrDealNotActive
	}
	if err := uc.gate.Load(ctx, d); err != nil {
		return domain.Deal{}, fmt.Errorf("load stock for deal %d: %w", id, err)
	}
	return d, nil
}
