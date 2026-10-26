package httpapi

import (
	"net/http"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
)

// createDeal handles POST /v1/deals (admin).
func (h *handlers) createDeal(w http.ResponseWriter, r *http.Request) {
	var req createDealRequest
	if err := decodeJSON(w, r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	if err := req.validate(); err != nil {
		h.writeError(w, r, err)
		return
	}
	d, err := h.deals.Create(r.Context(), req.toInput())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, r, http.StatusCreated, toDealResponse(d))
}

// activateDeal handles POST /v1/deals/{id}/activate (admin).
func (h *handlers) activateDeal(w http.ResponseWriter, r *http.Request) {
	id, err := dealIDParam(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	d, err := h.deals.Activate(r.Context(), id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, r, http.StatusOK, toDealResponse(d))
}

// getDeal handles GET /v1/deals/{id}.
func (h *handlers) getDeal(w http.ResponseWriter, r *http.Request) {
	id, err := dealIDParam(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	d, err := h.queries.Get(r.Context(), id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, r, http.StatusOK, toDealResponse(d))
}

// listDeals handles GET /v1/deals?status=active&cursor=&limit=. Only active
// deals can be listed; status may be omitted and defaults to active.
func (h *handlers) listDeals(w http.ResponseWriter, r *http.Request) {
	if s := r.URL.Query().Get("status"); s != "" && s != "active" {
		h.writeError(w, r, domain.NewValidation("status must be active"))
		return
	}
	cursor, err := queryInt64(r, "cursor")
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	limit, err := queryInt64(r, "limit")
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	page, err := h.queries.ListActive(r.Context(), cursor, int(min(limit, 1<<20)))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, r, http.StatusOK, toDealListResponse(page))
}
