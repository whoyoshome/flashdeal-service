package httpapi

import (
	"time"

	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

// DTOs are the public JSON contract (api/openapi.yaml). They live only in
// this file and are mapped explicitly to and from domain types.

// healthResponse is the body of GET /healthz and GET /readyz.
type healthResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

// errorBody is the single error format of the API.
type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

type createDealRequest struct {
	ProductID    string    `json:"product_id"`
	Title        string    `json:"title"`
	PriceCents   int64     `json:"price_cents"`
	Currency     string    `json:"currency"`
	TotalStock   int       `json:"total_stock"`
	PerUserLimit int       `json:"per_user_limit"`
	StartsAt     time.Time `json:"starts_at"`
	EndsAt       time.Time `json:"ends_at"`
}

func (req createDealRequest) validate() error {
	if req.StartsAt.IsZero() || req.EndsAt.IsZero() {
		return domain.NewValidation("starts_at and ends_at are required")
	}
	return nil
}

func (req createDealRequest) toInput() usecase.CreateDealInput {
	return usecase.CreateDealInput{
		ProductID:    req.ProductID,
		Title:        req.Title,
		PriceCents:   req.PriceCents,
		Currency:     req.Currency,
		TotalStock:   req.TotalStock,
		PerUserLimit: req.PerUserLimit,
		StartsAt:     req.StartsAt,
		EndsAt:       req.EndsAt,
	}
}

type dealResponse struct {
	ID         int64  `json:"id"`
	ProductID  string `json:"product_id"`
	Title      string `json:"title"`
	PriceCents int64  `json:"price_cents"`
	Currency   string `json:"currency"`
	TotalStock int    `json:"total_stock"`
	// AvailableStock is approximate: it is a snapshot, and the stock gate
	// decides the real outcome of a reservation.
	AvailableStock int       `json:"available_stock"`
	PerUserLimit   int       `json:"per_user_limit"`
	StartsAt       time.Time `json:"starts_at"`
	EndsAt         time.Time `json:"ends_at"`
	CreatedAt      time.Time `json:"created_at"`
}

func toDealResponse(d domain.Deal) dealResponse {
	return dealResponse{
		ID:             d.ID(),
		ProductID:      d.ProductID(),
		Title:          d.Title(),
		PriceCents:     d.Price().Amount(),
		Currency:       d.Price().Currency(),
		TotalStock:     d.TotalStock(),
		AvailableStock: d.Available(),
		PerUserLimit:   d.PerUserLimit(),
		StartsAt:       d.StartsAt(),
		EndsAt:         d.EndsAt(),
		CreatedAt:      d.CreatedAt(),
	}
}

type dealListResponse struct {
	Data []dealResponse `json:"data"`
	// NextCursor is omitted on the last page.
	NextCursor int64 `json:"next_cursor,omitempty"`
}

func toDealListResponse(p usecase.DealPage) dealListResponse {
	data := make([]dealResponse, 0, len(p.Deals))
	for _, d := range p.Deals {
		data = append(data, toDealResponse(d))
	}
	return dealListResponse{Data: data, NextCursor: p.NextCursor}
}

type reserveRequest struct {
	Quantity int `json:"quantity"`
}

type reservationResponse struct {
	ID        string    `json:"id"`
	DealID    int64     `json:"deal_id"`
	UserID    string    `json:"user_id"`
	Quantity  int       `json:"quantity"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

func toReservationResponse(r domain.Reservation) reservationResponse {
	return reservationResponse{
		ID:        r.ID(),
		DealID:    r.DealID(),
		UserID:    r.UserID(),
		Quantity:  r.Quantity(),
		Status:    string(r.Status()),
		ExpiresAt: r.ExpiresAt(),
		CreatedAt: r.CreatedAt(),
	}
}

type orderResponse struct {
	ID            string    `json:"id"`
	ReservationID string    `json:"reservation_id"`
	DealID        int64     `json:"deal_id"`
	UserID        string    `json:"user_id"`
	Quantity      int       `json:"quantity"`
	TotalCents    int64     `json:"total_cents"`
	Currency      string    `json:"currency"`
	CreatedAt     time.Time `json:"created_at"`
}

func toOrderResponse(o domain.Order) orderResponse {
	return orderResponse{
		ID:            o.ID(),
		ReservationID: o.ReservationID(),
		DealID:        o.DealID(),
		UserID:        o.UserID(),
		Quantity:      o.Quantity(),
		TotalCents:    o.Total().Amount(),
		Currency:      o.Total().Currency(),
		CreatedAt:     o.CreatedAt(),
	}
}
