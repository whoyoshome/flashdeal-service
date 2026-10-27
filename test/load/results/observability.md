# Phase 6: a flash sale on the Grafana dashboard

The phase 6 definition of done: during a k6 run, Grafana shows a deal's stock
falling to 0 and the `SOLD_OUT` rate rising.

The run used the full local stack (`make up`): the API, PostgreSQL and Redis,
plus Jaeger, Prometheus and Grafana, all in the same Docker Desktop VM (4 CPUs,
9.7 GiB) as k6. Tracing was on and sampled 10 % of new traces. Prometheus
scraped every second, and the stock sampler ran every second.

## Scenario B: the architecture document's case

`STOCK=1000 PEAK_RPS=5000 RAMP=2s HOLD=10s make load`

### From k6

| Metric                           | Value                        |
| -------------------------------- | ---------------------------- |
| Units sold / oversold            | 1 000 / **0**                |
| Requests served                  | 54 054 (4 502/s)             |
| Sold-out rejections              | 53 051                       |
| Timeouts (503)                   | 0                            |
| Sold-out rejection p50 / p99     | 0.44 ms / 31.7 ms            |
| Successful reservation p50 / p99 | 510 ms / 1.04 s              |

### From Prometheus

These are the queries behind the dashboard, evaluated over the run.

| What the dashboard shows        | Value      | Query                                                          |
| ------------------------------- | ---------- | -------------------------------------------------------------- |
| Stock left in the gate, deal 70 | 399 → 0    | `stock_remaining{deal_id="70"}`, one second apart              |
| Reservations created            | 1 003      | `sum(increase(reservations_total{result="created"}[60s]))`     |
| Sold-out share                  | 98.1 %     | sold_out over all outcomes, `increase` over the run            |
| Peak sold-out rate              | 5 000 /s   | `sum(rate(reservations_total{result="sold_out"}[5s]))`         |
| PostgreSQL pool in use, peak    | 20 of 20   | `db_pool_acquired_conns`                                       |
| Time waiting for a connection   | 468 s      | `increase(db_pool_empty_acquire_wait_seconds_total[60s])`      |

The 1 003 is `increase()` extrapolating at the edges of the window; k6
counted exactly 1 000.

### What the dashboard showed

- **Stock left in the gate.** The deal's line dropped from 399 to 0 within
  one sample. The Redis gate admits buyers as fast as they arrive, so 1 000
  units are gone in under a second. That speed is why Compose samples and
  scrapes every second; at 5 s the fall happened between two samples and
  never showed.
- **Reservation outcomes.** `created` stays a thin band for a few seconds.
  `sold_out` then climbs to the offered 5 000 req/s and holds there until k6
  stops. Requests by status mirrors it as 409s.
- **Sold-out share** was 98.1 %. **Request timeouts** and **gate repairs**
  stayed at 0.
- **Latency p99 by route.** The reservation route sits near 1 s while the
  units are being written, then falls to milliseconds once only rejections
  remain.
- **PostgreSQL pool.** It is pinned at 20 of 20 during the sale. Time
  waiting for a connection peaks at about two minutes per second, that is,
  around 125 requests queued for a connection at once. This is the hot-row
  write bottleneck documented in [redis-gate.md](redis-gate.md), now visible
  live. It is phase 7's starting point.
- **Expired reservations.** Deal 68, from a run five minutes earlier, had
  1 000 pending reservations that nobody confirmed. When their 5-minute TTL
  elapsed, the expirer returned all 1 000 units (`reservations_expired_total`
  +1 000, about 270/s for a few seconds), and the deal's gate line went back
  up to 1 000. That is phase 5 working on its own, caught on the same
  dashboard.

Screenshots were taken from the built-in browser during the review. They are
not committed, since the repository keeps no binaries; the dashboard
reproduces them from `deploy/grafana/dashboards/flashdeal.json`.

## Variation between runs

The sold-out p99 was 44.6 ms, 8.3 ms and 31.7 ms across three back-to-back
runs, against 6.5 ms in phase 4 without the observability stack. The p50
stayed between 0.43 and 0.47 ms. The tail moves because k6 and six
containers share four CPUs, so it is not attributable to tracing alone.
Phase 7 will measure the cost of each piece with pprof and tracing turned
off and on.

## How to reproduce

```bash
make up
make migrate
STOCK=1000 PEAK_RPS=5000 RAMP=2s HOLD=10s make load
```

Then open <http://localhost:3000> and set the time range to the run.
