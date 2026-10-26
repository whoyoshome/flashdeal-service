# Phase 3 baseline: PostgreSQL only

The stock gate is a no-op in phase 3. Every reservation attempt, including
every sold-out rejection, runs a transaction against PostgreSQL: insert the
reservation, then `UPDATE deals SET reserved = reserved + 1 WHERE ... AND
reserved + sold + 1 <= total_stock` on the **same row**. These numbers are the
reference that phase 4 (Redis gate) is measured against in phase 7.

## Environment

| Item     | Value                                                                         |
| -------- | ----------------------------------------------------------------------------- |
| Date     | 2026-10-26                                                                    |
| Host     | Intel Core i7-14700K (28 logical cores), 31.7 GB RAM, Windows 11               |
| Docker   | Docker Desktop 28.1.1, Linux VM with 4 CPUs and 9.7 GiB, shared by all below |
| Stack    | API (commit on `feat/load-baseline`), PostgreSQL 17, Redis 7.4 (unused)       |
| Pool     | 20 PostgreSQL connections, `REQUEST_TIMEOUT=5s`                               |
| Load gen | k6 2.3.0 in the same Docker VM, at most 2000 VUs                              |

## Run 1: opening burst (`make load` defaults)

10 000 units, ramp to 5 000 attempts/s in 2 s, held for 20 s.

| Metric                                     | Value                            |
| ------------------------------------------ | -------------------------------- |
| Units sold (client view / database)        | **10 000 / 10 000**              |
| Units oversold                             | **0**                            |
| Requests served                            | 11 412 (453 req/s)               |
| Attempts not started (`dropped_iterations`)| 93 588                           |
| Reservation latency p50 / p95 / p99        | 4.67 s / 4.91 s / 4.97 s         |
| Sold-out rejection p50 / p95 / p99         | 4.14 s / 4.82 s / 4.89 s         |
| Requests that hit the 5 s timeout          | 178 (1.6 %), all rolled back     |

The service never oversold, but it **collapsed under contention**. Every
reservation needs the row lock on the single `deals` row, so writes serialize
at roughly 400 per second. Requests queue for pool connections and for the
lock until latency reaches the 5 s request timeout. Rejections are as slow as
successes because they wait in the same queue, which is exactly the hot-row
problem the architecture describes. Most of the offered load (93 588 attempts)
was never served at all.

## Run 2: gentle ramp

`STOCK=1000 PEAK_RPS=1000 RAMP=10s HOLD=30s make load`

| Metric                              | Value                         |
| ----------------------------------- | ----------------------------- |
| Units sold / oversold               | 1 000 / **0**                 |
| Requests served                     | 35 002 (928 req/s)            |
| Reservation latency p50 / p95 / p99 | 2.42 ms / 3.92 ms / 11.47 ms  |
| Sold-out rejection p50 / p95 / p99  | 0.61 ms / 0.87 ms / 1.16 ms   |
| Failed requests                     | 0                             |

When the stock sells out during a slow ramp (the 1 000 units were gone before
the rate climbed), there is little contention and PostgreSQL alone is fast. The
gate matters when the burst and the stock meet at the same moment, as in run 1.

## Findings fixed during this phase

- **Ambiguous commits.** In an earlier burst run, 7 reservations committed in
  PostgreSQL while their clients received a 500, because `COMMIT` used the
  request context and the 5 s deadline cancelled it mid-flight. `COMMIT` and
  `ROLLBACK` now run detached from the caller's cancellation with their own
  timeout. In run 1 the client and database counts match exactly.
- **Load-script idempotency keys.** Buyer IDs repeated across runs, so reused
  `Idempotency-Key`s were rightly rejected with `409 CONFLICT`. Buyers are now
  unique per run.

## Timeouts under overload

Requests that exceed `REQUEST_TIMEOUT` roll back completely. Run 1 returned
them as `500 INTERNAL`; they now return a retryable `503 REQUEST_TIMEOUT` with
`Retry-After: 1`, and k6 reports them as `reservations_timed_out` instead of
failing the correctness gate. Because the system is saturated, their count
varies a lot between identical runs: 178, 747 and 9 timeouts in three
consecutive default runs, with zero oversell in all of them.
