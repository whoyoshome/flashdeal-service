# Load tests

[`flash_sale.js`](flash_sale.js) is a [k6](https://k6.io) scenario of a flash
sale opening: a burst of distinct buyers hits one deal at the same time, each
trying to reserve one unit.

It answers two questions:

1. **Correctness**: is any unit oversold? The run fails if more reservations
   succeed than the deal has stock, or if any response is neither `201` nor
   `409 SOLD_OUT`. Teardown reads the deal back and checks that the stock is
   fully reserved and never negative.
2. **Latency**: how long do successful reservations and sold-out rejections
   take? They are recorded as separate trends (`reserve_ok_duration`,
   `reserve_sold_out_duration`), because the architecture targets them
   separately (p99 < 50 ms and p99 < 10 ms).

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

For example, a gentle run: `STOCK=1000 PEAK_RPS=1000 RAMP=10s HOLD=30s make load`.

## Reading the summary

- `dropped_iterations` counts attempts k6 could not even start because all
  VUs were still waiting for responses: offered load the service did not
  absorb. It is a throughput signal, not a k6 error.
- k6, the API, PostgreSQL and Redis share the Docker VM, so absolute numbers
  depend on the machine. Compare runs made on the same machine only.

## Results

- [Phase 3 baseline: PostgreSQL only](results/baseline-postgres.md)
