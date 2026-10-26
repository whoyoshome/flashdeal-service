// Package logger builds the structured JSON logger shared by the service.
package logger

import (
	"context"
	"io"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// New returns a JSON logger that writes records at or above level to w.
// Records logged with a context inside a trace carry its trace_id and
// span_id, so a log line leads straight to its trace. The logger is passed
// explicitly to every component; the slog default logger is never replaced,
// so there is no hidden global state.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(traceHandler{Handler: slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})})
}

// traceHandler adds the active span's identifiers to every record.
type traceHandler struct {
	slog.Handler
}

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{Handler: h.Handler.WithGroup(name)}
}
