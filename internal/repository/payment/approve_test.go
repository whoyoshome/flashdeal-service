package payment_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/repository/payment"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

func TestAlwaysApproveIsIdempotentOnReservationID(t *testing.T) {
	t.Parallel()
	g := payment.NewAlwaysApprove()
	in := usecase.PaymentCharge{ReservationID: "r1", UserID: "u", AmountCents: 100, Currency: "USD"}

	require.NoError(t, g.Charge(t.Context(), in))
	require.NoError(t, g.Charge(t.Context(), in), "a second charge for the same reservation must not fail")
}
