# JWT authentication: the load scenarios measured again

Every reservation now carries a Bearer JWT that the API verifies before the
use case runs ([ADR 0008](../../docs/adr/0008-jwt-authentication.md)). The
numbers in the README were measured before that change, so both scenarios
were run again against the same stack to check the cost of the check.

## Environment

Same as [phase 7](write-path.md): the full local stack (`make up`) with
the API, PostgreSQL 17, Redis 7.4, Jaeger, Prometheus and Grafana, sharing a
Docker Desktop VM (4 CPUs, 9.7 GiB) with k6. The pool has 20 connections,
`REQUEST_TIMEOUT` is 5 s and tracing samples 10 % of new traces. k6 signs a fresh
HS256 token for every buyer ([`jwt.js`](../jwt.js)), and the API verifies it
on every reservation.

## Scenario B: 1,000 units, about 55,000 buyers

`STOCK=1000 PEAK_RPS=5000 RAMP=2s HOLD=10s make load`

| Metric                           | Before JWT (phase 7) | With JWT             |
| -------------------------------- | -------------------- | -------------------- |
| Units sold / oversold            | 1 000 / **0**        | 1 000 / **0**        |
| Requests served                  | 55 000 (4 584/s)     | 54 997 (4 580/s)     |
| Sold-out rejection p50 / p99     | 0.45 ms / 7.8 ms     | 0.45 ms / 7.4 ms     |
| Successful reservation p50 / p99 | 0.96 ms / 86 ms      | 0.94 ms / 91 ms      |
| Timeouts                         | 0                    | 0                    |

## Scenario A: 10,000 units at 5,000 attempts per second

`make load` defaults.

| Metric                           | Before JWT (phase 7) | With JWT             |
| -------------------------------- | -------------------- | -------------------- |
| Units sold / oversold            | 10 000 / **0**       | 10 000 / **0**       |
| Requests served                  | 96 841 (4 402/s)     | 96 177 (4 370/s)     |
| Attempts not started (dropped)   | 8 157                | 8 822                |
| Sold-out rejection p50 / p99     | 0.48 ms / 562 ms     | 0.48 ms / 547 ms     |
| Successful reservation p50 / p99 | 286 ms / 986 ms      | 284 ms / 967 ms      |
| Timeouts (503)                   | 0                    | 0                    |

## Reading

Verifying an HS256 signature is a few microseconds of CPU, well below the
noise between two runs on a shared VM. Every difference above is within
that noise, in both directions. The design targets met and missed are the
same as before JWT:

| Target                              | Scenario B | Scenario A |
| ----------------------------------- | ---------- | ---------- |
| 0 units oversold                    | ✅         | ✅         |
| Sold-out rejection p99 < 10 ms      | ✅ 7.4 ms  | ❌ 547 ms  |
| Successful reservation p99 < 50 ms  | ❌ 91 ms   | ❌ 967 ms  |
| 5xx rate < 1 %                      | ✅ 0 %     | ✅ 0 %     |

The missed targets have the same cause as in phase 7: one row per deal and
four shared CPUs. They stay as written.

## How to reproduce

```bash
make up
make migrate
STOCK=1000 PEAK_RPS=5000 RAMP=2s HOLD=10s make load
make load
```
