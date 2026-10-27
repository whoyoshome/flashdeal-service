package worker

import (
	"context"
	"log/slog"

	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

// LoggingHandler is the demo EventHandler: it logs each event. A real
// fulfilment or notification service replaces it behind the same interface.
type LoggingHandler struct {
	log *slog.Logger
}

// NewLoggingHandler returns a handler that logs every event it receives.
func NewLoggingHandler(log *slog.Logger) *LoggingHandler {
	return &LoggingHandler{log: log}
}

// Handle logs the event's identity and type.
func (h *LoggingHandler) Handle(ctx context.Context, e usecase.OutboxEvent) error {
	h.log.InfoContext(ctx, "event received",
		slog.Int64("event_id", e.ID),
		slog.String("type", e.Type),
		slog.String("aggregate_id", e.AggregateID),
		slog.Time("occurred_at", e.OccurredAt),
	)
	return nil
}
