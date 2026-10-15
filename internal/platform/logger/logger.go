// Package logger builds the structured JSON logger shared by the service.
package logger

import (
	"io"
	"log/slog"
)

// New returns a JSON logger that writes records at or above level to w.
// It is passed explicitly to every component; the slog default logger is
// never replaced, so there is no hidden global state.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}
