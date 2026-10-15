package domain_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

func TestAsDomain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		wantOK   bool
		wantCode domain.Code
	}{
		{
			name:     "finds a domain error wrapped several layers deep",
			err:      fmt.Errorf("handler: %w", fmt.Errorf("use case: %w", domain.ErrSoldOut)),
			wantOK:   true,
			wantCode: domain.CodeSoldOut,
		},
		{
			name:     "finds a validation error",
			err:      domain.NewValidation("quantity must be positive"),
			wantOK:   true,
			wantCode: domain.CodeValidation,
		},
		{
			name:   "reports false for an error without a code",
			err:    errors.New("connection reset"),
			wantOK: false,
		},
		{
			name:   "reports false for nil",
			err:    nil,
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			de, ok := domain.AsDomain(tc.err)

			require.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				require.Equal(t, tc.wantCode, de.Code)
			}
		})
	}
}

func TestErrorMessage(t *testing.T) {
	t.Parallel()

	t.Run("prefixes the message with its code", func(t *testing.T) {
		t.Parallel()

		require.Equal(t, "SOLD_OUT: deal sold out", domain.ErrSoldOut.Error())
	})
}
