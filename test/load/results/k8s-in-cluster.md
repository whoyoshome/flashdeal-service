# k6 inside the cluster: scenarios A and B on 2 replicas

Measured on 2026-10-27 on a local kind cluster: the hardened manifests
(`deploy/k8s/`, 2 API pods), PostgreSQL and Redis from
`deploy/k8s/smoke/`, and k6 running as a Job in the same cluster
(`make load-k8s-job`). Traffic goes straight to the ClusterIP Service, so
the `kubectl port-forward` tunnel, which capped the
[earlier kind runs](stock-shards.md#two-replicas-on-kind), is gone.

**This is not dedicated hardware.** kind runs the API, PostgreSQL, Redis
and k6 on one node: the same 4-CPU, 10 GB Docker VM as the Compose runs.
The Compose stack was stopped during the runs. Image: `main` plus the
transactional outbox (ADR 0012), 8 stock shards, a pool of 20 open
connections per pod. Every run used the full thresholds:
`reserve_ok` p99 < 50 ms and `reserve_sold_out` p99 < 10 ms.

## Scenario B: 1,000 units, 5,000 attempts per second

`STOCK=1000 PEAK_RPS=5000 RAMP=2s HOLD=10s make load-k8s-job`

| Run | Requests/s | `reserve_ok` p50 / p99 | `reserve_sold_out` p99 | Dropped |
| --- | --- | --- | --- | --- |
| Warm-up (cold API) | about 4,600 | 1.36 / 133.32 ms | 11.76 ms | 0 |
| 1 | 4,619 | 1.24 / 18.05 ms | 10.82 ms | 0 |
| 2 | 4,579 | 1.18 / 10.76 ms | 12.55 ms | 0 |

Through `port-forward`, k6 dropped 4,300–7,000 iterations at this rate and
the sold-out p99 rose to 365–551 ms. Inside the cluster, every iteration
ran. The reservation p99 meets its target with a warm API. The sold-out
p99 is 0.8–2.6 ms over its target, so every run fails its thresholds.

## Scenario A: 10,000 units, 5,000 attempts per second

`STOCK=10000 PEAK_RPS=5000 RAMP=2s HOLD=20s make load-k8s-job`

| Run | Requests/s | `reserve_ok` p50 / p99 | `reserve_sold_out` p99 | Dropped | Timeouts |
| --- | --- | --- | --- | --- | --- |
| 1 | 4,704 | 59.03 / 347.88 ms | 16.02 ms | 1,966 (1.9 %) | 0 |
| 2 | 4,615 | 101.90 / 631.03 ms | 112.28 ms | 3,894 (3.7 %) | 0 |
| 3 | about 4,700 | 45.76 / 218.77 ms | 16.87 ms | 1,315 (1.3 %) | 0 |

Every run sold exactly 10,000 units, and none oversold, answered
unexpectedly or timed out. One replica on Compose gave a p99 of
519–659 ms ([stock-shards.md](stock-shards.md)); two replicas give
219–631 ms. Scenario A still misses the 50 ms target.

## Where the time goes

- **The API pods are not CPU-bound.** Their cgroups recorded
  `nr_throttled 0` against a 1-CPU limit. While scenario A ran, the kind
  node used 2.1–2.5 of the 4 CPUs (`docker stats`).
- **Requests wait for a database connection.** On one API pod, across all
  six runs, 10,085 of 17,726 pool acquires waited, about 99 ms each on
  average (`db_pool_empty_acquires_total`,
  `db_pool_empty_acquire_wait_seconds_total`).
- **PostgreSQL is the next suspect.** It runs as a single unlimited pod on
  the VM's virtual disk, next to everything else. These runs do not show
  whether its limit is CPU, WAL I/O or lock waits. That needs
  `pg_stat_activity` sampling on a node of its own, which is still the
  next step.

## How to reproduce

```bash
docker build -t flashdeal-service:ci .
go run sigs.k8s.io/kind@v0.30.0 create cluster --name flashdeal-smoke --wait 120s
go run sigs.k8s.io/kind@v0.30.0 load docker-image flashdeal-service:ci \
  postgres:17-alpine redis:7.4-alpine grafana/k6:2.3.0 --name flashdeal-smoke
IMAGE=flashdeal-service:ci ./deploy/k8s/smoke/run.sh

# One warm-up run, then the measured ones. A crossed threshold fails the
# Job, and the summary is still saved under test/load/results/.
STOCK=1000 PEAK_RPS=5000 RAMP=2s HOLD=10s make load-k8s-job
STOCK=10000 PEAK_RPS=5000 RAMP=2s HOLD=20s make load-k8s-job

go run sigs.k8s.io/kind@v0.30.0 delete cluster --name flashdeal-smoke
```
