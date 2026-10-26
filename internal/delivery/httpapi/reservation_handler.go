package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/whoyoshome/flashdeal-service/internal/delivery/httpapi/middleware"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

// reserve handles POST /v1/deals/{id}/reservations. A retry with the same
// Idempotency-Key returns the original reservation with the same 201.
func (h *handlers) reserve(w http.ResponseWriter, r *http.Request) {
	dealID, err := dealIDParam(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var req reserveRequest
	if err := decodeJSON(w, r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}

	res, err := h.reserver.Execute(r.Context(), usecase.ReserveInput{
		DealID:         dealID,
		UserID:         middleware.UserIDFrom(r.Context()),
		Quantity:       req.Quantity,
		IdempotencyKey: key,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, r, http.StatusCreated, toReservationResponse(res))
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
