package main

import (
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/platform/config"
)

func TestRun(t *testing.T) {
	t.Parallel()

	t.Run("returns an error instead of starting when configuration is invalid", func(t *testing.T) {
		t.Parallel()

		err := run(t.Context(), []string{
			"POSTGRES_URL=postgres://localhost:5432/db",
			"SHUTDOWN_TIMEOUT=0s",
		}, io.Discard)

		require.ErrorIs(t, err, config.ErrInvalid)
	})
}
