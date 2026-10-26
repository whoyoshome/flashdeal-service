package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/whoyoshome/flashdeal-service/internal/delivery/httpapi/middleware"
	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

// reserve handles POST /v1/deals/{id}/reservations. A retry with the same
// Idempotency-Key returns the original reservation with the same 201; such
// replays are answered by the idempotency middleware and are not counted
// again as attempts.
func (h *handlers) reserve(w http.ResponseWriter, r *http.Request) {
	res, err := h.runReservation(w, r)
	h.metrics.ReservationAttempt(reservationResult(err))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, r, http.StatusCreated, toReservationResponse(res))
}

func (h *handlers) runReservation(w http.ResponseWriter, r *http.Request) (domain.Reservation, error) {
	dealID, err := dealIDParam(r)
	if err != nil {
		return domain.Reservation{}, err
	}
	key, err := idempotencyKey(r)
	if err != nil {
		return domain.Reservation{}, err
	}
	var req reserveRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return domain.Reservation{}, err
	}
	return h.reserver.Execute(r.Context(), usecase.ReserveInput{
		DealID:         dealID,
		UserID:         middleware.UserIDFrom(r.Context()),
		Quantity:       req.Quantity,
		IdempotencyKey: key,
	})
}

// reservationResult labels a reservation attempt's outcome for metrics:
// "created", the business error code in lower case ("sold_out",
// "user_limit_reached", ...), "request_timeout" or "internal". The set is
// closed, which keeps the metric's cardinality bounded.
func reservationResult(err error) string {
	if err == nil {
		return "created"
	}
	if de, ok := domain.AsDomain(err); ok {
		return strings.ToLower(string(de.Code))
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request_timeout"
	}
	return "internal"
}

// confirm handles POST /v1/reservations/{id}/confirm.
func (h *handlers) confirm(w http.ResponseWriter, r *http.Request) {
	order, err := h.confirmer.Execute(r.Context(), usecase.ConfirmInput{
		ReservationID: chi.URLParam(r, "id"),
		UserID:        middleware.UserIDFrom(r.Context()),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, r, http.StatusOK, toOrderResponse(order))
}

// cancel handles DELETE /v1/reservations/{id}.
func (h *handlers) cancel(w http.ResponseWriter, r *http.Request) {
	err := h.canceller.Execute(r.Context(), usecase.CancelInput{
		ReservationID: chi.URLParam(r, "id"),
		UserID:        middleware.UserIDFrom(r.Context()),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
