package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

// t0 is a fixed reference instant so time-dependent tests are deterministic.
var t0 = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// usd builds a valid USD amount in cents.
func usd(t *testing.T, amount int64) domain.Money {
	t.Helper()
	m, err := domain.NewMoney(amount, "USD")
	require.NoError(t, err)
	return m
}

func validDealSpec(t *testing.T) domain.DealSpec {
	t.Helper()
	return domain.DealSpec{
		ProductID:    "sku-123",
		Title:        "Noise-cancelling headphones",
		Price:        usd(t, 4999),
		TotalStock:   1000,
		PerUserLimit: 2,
		StartsAt:     t0,
		EndsAt:       t0.Add(time.Hour),
	}
}

func validDealState(t *testing.T) domain.DealState {
	t.Helper()
	spec := validDealSpec(t)
	return domain.DealState{
		ID:           7,
		ProductID:    spec.ProductID,
		Title:        spec.Title,
		Price:        spec.Price,
		TotalStock:   spec.TotalStock,
		Reserved:     10,
		Sold:         5,
		PerUserLimit: spec.PerUserLimit,
		StartsAt:     spec.StartsAt,
		EndsAt:       spec.EndsAt,
		CreatedAt:    t0.Add(-time.Hour),
		UpdatedAt:    t0.Add(-time.Minute),
	}
}

func validReservationSpec() domain.ReservationSpec {
	return domain.ReservationSpec{
		ID:             "0192f0c4-6a3b-7c1d-9e2f-3a4b5c6d7e8f",
		DealID:         7,
		UserID:         "user-1",
		Quantity:       2,
		IdempotencyKey: "idem-1",
		ExpiresAt:      t0.Add(5 * time.Minute),
	}
}

// pendingReservation returns a reservation created at t0 that expires at
// t0+5m.
func pendingReservation(t *testing.T) domain.Reservation {
	t.Helper()
	r, err := domain.NewReservation(validReservationSpec(), t0)
	require.NoError(t, err)
	return r
}

// reservationIn returns a reservation in the given status, as storage would
// rebuild it.
func reservationIn(t *testing.T, status domain.ReservationStatus) domain.Reservation {
	t.Helper()
	spec := validReservationSpec()
	r, err := domain.RestoreReservation(domain.ReservationState{
		ID:             spec.ID,
		DealID:         spec.DealID,
		UserID:         spec.UserID,
		Quantity:       spec.Quantity,
		Status:         status,
		IdempotencyKey: spec.IdempotencyKey,
		ExpiresAt:      spec.ExpiresAt,
		CreatedAt:      t0,
		UpdatedAt:      t0,
	})
	require.NoError(t, err)
	return r
}

// requireCode asserts err is a domain error with the given code.
func requireCode(t *testing.T, err error, want domain.Code) {
	t.Helper()
	de, ok := domain.AsDomain(err)
	require.True(t, ok, "expected a *domain.Error, got %v", err)
	require.Equal(t, want, de.Code)
}
