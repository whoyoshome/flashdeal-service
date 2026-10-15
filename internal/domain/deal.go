package domain

import (
	"errors"
	"time"
)

// DealSpec holds what an admin provides to create a deal.
type DealSpec struct {
	ProductID    string
	Title        string
	Price        Money
	TotalStock   int
	PerUserLimit int
	StartsAt     time.Time
	EndsAt       time.Time
}

// DealState is a deal as persisted, used to rebuild the entity from storage.
type DealState struct {
	ID           int64
	ProductID    string
	Title        string
	Price        Money
	TotalStock   int
	Reserved     int
	Sold         int
	PerUserLimit int
	StartsAt     time.Time
	EndsAt       time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Deal is a limited stock of one product sold at a fixed price during a time
// window. Its counters are read-only here: stock only moves through atomic
// operations in the stock gate and in PostgreSQL, never in process memory.
type Deal struct {
	id           int64
	productID    string
	title        string
	price        Money
	totalStock   int
	reserved     int
	sold         int
	perUserLimit int
	startsAt     time.Time
	endsAt       time.Time
	createdAt    time.Time
	updatedAt    time.Time
}

// NewDeal validates spec and returns an unsaved deal. It has no ID until
// storage assigns one.
func NewDeal(spec DealSpec) (Deal, error) {
	if err := spec.validate(); err != nil {
		return Deal{}, err
	}
	return dealFromSpec(spec), nil
}

func dealFromSpec(spec DealSpec) Deal {
	return Deal{
		productID:    spec.ProductID,
		title:        spec.Title,
		price:        spec.Price,
		totalStock:   spec.TotalStock,
		perUserLimit: spec.PerUserLimit,
		startsAt:     spec.StartsAt.UTC(),
		endsAt:       spec.EndsAt.UTC(),
	}
}

// RestoreDeal rebuilds a deal from storage. Invariant violations mean the
// stored data is corrupt, so they are reported as ErrCorruptState rather than
// as client validation errors.
func RestoreDeal(s DealState) (Deal, error) {
	spec := DealSpec{
		ProductID:    s.ProductID,
		Title:        s.Title,
		Price:        s.Price,
		TotalStock:   s.TotalStock,
		PerUserLimit: s.PerUserLimit,
		StartsAt:     s.StartsAt,
		EndsAt:       s.EndsAt,
	}
	err := spec.validate()
	switch {
	case err != nil:
	case s.ID <= 0:
		err = errors.New("id must be positive")
	case s.Reserved < 0 || s.Sold < 0:
		err = errors.New("counters must not be negative")
	case s.Reserved+s.Sold > s.TotalStock:
		err = errors.New("reserved plus sold exceeds total stock")
	}
	if err != nil {
		return Deal{}, corrupt("deal", s.ID, err)
	}

	d := dealFromSpec(spec)
	d.id = s.ID
	d.reserved = s.Reserved
	d.sold = s.Sold
	d.createdAt = s.CreatedAt.UTC()
	d.updatedAt = s.UpdatedAt.UTC()
	return d, nil
}

func (s DealSpec) validate() error {
	switch {
	case s.ProductID == "":
		return NewValidation("product_id is required")
	case s.Title == "":
		return NewValidation("title is required")
	case s.Price.Amount() <= 0:
		// Money can only be built valid by NewMoney; its zero value (no
		// currency) has a zero amount and is rejected here too.
		return NewValidation("price must be positive")
	case s.TotalStock <= 0:
		return NewValidation("total_stock must be positive")
	case s.PerUserLimit <= 0:
		return NewValidation("per_user_limit must be positive")
	case !s.EndsAt.After(s.StartsAt):
		return NewValidation("ends_at must be after starts_at")
	}
	return nil
}

// ID returns the storage identity; zero for a deal not yet saved.
func (d Deal) ID() int64 { return d.id }

// ProductID returns the catalog product this deal sells.
func (d Deal) ProductID() string { return d.productID }

// Title returns the display title.
func (d Deal) Title() string { return d.title }

// Price returns the unit price.
func (d Deal) Price() Money { return d.price }

// TotalStock returns the number of units offered.
func (d Deal) TotalStock() int { return d.totalStock }

// Reserved returns the units held by pending reservations.
func (d Deal) Reserved() int { return d.reserved }

// Sold returns the units in confirmed orders.
func (d Deal) Sold() int { return d.sold }

// PerUserLimit returns the maximum units one user may hold.
func (d Deal) PerUserLimit() int { return d.perUserLimit }

// StartsAt returns when the deal opens (inclusive).
func (d Deal) StartsAt() time.Time { return d.startsAt }

// EndsAt returns when the deal closes (exclusive).
func (d Deal) EndsAt() time.Time { return d.endsAt }

// CreatedAt returns when the deal was stored.
func (d Deal) CreatedAt() time.Time { return d.createdAt }

// UpdatedAt returns when the deal was last changed in storage.
func (d Deal) UpdatedAt() time.Time { return d.updatedAt }

// Available returns the units neither reserved nor sold at snapshot time.
// It is informational only: the stock gate is the authority on stock.
func (d Deal) Available() int { return d.totalStock - d.reserved - d.sold }

// IsActive reports whether now falls in the window [StartsAt, EndsAt).
func (d Deal) IsActive(now time.Time) bool {
	return !now.Before(d.startsAt) && now.Before(d.endsAt)
}

// CanReserve checks the rules a reservation request must meet before it
// reaches the stock gate. It deliberately ignores Available: the deal may
// come from a short-lived cache, and a stale snapshot must not turn into a
// false SOLD_OUT. Stock is decided atomically by the gate.
func (d Deal) CanReserve(now time.Time, qty int) error {
	if qty <= 0 {
		return NewValidation("quantity must be positive")
	}
	if !d.IsActive(now) {
		return ErrDealNotActive
	}
	if qty > d.perUserLimit {
		return ErrUserLimitReached
	}
	return nil
}
