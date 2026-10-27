# Load tests

[`flash_sale.js`](flash_sale.js) is a [k6](https://k6.io) scenario of a flash
sale opening: a burst of distinct buyers hits one deal at the same time, each
trying to reserve one unit.

It answers two questions:

1. **Correctness**: is any unit oversold? The run fails if more reservations
   succeed than the deal has stock, or if any response is something other than
   `201`, `409 SOLD_OUT` or `503 REQUEST_TIMEOUT`. Timeouts are reported as
   `reservations_timed_out`; they mean degraded availability, not
   overselling. Teardown reads the deal back and checks that the stock is
   fully reserved and never negative.
2. **Latency**: how long do successful reservations and sold-out rejections
   take? They are recorded as separate trends (`reserve_ok_duration`,
   `reserve_sold_out_duration`), because the architecture targets them
   separately. The run fails when a target is missed: successful
   reservation p99 < 50 ms, sold-out rejection p99 < 10 ms, and fewer than
   1 % of responses with a 5xx status (`server_errors`). The targets are not
   relaxed to make a run pass; see the phase 7 results for where they hold.

## Running it

k6 runs in Docker on the Compose network, so nothing needs to be installed:

```bash
make up
make migrate
make load
```

Each run creates and activates its own deal, so runs never interfere with each
other. The raw summary is written to `results/latest.json` (git-ignored);
curated results are committed as Markdown in [`results/`](results).

## Parameters

| Variable   | Default           | Meaning                      |
| ---------- | ----------------- | ---------------------------- |
| `STOCK`    | `10000`           | Units on sale                |
| `PEAK_RPS` | `5000`            | Peak reservation attempts/s  |
| `RAMP`     | `2s`              | Time to reach the peak       |
| `HOLD`     | `20s`             | Time held at the peak        |
| `BASE_URL` | `http://api:8080` | API address inside Compose   |
| `THRESHOLDS` | `all`           | `correctness` drops the latency and 5xx targets (`make demo` uses it) |

For example, a gentle run: `STOCK=1000 PEAK_RPS=1000 RAMP=10s HOLD=30s make load`.

## On Kubernetes

[`k8s/k6-job.yaml`](k8s/k6-job.yaml) runs the same scenario as a Job inside
the cluster, so traffic reaches the API Service directly. Through
`kubectl port-forward` (`make load-k8s`), the tunnel itself becomes the
bottleneck at 5,000 attempts per second. After `deploy/k8s/smoke/run.sh`
has deployed the API:

```bash
STOCK=10000 PEAK_RPS=5000 RAMP=2s HOLD=20s make load-k8s-job
```

[`k8s/run.sh`](k8s/run.sh) loads the scripts and parameters into
ConfigMaps, runs the Job, prints the k6 output and saves the summary to
`results/k8s-job.json` (`LOAD_K8S_SUMMARY` changes the path). A crossed
threshold fails the Job and the target, but the summary is still saved.

## Profiling PostgreSQL during a run

[`pg_profile.sh`](pg_profile.sh) runs `make load` while sampling
`pg_stat_activity` every 200 ms, diffing the WAL and transaction counters,
and sampling container CPU. It prints the share of active session time per
wait event (CPU, row locks, I/O, ...):

```bash
STOCK=10000 PEAK_RPS=5000 RAMP=2s HOLD=20s ./test/load/pg_profile.sh test/load/results/pg-profile
```

## Reading the summary

- `dropped_iterations` counts attempts k6 could not even start because all
  VUs were still waiting for responses: offered load the service did not
  absorb. It is a throughput signal, not a k6 error.
- k6, the API, PostgreSQL and Redis share the Docker VM, so absolute numbers
  depend on the machine. Compare runs made on the same machine only.

## Results

- [Phase 3 baseline: PostgreSQL only](results/baseline-postgres.md)
- [Phase 4: Redis gate and deal cache vs baseline](results/redis-gate.md)
- [Phase 6: a flash sale on the Grafana dashboard](results/observability.md)
- [Phase 7: the reservation write path, profiled and fixed](results/write-path.md)
- [Phase 7: chaos under load (Redis down, SIGTERM)](results/chaos.md)
- [Stock shards: 1 vs 8 rows, 1 vs 2 replicas](results/stock-shards.md)
- [k6 inside the cluster: scenarios A and B on 2 replicas](results/k8s-in-cluster.md)
- [Scenario A profiled: row-lock waits, and 32 shards](results/pg-profile.md)
