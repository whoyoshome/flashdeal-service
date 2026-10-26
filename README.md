# FlashDeal Service

High-concurrency flash-sale API in Go. It sells a limited stock during a short
window with **zero overselling** under heavy load.

> **Status:** phase 3 (use cases and HTTP API on a PostgreSQL-only baseline).
> The Redis stock gate arrives in phase 4. See the implementation plan in
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

### Known limitations (phase 3)

- Admin endpoints (`POST /v1/deals`, `/activate`) are not authenticated yet.
- `X-User-ID` stands in for real authentication; the middleware is the seam
  where JWT validation will go.
- The per-user limit is enforced per request only; the cumulative limit
  arrives with the Redis Lua gate in phase 4.
- Unconfirmed reservations keep their units until the expiry worker lands in
  phase 5.
- Under contention every reservation serializes on one `deals` row; see the
  [PostgreSQL-only baseline](test/load/results/baseline-postgres.md). The
  Redis gate in phase 4 exists to remove this bottleneck.

## Make targets

| Target                  | What it does                                            |
| ----------------------- | ------------------------------------------------------- |
| `make up` / `make down` | Start / stop the local stack                            |
| `make migrate`          | Apply database migrations (golang-migrate container)    |
| `make test`             | Unit tests with the race detector                       |
| `make test-integration` | Integration tests against real Postgres and Redis       |
| `make lint`             | `golangci-lint` (including layer rules) + `govulncheck` |
| `make lint-api`         | Validate `api/openapi.yaml` with Redocly (Docker)        |
| `make load`             | k6 flash-sale scenario against the running stack        |
| `make build`            | Build the binary into `bin/`                            |

## Configuration

All configuration comes from environment variables; see
[`.env.example`](.env.example) for the full list and defaults.

## Project layout and rules

- Design and rationale: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) (Spanish)
- Engineering rules for contributors and coding agents: [`CLAUDE.md`](CLAUDE.md)
