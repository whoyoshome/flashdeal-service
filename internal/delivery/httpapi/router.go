// Package httpapi translates HTTP requests into application calls and maps
// the results back to HTTP. It is not named "http" to avoid shadowing net/http.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/whoyoshome/flashdeal-service/internal/delivery/httpapi/middleware"
	"github.com/whoyoshome/flashdeal-service/internal/domain"
	"github.com/whoyoshome/flashdeal-service/internal/usecase"
)

// The use cases the HTTP layer calls, declared here where they are consumed
// so handler tests can substitute fakes.

// DealManager runs the admin operations on deals.
type DealManager interface {
	Create(ctx context.Context, in usecase.CreateDealInput) (domain.Deal, error)
	Activate(ctx context.Context, id int64) (domain.Deal, error)
}

// DealFinder answers read-only deal queries.
type DealFinder interface {
	Get(ctx context.Context, id int64) (domain.Deal, error)
	ListActive(ctx context.Context, cursor int64, limit int) (usecase.DealPage, error)
}

// Reserver creates reservations.
type Reserver interface {
	Execute(ctx context.Context, in usecase.ReserveInput) (domain.Reservation, error)
}

// Confirmer confirms reservations into orders.
type Confirmer interface {
	Execute(ctx context.Context, in usecase.ConfirmInput) (domain.Order, error)
}

// Canceller cancels reservations.
type Canceller interface {
	Execute(ctx context.Context, in usecase.CancelInput) error
}

// Deps holds everything the HTTP layer needs. It is assembled in cmd/api,
// the only place that knows the concrete implementations.
type Deps struct {
	Log *slog.Logger

	Deals   DealManager
	Queries DealFinder
	Reserve Reserver
	Confirm Confirmer
	Cancel  Canceller

	// RequestTimeout bounds every /v1 request.
	RequestTimeout time.Duration
	// ReadinessChecks are probed by GET /readyz.
	ReadinessChecks []ReadinessCheck
	// ReadinessTimeout bounds each individual readiness probe.
	ReadinessTimeout time.Duration
}

// NewRouter builds the HTTP handler tree. Health endpoints sit outside /v1
// so frequent orchestrator probes are neither access-logged nor timed out
// with the business routes.
func NewRouter(d Deps) http.Handler {
	h := &handlers{
		log:              d.Log,
		deals:            d.Deals,
		queries:          d.Queries,
		reserver:         d.Reserve,
		confirmer:        d.Confirm,
		canceller:        d.Cancel,
		readinessChecks:  d.ReadinessChecks,
		readinessTimeout: d.ReadinessTimeout,
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer(d.Log))
	r.NotFound(h.routeNotFound)
	r.MethodNotAllowed(h.methodNotAllowed)

	r.Get("/healthz", h.liveness)
	r.Get("/readyz", h.readiness)

	r.Route("/v1", func(r chi.Router) {
		r.Use(middleware.Logging(d.Log), middleware.Timeout(d.RequestTimeout))

		// Admin endpoints are not authenticated yet (pending, see README).
		r.Post("/deals", h.createDeal)
		r.Post("/deals/{id}/activate", h.activateDeal)
		r.Get("/deals", h.listDeals)
		r.Get("/deals/{id}", h.getDeal)

		r.Group(func(r chi.Router) {
			r.Use(middleware.Auth)
			r.Post("/deals/{id}/reservations", h.reserve)
			r.Post("/reservations/{id}/confirm", h.confirm)
			r.Delete("/reservations/{id}", h.cancel)
		})
	})
	return r
}

type handlers struct {
	log              *slog.Logger
	deals            DealManager
	queries          DealFinder
	reserver         Reserver
	confirmer        Confirmer
	canceller        Canceller
	readinessChecks  []ReadinessCheck
	readinessTimeout time.Duration
}
