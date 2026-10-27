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

// Metrics records what the HTTP layer observes. platform/observability
// implements it.
type Metrics interface {
	middleware.HTTPObserver
	// ReservationAttempt counts one reservation attempt by its outcome.
	ReservationAttempt(result string)
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

	// Tokens verifies Bearer JWTs (HS256 or JWKS/RS256). Required.
	Tokens middleware.Authenticator

	// RateLimiter bounds authenticated callers. Nil disables it.
	RateLimiter middleware.RateLimiter

	// Idempotency stores reservation responses by Idempotency-Key. Nil
	// disables the middleware (handler tests).
	Idempotency usecase.IdempotencyStore
	// IdempotencyTTL is how long a stored response is replayed.
	IdempotencyTTL time.Duration

	// RequestTimeout bounds every /v1 request.
	RequestTimeout time.Duration
	// Metrics records requests and reservation outcomes; nil disables it.
	Metrics Metrics
	// MetricsHandler serves GET /metrics; nil leaves the route out.
	MetricsHandler http.Handler
	// Tracing starts a span per /v1 request; nil disables it.
	Tracing func(http.Handler) http.Handler

	// ReadinessChecks are probed by GET /readyz.
	ReadinessChecks []ReadinessCheck
	// ReadinessTimeout bounds each individual readiness probe.
	ReadinessTimeout time.Duration

	// DemoEnabled serves GET / and POST /v1/demo/session for the public
	// portfolio page. Requires Demo and Issuer.
	DemoEnabled bool
	Demo        DemoStarter
	Issuer      TokenIssuer
	// DemoLimiter bounds demo session creation by client IP. Nil skips it.
	DemoLimiter middleware.RateLimiter
}

// NewRouter builds the HTTP handler tree. Health and metrics endpoints sit
// outside /v1 so frequent probes and scrapes are neither access-logged,
// measured nor timed out with the business routes.
func NewRouter(d Deps) http.Handler {
	metrics := d.Metrics
	if metrics == nil {
		metrics = noMetrics{}
	}
	h := &handlers{
		log:              d.Log,
		metrics:          metrics,
		deals:            d.Deals,
		queries:          d.Queries,
		reserver:         d.Reserve,
		confirmer:        d.Confirm,
		canceller:        d.Cancel,
		readinessChecks:  d.ReadinessChecks,
		readinessTimeout: d.ReadinessTimeout,
		demo:             d.Demo,
		issuer:           d.Issuer,
		demoLimiter:      d.DemoLimiter,
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer(d.Log))
	r.NotFound(h.routeNotFound)
	r.MethodNotAllowed(h.methodNotAllowed)

	r.Get("/healthz", h.liveness)
	r.Get("/readyz", h.readiness)
	if d.MetricsHandler != nil {
		r.Method(http.MethodGet, "/metrics", d.MetricsHandler)
	}
	if d.DemoEnabled && d.Demo != nil && d.Issuer != nil {
		r.Get("/", h.demoPage)
	}

	r.Route("/v1", func(r chi.Router) {
		// Tracing comes first so the access log and everything below run
		// inside the request's span and carry its trace_id.
		if d.Tracing != nil {
			r.Use(d.Tracing)
		}
		r.Use(middleware.Logging(d.Log), middleware.Metrics(metrics), middleware.Timeout(d.RequestTimeout))

		// Public reads: deal snapshots are approximate and not sensitive.
		r.Get("/deals", h.listDeals)
		r.Get("/deals/{id}", h.getDeal)
		if d.DemoEnabled && d.Demo != nil && d.Issuer != nil {
			r.Post("/demo/session", h.demoSession)
		}

		r.Group(func(r chi.Router) {
			r.Use(middleware.Auth(d.Tokens), middleware.RateLimit(d.RateLimiter, d.Log))
			r.With(middleware.RequireAdmin).Post("/deals", h.createDeal)
			r.With(middleware.RequireAdmin).Post("/deals/{id}/activate", h.activateDeal)
			r.With(idempotency(d)...).Post("/deals/{id}/reservations", h.reserve)
			r.Post("/reservations/{id}/confirm", h.confirm)
			r.Delete("/reservations/{id}", h.cancel)
		})
	})
	return r
}

// idemLockMargin keeps a running request's idempotency claim alive past its
// own timeout, so the claim never expires while the request still runs.
const idemLockMargin = 5 * time.Second

// idempotency returns the idempotency middleware for resource-creating
// routes, or none when no store is configured.
func idempotency(d Deps) []func(http.Handler) http.Handler {
	if d.Idempotency == nil {
		return nil
	}
	cfg := middleware.IdempotencyConfig{LockTTL: d.RequestTimeout + idemLockMargin, ResponseTTL: d.IdempotencyTTL}
	return []func(http.Handler) http.Handler{middleware.Idempotency(d.Idempotency, cfg, d.Log)}
}

type handlers struct {
	log              *slog.Logger
	metrics          Metrics
	deals            DealManager
	queries          DealFinder
	reserver         Reserver
	confirmer        Confirmer
	canceller        Canceller
	readinessChecks  []ReadinessCheck
	readinessTimeout time.Duration
	demo             DemoStarter
	issuer           TokenIssuer
	demoLimiter      middleware.RateLimiter
}

// noMetrics discards observations when no Metrics is configured.
type noMetrics struct{}

func (noMetrics) ObserveHTTP(string, string, int, time.Duration) {}

func (noMetrics) ReservationAttempt(string) {}
