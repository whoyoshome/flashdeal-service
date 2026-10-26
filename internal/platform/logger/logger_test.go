package logger_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

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

func TestTraceCorrelation(t *testing.T) {
	t.Parallel()

	t.Run("adds the trace and span IDs of the context's span", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := logger.New(&buf, slog.LevelInfo).With(slog.String("component", "test"))
		sc := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    trace.TraceID{0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6, 0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36},
			SpanID:     trace.SpanID{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7},
			TraceFlags: trace.FlagsSampled,
		})

		log.InfoContext(trace.ContextWithSpanContext(t.Context(), sc), "reserved")

		var record map[string]any
		require.NoError(t, json.Unmarshal(buf.Bytes(), &record))
		require.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", record["trace_id"])
		require.Equal(t, "00f067aa0ba902b7", record["span_id"])
		require.Equal(t, "test", record["component"], "attributes added with With survive")
	})

	t.Run("adds nothing outside a trace", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer

		logger.New(&buf, slog.LevelInfo).InfoContext(t.Context(), "started")

		require.NotContains(t, buf.String(), "trace_id")
	})
}
