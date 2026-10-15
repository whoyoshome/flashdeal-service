package logger_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/platform/logger"
)

func TestNew(t *testing.T) {
	t.Parallel()

	t.Run("writes records as JSON with structured attributes", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := logger.New(&buf, slog.LevelInfo)

		log.InfoContext(t.Context(), "deal activated", slog.Int64("deal_id", 42))

		var record map[string]any
		require.NoError(t, json.Unmarshal(buf.Bytes(), &record))
		require.Equal(t, "INFO", record["level"])
		require.Equal(t, "deal activated", record["msg"])
		require.InDelta(t, 42, record["deal_id"], 0)
	})

	t.Run("drops records below the configured level", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := logger.New(&buf, slog.LevelWarn)

		log.InfoContext(t.Context(), "noise")

		require.Empty(t, buf.String())
	})
}
