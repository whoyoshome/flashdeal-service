// Package observability holds the service's telemetry plumbing: the
// Prometheus registry and its metrics, and the admin endpoints used for
// profiling. It knows nothing about the business; the layers that observe
// something depend on small interfaces this package satisfies.
package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// latencyBuckets spans the service's range: sold-out rejections answered
// from Redis in well under a millisecond up to requests hitting the timeout.
var latencyBuckets = []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

// Metrics owns a private Prometheus registry and every metric the service
// exports. A private registry, unlike the global default, keeps tests
// independent and avoids init-time registration.
type Metrics struct {
	registry     *prometheus.Registry
	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
	reservations *prometheus.CounterVec
	expired      prometheus.Counter
	repairs      prometheus.Counter
	stock        *prometheus.GaugeVec
}

// NewMetrics creates and registers every metric, plus the Go runtime and
// process collectors.
func NewMetrics() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP requests handled, by method, route pattern and status code.",
		}, []string{"method", "route", "status"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency, by method and route pattern.",
			Buckets: latencyBuckets,
		}, []string{"method", "route"}),
		reservations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "reservations_total",
			Help: "Reservation attempts by outcome: created, sold_out, user_limit_reached, request_timeout, ...",
		}, []string{"result"}),
		expired: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "reservations_expired_total",
			Help: "Reservations whose TTL elapsed and whose units returned to the deal.",
		}),
		repairs: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "stock_gate_repairs_total",
			Help: "Deals whose Redis stock gate was corrected from PostgreSQL after a lost update.",
		}),
		stock: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "stock_remaining",
			Help: "Units left in the Redis stock gate, per active deal, as last sampled by this replica.",
		}, []string{"deal_id"}),
	}
	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.httpRequests, m.httpDuration, m.reservations, m.expired, m.repairs, m.stock,
	)
	for _, result := range reservationResults {
		m.reservations.WithLabelValues(result)
	}
	return m
}

// reservationResults are the outcomes exported from the start at zero. A
// series that only appears with its first increment hides that increment
// from rate() and increase(): the first reservations of a flash sale, which
// all arrive within one scrape interval, would go uncounted.
var reservationResults = []string{
	"created", "sold_out", "user_limit_reached", "deal_not_active", "stock_unavailable",
	"request_timeout", "validation_error", "not_found", "conflict", "internal",
}

// Handler serves the registry in the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// ObserveHTTP records one handled request. route must be the route pattern,
// never the raw path, to keep label cardinality bounded.
func (m *Metrics) ObserveHTTP(method, route string, status int, d time.Duration) {
	m.httpRequests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	m.httpDuration.WithLabelValues(method, route).Observe(d.Seconds())
}

// ReservationAttempt counts one reservation attempt by its outcome.
func (m *Metrics) ReservationAttempt(result string) {
	m.reservations.WithLabelValues(result).Inc()
}

// ReservationsExpired counts reservations returned to stock by the expirer.
func (m *Metrics) ReservationsExpired(n int) {
	m.expired.Add(float64(n))
}

// StockGateRepaired counts deals whose gate the reconciler corrected.
func (m *Metrics) StockGateRepaired(n int) {
	m.repairs.Add(float64(n))
}

// SetStockLevels replaces the stock_remaining series with levels, keyed by
// deal ID. Deals no longer active disappear instead of reporting a stale
// value forever.
func (m *Metrics) SetStockLevels(levels map[int64]int) {
	m.stock.Reset()
	for dealID, remaining := range levels {
		m.stock.WithLabelValues(strconv.FormatInt(dealID, 10)).Set(float64(remaining))
	}
}
