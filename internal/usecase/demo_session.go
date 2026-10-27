package usecase

import (
	"context"
	"fmt"
	"time"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

// DemoStock is how many units each clickable demo sale starts with: enough
// to reserve and confirm once, and to show a sold-out path if the visitor
// hammers the button.
const DemoStock = 5

// DemoSessionTTL is how long a demo JWT and its deal window stay usable.
const DemoSessionTTL = 30 * time.Minute

// DemoSession is what the public demo UI needs to drive one flash sale.
type DemoSession struct {
	Deal   domain.Deal
	UserID string
}

// StartDemo opens a fresh mini-deal and a buyer id for the public demo
// page (ADR 0015). Each call is isolated so visitors do not share stock.
type StartDemo struct {
	deals DealRepository
	gate  StockGate
	clock Clock
	ids   IDGenerator
}

// NewStartDemo wires the demo session use case.
func NewStartDemo(deals DealRepository, gate StockGate, clock Clock, ids IDGenerator) *StartDemo {
	return &StartDemo{deals: deals, gate: gate, clock: clock, ids: ids}
}

// Execute creates an active deal with DemoStock units and a unique buyer id.
func (uc *StartDemo) Execute(ctx context.Context) (DemoSession, error) {
	userID, err := uc.ids.New()
	if err != nil {
		return DemoSession{}, fmt.Errorf("demo user id: %w", err)
	}
	productID, err := uc.ids.New()
	if err != nil {
		return DemoSession{}, fmt.Errorf("demo product id: %w", err)
	}
	now := uc.clock.Now().UTC()
	price, err := domain.NewMoney(4999, "USD")
	if err != nil {
		return DemoSession{}, err
	}
	d, err := domain.NewDeal(domain.DealSpec{
		ProductID:    "demo-" + productID,
		Title:        "Demo headphones",
		Price:        price,
		TotalStock:   DemoStock,
		PerUserLimit: 1,
		StartsAt:     now.Add(-time.Minute),
		EndsAt:       now.Add(DemoSessionTTL),
	})
	if err != nil {
		return DemoSession{}, err
	}
	stored, err := uc.deals.Create(ctx, d)
	if err != nil {
		return DemoSession{}, fmt.Errorf("store demo deal: %w", err)
	}
	if err := uc.gate.Load(ctx, stored); err != nil {
		return DemoSession{}, fmt.Errorf("load demo stock: %w", err)
	}
	return DemoSession{Deal: stored, UserID: "demo-" + userID}, nil
}
