package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/whoyoshome/flashdeal-service/internal/delivery/httpapi/middleware"
	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

// statusFor maps a business error code to its HTTP status. This is the only
// place in the service that knows about that mapping.
func statusFor(c domain.Code) int {
	switch c {
	case domain.CodeNotFound:
		return http.StatusNotFound
	case domain.CodeValidation:
		return http.StatusBadRequest
	case domain.CodeSoldOut, domain.CodeDealNotActive, domain.CodeUserLimit, domain.CodeConflict:
		return http.StatusConflict
	case domain.CodeReservationExpired:
		return http.StatusGone
	case domain.CodeUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// writeError renders err as the standard error body. Business errors expose
// their stable code and message; anything else becomes a generic 500 and its
// details go only to the log. This is the edge where errors are logged once.
func (h *handlers) writeError(w http.ResponseWriter, r *http.Request, err error) {
	reqID := middleware.RequestIDFrom(r.Context())
	if de, ok := domain.AsDomain(err); ok {
		status := statusFor(de.Code)
		if status >= http.StatusInternalServerError {
			h.log.ErrorContext(r.Context(), "request failed",
				slog.String("request_id", reqID), slog.String("code", string(de.Code)), slog.Any("error", err))
		}
		h.writeJSON(w, r, status, errorBody{Code: string(de.Code), Message: de.Message, RequestID: reqID})
		return
	}
	h.log.ErrorContext(r.Context(), "unhandled error", slog.String("request_id", reqID), slog.Any("error", err))
	h.writeJSON(w, r, http.StatusInternalServerError, errorBody{Code: codeInternal, Message: "internal error", RequestID: reqID})
}

// Codes used only by the HTTP layer, for failures that never reach a use case.
const (
	codeInternal         = "INTERNAL"
	codeRouteNotFound    = "NOT_FOUND"
	codeMethodNotAllowed = "METHOD_NOT_ALLOWED"
)

func (h *handlers) routeNotFound(w http.ResponseWriter, r *http.Request) {
	h.writeJSON(w, r, http.StatusNotFound, errorBody{
		Code: codeRouteNotFound, Message: "route not found", RequestID: middleware.RequestIDFrom(r.Context()),
	})
}

func (h *handlers) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	h.writeJSON(w, r, http.StatusMethodNotAllowed, errorBody{
		Code: codeMethodNotAllowed, Message: "method not allowed", RequestID: middleware.RequestIDFrom(r.Context()),
	})
}
