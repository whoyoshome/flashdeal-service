# Stock shards: 1 row vs 8 rows, 1 vs 2 replicas

Measured on 2026-10-27 on the 4-CPU development VM, which also runs k6 and
the observability stack. Design and decision:
[ADR 0011](../../../docs/adr/0011-stock-shards.md).

Each configuration was run several times in a row. The first run after the
API starts is reported separately ("cold") because it also opens pool
connections. The others are "warm". All runs used the full thresholds:
`reserve_ok` p99 < 50 ms and `reserve_sold_out` p99 < 10 ms.

## Scenario B: 1,000 units, 5,000 attempts per second (Compose, 1 replica)

`STOCK=1000 PEAK_RPS=5000 RAMP=2s HOLD=10s make load`

| Configuration | `reserve_ok` p99, cold | `reserve_ok` p99, warm runs | `reserve_sold_out` p99 |
| --- | --- | --- | --- |
| 1 shard (`STOCK_SHARDS=1`) | 90.42 ms | 19.97 ms, 16.02 ms | 7.3–9.5 ms |
| 8 shards | 79.63 ms | 7.09, 10.11, 10.87, 7.70 ms | 8–10.5 ms |
| 8 shards, `POSTGRES_MIN_CONNS=20` | 51.47 ms | 10.51 ms | under 10 ms |
| Final build: the above plus the shard savepoint | 43.96 ms | 10.49 ms, 10.98 ms | 13.46 ms cold, 7.8–8.4 ms warm |

Every run sold exactly 1,000 units, with no oversell, no unexpected
responses and no timeouts. Two runs crossed the 10 ms sold-out threshold:
one 8-shard run at 10.48 ms and the final build's cold run at 13.46 ms.
That path only touches Redis.

The final build wraps each shard attempt in a savepoint. It is sent in the
same round trip as the attempt, so it adds no round trip. An integration
test found that, without it, two reservations retrying under contention
could deadlock (ADR 0011).

With a warm API, the 50 ms target is met with about a 5× margin, and 8
shards halve the 1-shard tail. Most of phase 7's 86 ms was cold start. A
pool kept open at its maximum size cut the first-burst p99 from about
80 ms to 51 ms, and empty-pool acquires during that burst dropped to 46.
`POSTGRES_MIN_CONNS=20` is now the default in Compose and Kubernetes.

## Scenario A: 10,000 units, 5,000 attempts per second (Compose, 1 replica)

| Configuration | `reserve_ok` p50 | `reserve_ok` p99 |
| --- | --- | --- |
| 1 shard | 296 ms | 872 ms |
| 8 shards | 100–200 ms | 519–659 ms |
| 8 shards, 50-connection pool | — | 1.45 s, 3.6 % timeouts |

Shards help, but this scenario stays far from the target on this
machine. About 8,800 of 10,000 pool acquires had to wait, for about 146 ms
on average (`db_pool_empty_acquires_total`,
`db_pool_empty_acquire_wait_seconds_total`). A bigger pool was worse
because the CPU is shared with k6 and PostgreSQL. The limit here is
hardware, not a row lock.

## Two replicas on kind

Same manifests and procedure as [multi-replica.md](multi-replica.md),
8 shards, `POSTGRES_MIN_CONNS=20`, traffic through `kubectl port-forward`.
These runs used the build before the shard savepoint. On Compose, the
savepoint did not change the warm p99.

`STOCK=1000 PEAK_RPS=1000 RAMP=2s HOLD=8s make load-k8s` (full thresholds):

| Run | `reserve_ok` p50 / p99 | `reserve_sold_out` p99 | Result |
| --- | --- | --- | --- |
| 1 (cold) | 2.15 / 89.93 ms | 5.92 ms | p99 threshold crossed |
| 2 | 2.18 / 14.69 ms | 4.32 ms | passed |
| 3 | 2.17 / 16.67 ms | 5.76 ms | passed |

Before shards, the same run gave 72.56 ms. Every run sold exactly 1,000
units, with no oversell and no failed requests.

At 5,000 attempts per second the tunnel became the bottleneck. k6 dropped
4,300 to 7,000 iterations, and the sold-out p99 (Redis only) rose to
365–551 ms. Those numbers measure `kubectl port-forward`, not the service.
A real ingress or in-cluster load generator is needed to measure that rate
on Kubernetes.

## How to reproduce

```bash
# 1 shard vs 8 shards (run each 2–3 times; the first run is cold)
STOCK_SHARDS=1 docker compose up -d --wait api
STOCK=1000 PEAK_RPS=5000 RAMP=2s HOLD=10s make load
docker compose up -d --wait api
STOCK=1000 PEAK_RPS=5000 RAMP=2s HOLD=10s make load

# Two replicas: build the image and start kind as in multi-replica.md, then
kubectl -n flashdeal port-forward svc/flashdeal-api 18080:8080 &
make load-k8s STOCK=1000 PEAK_RPS=1000 RAMP=2s HOLD=8s
```
