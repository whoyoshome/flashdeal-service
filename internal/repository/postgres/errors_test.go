package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

func TestTranslate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want error
	}{
		{
			name: "maps the stock CHECK constraint to ErrSoldOut by name",
			err:  &pgconn.PgError{Code: "23514", ConstraintName: "deal_stock_shards_not_exceeded"},
			want: domain.ErrSoldOut,
		},
		{
			name: "maps the reservation deal foreign key to ErrDealNotFound",
			err:  &pgconn.PgError{Code: "23503", ConstraintName: "reservations_deal_id_fkey"},
			want: domain.ErrDealNotFound,
		},
		{
			name: "maps any unique violation to ErrAlreadyExists",
			err:  &pgconn.PgError{Code: "23505", ConstraintName: "orders_reservation_id_key"},
			want: domain.ErrAlreadyExists,
		},
		{
			name: "finds the driver error through wrapping",
			err:  fmt.Errorf("exec: %w", &pgconn.PgError{Code: "23514", ConstraintName: "deal_stock_shards_not_exceeded"}),
			want: domain.ErrSoldOut,
		},
		{
			name: "returns nil for an unrelated CHECK violation",
			err:  &pgconn.PgError{Code: "23514", ConstraintName: "deals_window_valid"},
			want: nil,
		},
		{
			name: "returns nil for a non-driver error",
			err:  errors.New("connection reset"),
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := translate(tc.err)

			if tc.want == nil {
				require.NoError(t, got)
				return
			}
			require.ErrorIs(t, got, tc.want)
		})
	}

	t.Run("maps invalid text representation to a validation error", func(t *testing.T) {
		t.Parallel()

		de, ok := domain.AsDomain(translate(&pgconn.PgError{Code: "22P02"}))

		require.True(t, ok)
		require.Equal(t, domain.CodeValidation, de.Code)
	})
}
