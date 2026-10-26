package middleware

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Recoverer turns a panic in a handler into a logged stack trace and a 500,
// so one bug cannot take the process down. It is a safety net, not control
// flow: handlers never panic on purpose. http.ErrAbortHandler is re-raised
// because net/http uses it to abort a response deliberately.
func Recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec)
				}
				log.ErrorContext(r.Context(), "panic recovered",
					slog.String("request_id", RequestIDFrom(r.Context())),
					slog.Any("panic", rec),
					slog.String("stack", string(debug.Stack())))
				writeError(w, r, http.StatusInternalServerError, "INTERNAL", "internal error")
			}()
			next.ServeHTTP(w, r)
		})
	}
}
