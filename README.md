# FlashDeal Service

High-concurrency flash-sale API in Go. It sells a limited stock during a short
window with **zero overselling** under heavy load.

> **Status:** phase 0 (repository bootstrap). The business API arrives in later
> phases; see the implementation plan in
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
curl -i localhost:8080/readyz
make down
```

`make up` builds the API image and starts PostgreSQL and Redis, waiting until
they are healthy.

## Endpoints

| Method | Path       | Purpose                                                        |
| ------ | ---------- | -------------------------------------------------------------- |
| GET    | `/healthz` | Liveness: the process is up (never checks dependencies)        |
| GET    | `/readyz`  | Readiness: PostgreSQL and Redis are reachable (`200` or `503`) |

## Make targets

| Target                  | What it does                                            |
| ----------------------- | ------------------------------------------------------- |
| `make up` / `make down` | Start / stop the local stack                            |
| `make migrate`          | Apply database migrations (golang-migrate container)    |
| `make test`             | Unit tests with the race detector                       |
| `make test-integration` | Integration tests against real Postgres and Redis       |
| `make lint`             | `golangci-lint` (including layer rules) + `govulncheck` |
| `make load`             | k6 load scenario (phase 3)                              |
| `make build`            | Build the binary into `bin/`                            |

## Configuration

All configuration comes from environment variables; see
[`.env.example`](.env.example) for the full list and defaults.

## Project layout and rules

- Design and rationale: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) (Spanish)
- Engineering rules for contributors and coding agents: [`CLAUDE.md`](CLAUDE.md)
