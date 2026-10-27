# Phase 7: the reservation write path, profiled and fixed

Phase 4 left one bottleneck: successful reservations serialized on the deal's
row at about 450 per second ([redis-gate.md](redis-gate.md)). This run
profiles it, fixes it ([ADR 0006](../../docs/adr/0006-asynchronous-commit-for-reservations.md))
and measures the result with the same scenarios.

## Environment

The environment is the full local stack (`make up`): the API, PostgreSQL 17,
Redis 7.4, Jaeger, Prometheus and Grafana. It shares a Docker Desktop VM
(4 CPUs, 9.7 GiB) with k6. The pool has 20 connections, `REQUEST_TIMEOUT` is
5 s, and tracing samples 10 % of new traces. The "before" and "after" runs
were back to back, minutes apart.

## Diagnosis

While each run was in progress:

- **API CPU profile** (`/debug/pprof/profile?seconds=8` on the admin port):
  1.45 s of CPU in 8 s, or 18 % of one core. The top functions are
  `Syscall6` (31 %) and `futex` (13 %): network I/O and waiting. The Go
  process was not the bottleneck.
- **PostgreSQL wait events.** Every 100 ms, the run sampled the active
  sessions in `pg_stat_activity` and recorded each one's wait event.

  | Wait event              | Scenario A, before | Scenario A, after |
  | ----------------------- | ------------------ | ----------------- |
  | `Lock:transactionid`    | 965                | 125               |
  | `IO:WalSync`            | 41                 | 0                 |
  | `CPU:running`           | 41                 | 14                |
  | `LWLock:BufferContent`  | 34                 | 21                |

  Before the change, sessions queued for the deal row (`Lock:transactionid`)
  while its holder flushed the WAL (`IO:WalSync`, in 41 of the 60 sampling
  rounds). The row lock lasts from the stock `UPDATE` to `COMMIT`, so every
  sale waited for the previous sale's fsync.

## The fix

The reservation transaction runs `SET LOCAL synchronous_commit = off` before
locking the row, so `COMMIT` no longer waits for the disk. Orders and every
other write keep synchronous commits. The durability trade-off is in ADR 0006.

## Results

### Scenario A: 10,000 units at 5,000 attempts per second

`make load` defaults: a 2 s ramp to 5,000 attempts per second, held for 20 s.

| Metric                             | Before            | After                 |
| ---------------------------------- | ----------------- | --------------------- |
| Units sold / oversold              | 10 000 / **0**    | 10 000 / **0**        |
| Write rate (units per second)      | ~480              | **~2 000** (peak 2 238) |
| Time to write all units            | ~22 s             | **~6 s**              |
| Timeouts (503)                     | 9 278 (28 %)      | **0**                 |
| Requests served                    | 33 080            | **96 841**            |
| Attempts not started (dropped)     | 71 920            | **8 157**             |
| Successful reservation p50 / p99   | 2.25 s / 4.39 s   | 286 ms / 986 ms       |
| Sold-out rejection p50 / p99       | 0.62 ms / 649 ms  | 0.48 ms / 562 ms      |

The write rate comes from `rate(reservations_total{result="created"}[3s])`,
second by second. Before: 480 to 500 units a second for 22 s. After: 2,238,
2,213, 1,820 and 1,865 units a second, done in about 6 s.

Phase 4's feedback loop is gone: admitted buyers no longer queue until the
timeout, so no units go back to the gate to admit more buyers into the same
queue.

### Scenario B: the architecture document's case

`STOCK=1000 PEAK_RPS=5000 RAMP=2s HOLD=10s make load`, about 55,000 buyers
for 1,000 units.

| Metric                           | Before            | After               |
| -------------------------------- | ----------------- | ------------------- |
| Units sold / oversold            | 1 000 / **0**     | 1 000 / **0**       |
| Successful reservation p50       | 459 ms            | **0.96 ms**         |
| Successful reservation p99       | 1.04 s            | **86 ms**           |
| Sold-out rejection p50 / p99     | 0.46 ms / 18.9 ms | 0.45 ms / **7.8 ms** |
| Timeouts                         | 0                 | 0                   |

## Against the design targets

`flash_sale.js` now fails the run when a target from `docs/ARCHITECTURE.md`
is missed:

| Target                              | Scenario B        | Scenario A        |
| ----------------------------------- | ----------------- | ----------------- |
| 0 units oversold                    | ✅                | ✅                |
| Sold-out rejection p99 < 10 ms      | ✅ 7.8 ms         | ❌ 562 ms         |
| Successful reservation p99 < 50 ms  | ❌ 86 ms          | ❌ 986 ms         |
| 5xx rate < 1 %                      | ✅ 0 %            | ✅ 0 %            |

The p99 targets are still missed. They are left as written instead of being
relaxed to pass:

- **Scenario A.** 10,000 buyers are admitted in about 3 s, while one row
  accepts about 2,000 writes per second. The last admitted buyers queue for
  up to a second.
- **Scenario B.** 1,000 buyers arrive in under a second for the same row.
  The tail is their queue.
- **Sold-out tail in A.** The sold-out p99 in A comes from the same seconds:
  four CPUs run k6 at 5,000 requests per second, the API and the write
  burst.

What remains is one network round trip per sale on one row. Stock shards
(ADR 0006, alternatives) are the next lever if production needs more.

## How to reproduce

```bash
make up
make migrate
STOCK=1000 PEAK_RPS=5000 RAMP=2s HOLD=10s make load
make load
```

`make load` exits non-zero when a target is missed. The summary is still
written to `test/load/results/latest.json`.
