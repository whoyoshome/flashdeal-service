package domain_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

func TestNewMoney(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		amount   int64
		currency string
		wantErr  bool
	}{
		{name: "accepts a positive amount in minor units", amount: 4999, currency: "USD"},
		{name: "accepts zero", amount: 0, currency: "COP"},
		{name: "rejects a negative amount", amount: -1, currency: "USD", wantErr: true},
		{name: "rejects a lowercase currency", amount: 100, currency: "usd", wantErr: true},
		{name: "rejects a two-letter currency", amount: 100, currency: "US", wantErr: true},
		{name: "rejects a four-letter currency", amount: 100, currency: "USDT", wantErr: true},
		{name: "rejects a currency with digits", amount: 100, currency: "U5D", wantErr: true},
		{name: "rejects an empty currency", amount: 100, currency: "", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m, err := domain.NewMoney(tc.amount, tc.currency)

			if tc.wantErr {
				requireCode(t, err, domain.CodeValidation)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.amount, m.Amount())
			require.Equal(t, tc.currency, m.Currency())
		})
	}
}

func TestMoneyMul(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		amount     int64
		n          int
		wantAmount int64
		wantErr    bool
	}{
		{name: "multiplies the amount and keeps the currency", amount: 4999, n: 3, wantAmount: 14997},
		{name: "returns zero when multiplied by zero", amount: 4999, n: 0, wantAmount: 0},
		{name: "accepts the largest product that fits", amount: math.MaxInt64 / 2, n: 2, wantAmount: math.MaxInt64 - 1},
		{name: "rejects a product that overflows int64", amount: math.MaxInt64/2 + 1, n: 2, wantErr: true},
		{name: "rejects a negative multiplier", amount: 100, n: -1, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := usd(t, tc.amount)

			got, err := m.Mul(tc.n)

			if tc.wantErr {
				requireCode(t, err, domain.CodeValidation)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantAmount, got.Amount())
			require.Equal(t, "USD", got.Currency())
		})
	}
}
