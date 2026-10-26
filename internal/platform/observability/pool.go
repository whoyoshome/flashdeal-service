package observability

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// PoolStats is the subset of a connection pool's statistics exported as
// metrics. *pgxpool.Stat satisfies it.
type PoolStats interface {
	AcquiredConns() int32
	IdleConns() int32
	MaxConns() int32
	AcquireCount() int64
	EmptyAcquireCount() int64
	EmptyAcquireWaitTime() time.Duration
	CanceledAcquireCount() int64
}

// poolCollector reads the pool's statistics at scrape time, so the values
// are always current and nothing has to poll the pool.
type poolCollector struct {
	stats func() PoolStats

	acquired, idle, maxConns           *prometheus.Desc
	acquires, emptyAcquires, emptyWait *prometheus.Desc
	canceled                           *prometheus.Desc
}

// WatchPool exports the statistics of a database connection pool. Under a
// flash sale the pool is where requests queue for the hot row, so its wait
// counters are the first place to look when latency grows.
func (m *Metrics) WatchPool(stats func() PoolStats) {
	m.registry.MustRegister(&poolCollector{
		stats:         stats,
		acquired:      prometheus.NewDesc("db_pool_acquired_conns", "Connections currently checked out of the pool.", nil, nil),
		idle:          prometheus.NewDesc("db_pool_idle_conns", "Idle connections in the pool.", nil, nil),
		maxConns:      prometheus.NewDesc("db_pool_max_conns", "Maximum size of the pool.", nil, nil),
		acquires:      prometheus.NewDesc("db_pool_acquires_total", "Successful connection acquisitions.", nil, nil),
		emptyAcquires: prometheus.NewDesc("db_pool_empty_acquires_total", "Acquisitions that had to wait because the pool was empty.", nil, nil),
		emptyWait:     prometheus.NewDesc("db_pool_empty_acquire_wait_seconds_total", "Time spent waiting for a connection when the pool was empty.", nil, nil),
		canceled:      prometheus.NewDesc("db_pool_canceled_acquires_total", "Acquisitions abandoned because their context ended first.", nil, nil),
	})
}

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{c.acquired, c.idle, c.maxConns, c.acquires, c.emptyAcquires, c.emptyWait, c.canceled} {
		ch <- d
	}
}

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.stats()
	ch <- prometheus.MustNewConstMetric(c.acquired, prometheus.GaugeValue, float64(s.AcquiredConns()))
	ch <- prometheus.MustNewConstMetric(c.idle, prometheus.GaugeValue, float64(s.IdleConns()))
	ch <- prometheus.MustNewConstMetric(c.maxConns, prometheus.GaugeValue, float64(s.MaxConns()))
	ch <- prometheus.MustNewConstMetric(c.acquires, prometheus.CounterValue, float64(s.AcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.emptyAcquires, prometheus.CounterValue, float64(s.EmptyAcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.emptyWait, prometheus.CounterValue, s.EmptyAcquireWaitTime().Seconds())
	ch <- prometheus.MustNewConstMetric(c.canceled, prometheus.CounterValue, float64(s.CanceledAcquireCount()))
}
