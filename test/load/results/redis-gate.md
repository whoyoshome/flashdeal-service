# Phase 4: Redis gate and deal cache vs PostgreSQL-only baseline

Both versions were measured in the same session, on the same machine and with
the same k6 script, run back to back.

| Version  | Commit    | Stock decided by                                            |
| -------- | --------- | ----------------------------------------------------------- |
| Baseline | `da491ca` | PostgreSQL only (conditional `UPDATE` on the `deals` row)   |
| Phase 4  | this PR   | Redis Lua gate, deal cache (2 s) with singleflight, then PG |

The environment matches the [baseline](baseline-postgres.md): Docker Desktop
VM with 4 CPUs and 9.7 GiB, shared by k6, the API, PostgreSQL and Redis; a
20-connection pool; `REQUEST_TIMEOUT=5s`.

## Scenario B: the architecture document's case

`STOCK=1000 PEAK_RPS=5000 RAMP=2s HOLD=10s make load`, about 55 000 buyers
competing for 1 000 units (98 % must be turned away).

| Metric                                | Baseline         | Phase 4             |
| ------------------------------------- | ---------------- | ------------------- |
| Units sold / oversold                 | 1 000 / **0**    | 1 000 / **0**       |
| Requests served                       | 39 357 (3 169/s) | **54 422 (4 532/s)** |
| Attempts not started (dropped)        | 15 642           | **577**             |
| Sold-out rejection p50 / p99          | 523 ms / 794 ms  | **0.40 ms / 6.5 ms** |
| Successful reservation p50 / p99      | 483 ms / 1.03 s  | 434 ms / 899 ms     |
| Timeouts (503)                        | 0                | 0                   |

This is what the gate is for. Rejections never touch PostgreSQL: the deal is
read from the cache and the Lua script answers in memory, so **sold-out p99
drops from 794 ms to 6.5 ms** (target < 10 ms ✅), and the service absorbs
almost the whole offered load instead of dropping 28 % of it.

Successful reservations still serialize on the single `deals` row, so their
p99 (899 ms) remains far from the < 50 ms target ❌. The gate cannot help
there: every sale must be written.

## Scenario A: opening burst with a large stock

`make load` defaults: 10 000 units at 5 000 attempts/s.

| Metric                             | Baseline        | Phase 4, run 1   | Phase 4, run 2   |
| ---------------------------------- | --------------- | ---------------- | ---------------- |
| Units sold / oversold              | 10 000 / **0**  | 10 000 / **0**   | 10 000 / **0**   |
| Requests served                    | 13 489 (588/s)  | 34 604 (1 573/s) | 28 607 (1 172/s) |
| Sold-out rejection p99             | 4.16 s          | 22 ms            | 768 ms           |
| Successful reservation p99         | 4.43 s          | 4.64 s           | 4.90 s           |
| Timeouts (503, all rolled back)    | 0               | 5 909 (17 %)     | 8 818 (31 %)     |

Here the stock is larger than what PostgreSQL can write within the request
timeout: about 410 to 450 sales per second on one row. The gate admits buyers
as long as Redis has units, those requests queue for pool connections and the
row lock, and some hit the 5 s timeout. Each timeout compensates the gate,
which returns the unit to Redis and admits another buyer, who queues again.
The loop never oversells (the database matched Redis exactly after every
run), but it turns admitted buyers into timeouts and makes runs vary widely.

## Bottleneck carried to phase 7

The remaining bottleneck is the write path, not the gate. Candidates to
evaluate with pprof and these same scenarios:

- **Backpressure**: bound in-flight PostgreSQL writes, for example to the
  pool size, and reject the excess quickly with a retryable 503. Queueing
  until the timeout then compensating is the worst of both.
- **A shorter write budget** than the request timeout, so a queued
  reservation gives its unit back while other buyers can still use it.
- **Fewer round trips per sale**: insert and stock update in one statement.

Phase 7 profiled it: the deal row stayed locked while each `COMMIT` waited for
its WAL fsync. Committing reservation transactions asynchronously raised
the write rate from about 480 to about 2,000 units per second; see
[write-path.md](write-path.md) and ADR 0006.

## Correctness in every run

Zero oversell in all five runs. After each one, PostgreSQL's `reserved`
equals the number of reservation rows, and Redis's counter is 0 for every
sold-out deal.
