package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// writeJSON encodes v before writing any header, so an encoding bug turns
// into a clean 500 instead of a truncated 200.
func (h *handlers) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		h.log.ErrorContext(r.Context(), "encode response", slog.Any("error", err))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(append(body, '\n')); err != nil {
		// The client went away; there is nobody left to tell.
		h.log.DebugContext(r.Context(), "write response", slog.Any("error", err))
	}
}
