package domain_test

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

const orderID = "0192f0c4-0000-7000-8000-000000000001"

func TestNewOrder(t *testing.T) {
	t.Parallel()

	t.Run("prices a confirmed reservation at unit price times quantity", func(t *testing.T) {
		t.Parallel()
		r := reservationIn(t, domain.StatusConfirmed)
		now := t0.Add(time.Minute)

		o, err := domain.NewOrder(orderID, r, usd(t, 4999), now)

		require.NoError(t, err)
		require.Equal(t, orderID, o.ID())
		require.Equal(t, r.ID(), o.ReservationID())
		require.Equal(t, r.DealID(), o.DealID())
		require.Equal(t, r.UserID(), o.UserID())
		require.Equal(t, r.Quantity(), o.Quantity())
		require.Equal(t, usd(t, 9998), o.Total())
		require.Equal(t, now, o.CreatedAt())
	})

	t.Run("returns a validation error without an id", func(t *testing.T) {
		t.Parallel()

		_, err := domain.NewOrder("", reservationIn(t, domain.StatusConfirmed), usd(t, 4999), t0)

		requireCode(t, err, domain.CodeValidation)
	})

	t.Run("returns a validation error for a zero unit price", func(t *testing.T) {
		t.Parallel()

		_, err := domain.NewOrder(orderID, reservationIn(t, domain.StatusConfirmed), usd(t, 0), t0)

		requireCode(t, err, domain.CodeValidation)
	})

	t.Run("returns an error when the total overflows", func(t *testing.T) {
		t.Parallel()

		_, err := domain.NewOrder(orderID, reservationIn(t, domain.StatusConfirmed), usd(t, math.MaxInt64), t0)

		requireCode(t, err, domain.CodeValidation)
	})

	for _, status := range []domain.ReservationStatus{domain.StatusPending, domain.StatusExpired, domain.StatusCancelled} {
		t.Run("rejects a "+string(status)+" reservation as a caller bug", func(t *testing.T) {
			t.Parallel()

			_, err := domain.NewOrder(orderID, reservationIn(t, status), usd(t, 4999), t0)

			require.Error(t, err)
			_, isDomain := domain.AsDomain(err)
			require.False(t, isDomain, "ordering an unconfirmed reservation is a bug, not a client error")
		})
	}
}

func TestRestoreOrder(t *testing.T) {
	t.Parallel()

	valid := func(t *testing.T) domain.OrderState {
		t.Helper()
		return domain.OrderState{
			ID:            orderID,
			ReservationID: validReservationSpec().ID,
			DealID:        7,
			UserID:        "user-1",
			Quantity:      2,
			Total:         usd(t, 9998),
			CreatedAt:     t0,
		}
	}

	t.Run("rebuilds every persisted field", func(t *testing.T) {
		t.Parallel()
		s := valid(t)

		o, err := domain.RestoreOrder(s)

		require.NoError(t, err)
		require.Equal(t, s.ID, o.ID())
		require.Equal(t, s.Total, o.Total())
		require.Equal(t, s.CreatedAt, o.CreatedAt())
	})

	zeroUSD := usd(t, 0)
	tests := []struct {
		name   string
		mutate func(*domain.OrderState)
	}{
		{name: "rejects a missing id", mutate: func(s *domain.OrderState) { s.ID = "" }},
		{name: "rejects a missing reservation id", mutate: func(s *domain.OrderState) { s.ReservationID = "" }},
		{name: "rejects a non-positive deal id", mutate: func(s *domain.OrderState) { s.DealID = 0 }},
		{name: "rejects zero quantity", mutate: func(s *domain.OrderState) { s.Quantity = 0 }},
		{name: "rejects a zero total", mutate: func(s *domain.OrderState) { s.Total = zeroUSD }},
		{name: "rejects a total without a currency", mutate: func(s *domain.OrderState) { s.Total = domain.Money{} }},
	}
	for _, tc := range tests {
		t.Run(tc.name+" as corrupt state", func(t *testing.T) {
			t.Parallel()
			s := valid(t)
			tc.mutate(&s)

			_, err := domain.RestoreOrder(s)

			require.ErrorIs(t, err, domain.ErrCorruptState)
		})
	}
}
