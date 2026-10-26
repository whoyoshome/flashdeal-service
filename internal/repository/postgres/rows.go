package postgres

import (
	"fmt"
	"time"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

// Row structs mirror table columns and never leave this package; they are
// mapped explicitly to domain entities through the Restore constructors.

const dealColumns = `id, product_id, title, price_cents, currency, total_stock, reserved, sold,
	per_user_limit, starts_at, ends_at, created_at, updated_at`

type dealRow struct {
	ID           int64
	ProductID    string
	Title        string
	PriceCents   int64
	Currency     string
	TotalStock   int
	Reserved     int
	Sold         int
	PerUserLimit int
	StartsAt     time.Time
	EndsAt       time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// scanTargets returns pointers in dealColumns order.
func (r *dealRow) scanTargets() []any {
	return []any{
		&r.ID, &r.ProductID, &r.Title, &r.PriceCents, &r.Currency, &r.TotalStock, &r.Reserved, &r.Sold,
		&r.PerUserLimit, &r.StartsAt, &r.EndsAt, &r.CreatedAt, &r.UpdatedAt,
	}
}

func (r dealRow) toDomain() (domain.Deal, error) {
	price, err := domain.NewMoney(r.PriceCents, r.Currency)
	if err != nil {
		return domain.Deal{}, fmt.Errorf("%w: deal %d price: %s", domain.ErrCorruptState, r.ID, err.Error())
	}
	return domain.RestoreDeal(domain.DealState{
		ID:           r.ID,
		ProductID:    r.ProductID,
		Title:        r.Title,
		Price:        price,
		TotalStock:   r.TotalStock,
		Reserved:     r.Reserved,
		Sold:         r.Sold,
		PerUserLimit: r.PerUserLimit,
		StartsAt:     r.StartsAt,
		EndsAt:       r.EndsAt,
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
	})
}

const reservationColumns = `id::text, deal_id, user_id, quantity, status, idempotency_key,
	expires_at, created_at, updated_at`

type reservationRow struct {
	ID             string
	DealID         int64
	UserID         string
	Quantity       int
	Status         string
	IdempotencyKey string
	ExpiresAt      time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// scanTargets returns pointers in reservationColumns order.
func (r *reservationRow) scanTargets() []any {
	return []any{
		&r.ID, &r.DealID, &r.UserID, &r.Quantity, &r.Status, &r.IdempotencyKey,
		&r.ExpiresAt, &r.CreatedAt, &r.UpdatedAt,
	}
}

func (r reservationRow) toDomain() (domain.Reservation, error) {
	return domain.RestoreReservation(domain.ReservationState{
		ID:             r.ID,
		DealID:         r.DealID,
		UserID:         r.UserID,
		Quantity:       r.Quantity,
		Status:         domain.ReservationStatus(r.Status),
		IdempotencyKey: r.IdempotencyKey,
		ExpiresAt:      r.ExpiresAt,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
	})
}
