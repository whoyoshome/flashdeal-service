# FlashDeal Service

High-concurrency flash-sale API in Go. It sells a limited stock during a short
window with **zero overselling** under heavy load.

> **Status:** phase 4 (Redis stock gate). Reservations pass an atomic Lua gate
> in Redis before reaching PostgreSQL. See the implementation plan in
> [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) (section 7).

## Requirements

- Go (version pinned in `go.mod`; the `toolchain` line is fetched automatically)
- Docker with Compose v2
- GNU make
- `golangci-lint` v2 and `govulncheck` for `make lint`
- On Windows, a C compiler (for example MinGW-w64) for `go test -race`

## Quick start

```bash
make up
make migrate
curl -i localhost:8080/readyz
make down
```

`make up` builds the API image and starts PostgreSQL and Redis, waiting until
they are healthy. `make migrate` applies the SQL schema in
[`migrations/`](migrations).

`make test-integration` starts a disposable PostgreSQL with testcontainers, so
Docker must be running.

## Endpoints

The full contract is in [`api/openapi.yaml`](api/openapi.yaml). Every error
uses the body `{"code", "message", "request_id"}`.

| Method   | Path                            | Purpose                                                        |
| -------- | ------------------------------- | -------------------------------------------------------------- |
| `POST`   | `/v1/deals`                     | Create a deal (admin)                                          |
| `POST`   | `/v1/deals/{id}/activate`       | Open a deal's stock for reservations (admin)                  |
| `GET`    | `/v1/deals/{id}`                | Get a deal with its approximate available stock                |
| `GET`    | `/v1/deals?status=active`       | List active deals (cursor pagination)                          |
| `POST`   | `/v1/deals/{id}/reservations`   | Reserve units; needs `X-User-ID` and `Idempotency-Key`         |
| `POST`   | `/v1/reservations/{id}/confirm` | Confirm a reservation and create its order (`X-User-ID`)       |
| `DELETE` | `/v1/reservations/{id}`         | Cancel a pending reservation (`X-User-ID`)                     |
| `GET`    | `/healthz`                      | Liveness: the process is up (never checks dependencies)        |
| `GET`    | `/readyz`                       | Readiness: PostgreSQL and Redis are reachable (`200` or `503`) |
| `GET`    | `/metrics`                      | Prometheus metrics (see [Observability](#observability))       |

### Behavior notes and known limitations

- A deal must be **activated** (`POST /v1/deals/{id}/activate`) before it can
  be reserved: activation loads its stock into the Redis gate.
- The per-user limit is cumulative across a user's reservations on a deal and
  holds under parallel requests (enforced atomically in Redis).
- If Redis is unreachable, reservations fail closed with `503
  STOCK_UNAVAILABLE`; nothing is sold without the gate.
- Reservations are idempotent per user and `Idempotency-Key`: a retry replays
  the original response (marked `Idempotency-Replayed: true`) for 24 h, and a
  retry while the first request is still running gets `409
  IDEMPOTENCY_IN_PROGRESS`.
- Admin endpoints (`POST /v1/deals`, `/activate`) are not authenticated yet.
- `X-User-ID` stands in for real authentication; the middleware is the seam
  where JWT validation will go.
- Unconfirmed reservations expire after `RESERVATION_TTL` (5 min); the
  expirer returns their units within `EXPIRE_EVERY`.
- Sold-out rejections are served from Redis in milliseconds, but successful
  reservations still serialize on one `deals` row (about 400 to 450 per second
  locally). See [phase 4 results](test/load/results/redis-gate.md); this is
  the bottleneck carried to phase 7.

## Make targets

| Target                  | What it does                                            |
| ----------------------- | ------------------------------------------------------- |
| `make up` / `make down` | Start / stop the local stack                            |
| `make migrate`          | Apply database migrations (golang-migrate container)    |
| `make test`             | Unit tests with the race detector                       |
| `make test-integration` | Integration tests against real Postgres and Redis       |
| `make test-e2e`         | End-to-end tests over HTTP against the running stack     |
| `make lint`             | `golangci-lint` (including layer rules) + `govulncheck` |
| `make lint-api`         | Validate `api/openapi.yaml` with Redocly (Docker)        |
| `make load`             | k6 flash-sale scenario against the running stack        |
| `make build`            | Build the binary into `bin/`                            |

## Configuration

All configuration comes from environment variables; see
[`.env.example`](.env.example) for the full list and defaults.

## Background workers

Workers run inside the API process, share its lifecycle and stop with it
on `SIGTERM`. Every replica runs them; they need no leader election.

- **Expirer** (`EXPIRE_EVERY`, default 5s): marks pending reservations
  past their TTL as `expired` and returns their units to the deal and to
  the Redis gate. Batches of `EXPIRE_BATCH_SIZE` rows are claimed with
  `FOR UPDATE SKIP LOCKED`, so replicas never process the same row.
- **Reconciler** (`RECONCILE_EVERY`, default 30s): compares the Redis gate
  of every active deal with PostgreSQL and, when a release was lost,
  restores the stock and the per-buyer holdings from PostgreSQL. It only
  repairs a disagreement that stood still for two consecutive passes, with
  a compare-and-set, so it never fights an operation in flight. Repairs are
  logged as `stock gate drift repaired`. See
  [ADR 0005](docs/adr/0005-stock-gate-reconciliation.md).
- **Stock sampler** (`STOCK_SAMPLE_EVERY`, default 5s): publishes the units
  left in the gate for every active deal as `stock_remaining{deal_id}`.

## Observability

`GET /metrics` serves Prometheus metrics:

| Metric                                     | What it shows                                                   |
| ------------------------------------------ | --------------------------------------------------------------- |
| `http_requests_total{method,route,status}` | Traffic per route pattern and status                            |
| `http_request_duration_seconds{method,route}` | Latency histogram per route pattern                          |
| `reservations_total{result}`               | Reservation attempts: `created`, `sold_out`, `user_limit_reached`, `request_timeout`, ... |
| `stock_remaining{deal_id}`                 | Units left in the Redis gate per active deal                    |
| `reservations_expired_total`               | Reservations returned to stock by the expirer                   |
| `stock_gate_repairs_total`                 | Gates corrected by the reconciler (each one is a lost update)   |
| `db_pool_*`                                | PostgreSQL pool usage and time spent waiting for a connection   |

Profiling (`net/http/pprof`) is served on a separate admin listener,
`ADMIN_ADDR` (default `:6060`), which Docker Compose publishes on
`127.0.0.1` only. For example, a 10-second CPU profile:

```bash
go tool pprof "http://localhost:6060/debug/pprof/profile?seconds=10"
```

### Tracing

With `TRACING_OTLP_ENDPOINT` set (an OTLP/HTTP collector such as Jaeger,
for example `http://jaeger:4318`), the service exports OpenTelemetry traces:

- one server span per `/v1` request, named after its route
  (`POST /v1/deals/{id}/reservations`), continuing the caller's trace when
  the request carries a W3C `traceparent` header;
- one client span per PostgreSQL query (`db.query.text` holds the
  parameterized SQL; values are never recorded) and per Redis command or
  pipeline;
- one span per worker pass (`worker.expire`, `worker.reconcile`,
  `worker.sample_stock`).

`TRACING_SAMPLE_RATIO` (default `1`) is the share of new traces kept.
Requests inside a sampled trace are always kept. Log lines written during a
traced request carry its `trace_id` and `span_id`. With no endpoint, tracing
is off and costs nothing.

## Project layout and rules

- Design and rationale: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) (Spanish)
- Engineering rules for contributors and coding agents: [`CLAUDE.md`](CLAUDE.md)
