# CLAUDE.md — FlashDeal Service

High-concurrency flash-sale API in Go. Sells a limited stock during a short window with **zero overselling** under heavy load.

**Source of truth:** `docs/ARCHITECTURE.md`. Read the relevant section before starting any task. If a request contradicts it, stop and ask; do not silently diverge.

## Language

- Talk to the developer in **Spanish**.
- Everything committed to the repository is in **English**: identifiers, comments, log messages, error messages, API fields, test names, commit messages, PR titles and descriptions, README, ADRs, OpenAPI spec.
- Only exception: `docs/ARCHITECTURE.md` (Spanish design document).

## Commands

- `make up` / `make down` — start/stop Postgres, Redis, the API, Jaeger, Prometheus and Grafana via Docker Compose (`make up PROFILES=` skips the observability services, as CI does)
- `make migrate` — apply migrations
- `make test` — unit tests with `-race`
- `make test-integration` — integration tests (`-tags=integration`, testcontainers)
- `make test-chaos` — chaos tests against the running stack (`-tags=chaos`): Redis stopped and SIGTERM under load
- `make lint` — `golangci-lint run` + `govulncheck ./...`
- `make lint-api` — validate `api/openapi.yaml` with Redocly (Docker); run it whenever the contract changes
- `make lint-k8s` — validate `deploy/k8s/` with kubeconform (Docker); run it whenever a manifest changes
- `make load` — k6 scenario in `test/load`
- `make demo` — `make up` + `make migrate` + a short k6 flash sale (correctness thresholds only) for a first look
- `make token` — mint a local HS256 JWT (`TOKEN_SUB`, `TOKEN_ROLE=user|admin`)
- Before declaring any task done: `make lint test` must pass.

## Workflow

1. Work on **one phase** of `docs/ARCHITECTURE.md` (section 7) at a time.
2. Before writing code, show a short plan: files to create/modify and why.
3. Ask for approval before: adding a dependency, changing the DB schema, changing the public API contract, or touching more than ~8 files.
4. Write tests together with the code, never "later".
5. Finish with a summary: what changed, how it was verified, what is pending.
6. Never delete, skip or weaken a failing test to get green. Fix the code or explain the blocker.
7. If something is ambiguous at the business level, ask instead of inventing behavior.

## Architecture rules (strict)

Dependencies point **inward**: `delivery → usecase → domain`. Adapters in `repository` implement ports declared in `usecase`. Enforced with `depguard` in `.golangci.yml`.

| Package | May import | Must never import |
| --- | --- | --- |
| `internal/domain` | stdlib only | `pgx`, `redis`, `net/http`, `encoding/json`, any other `internal/*` |
| `internal/usecase` | `domain`, stdlib, `golang.org/x/sync` | `delivery`, `repository`, `pgx`, `redis`, `net/http` |
| `internal/delivery/httpapi` | `usecase`, `domain`, `chi`, `platform` | `repository`, `pgx`, `redis` |
| `internal/repository/postgres` | `domain`, `usecase` (ports), `pgx`, `platform`, `golang.org/x/sync` | `delivery`, `redis` |
| `internal/repository/redisrepo` | `domain`, `usecase` (ports), `go-redis`, `platform`, `golang.org/x/sync` | `delivery`, `pgx` |
| `internal/worker` | `usecase`, `domain`, `platform` | `delivery`, `repository` |
| `internal/platform` | stdlib, infra libs | `usecase`, `delivery`, `repository` |
| `cmd/api` | everything | — (composition root only, no business logic) |

Rules:

- **Ports** (interfaces) live in `internal/usecase/ports.go`, next to their consumer. Keep them small (1–3 methods).
- **Accept interfaces, return structs.** Constructors are `NewX(deps...) *X` with explicit dependencies.
- Each adapter has a compile-time assertion: `var _ usecase.StockGate = (*StockGate)(nil)`.
- **Domain entities** have no `json`/`db` tags, validate invariants in constructors and methods, and expose state transitions as methods (`r.Confirm(now)`), not field mutation from outside.
- **DTOs** exist only in `delivery/httpapi/dto.go`. **Row structs** exist only in `repository/postgres/rows.go`. Map explicitly; never pass them across layers.
- Handlers are thin: decode → validate → call use case → map response. No business rules in handlers or SQL.
- `cmd/api/main.go` is the only place that wires dependencies. Manual DI; no Wire/Fx/global containers.
- A use case with more than ~6 dependencies is doing too much: split it.
- Money is `int64` cents + ISO 4217 currency. Never `float64`.

## Go conventions

- `gofmt`/`goimports` clean; `golangci-lint` with zero warnings.
- `context.Context` is the first parameter of every I/O function. Never store a context in a struct.
- Package names: short, lowercase, singular, no underscores. Avoid stutter. Use `httpapi` and `redisrepo` to avoid shadowing `net/http` and `go-redis`.
- Initialisms in caps: `ID`, `HTTP`, `URL`, `JSON` (`dealID`, not `dealId`).
- Every exported identifier has a doc comment starting with its name. Comments explain **why**, not what.
- Prefer early returns; no `else` after `return`; no naked returns.
- Inject `Clock` and `IDGenerator`; never call `time.Now()` or generate UUIDs directly inside `domain` or `usecase`. Store times in UTC.
- Configuration is loaded once in `platform/config` and passed as a struct. No `os.Getenv` elsewhere.
- No global mutable state. No `init()` with side effects.

## Error handling

- Errors are values. Business errors are `*domain.Error` with a stable `Code` (see `internal/domain/errors.go`).
- Wrap with context using `%w`, lowercase, no "failed to": `fmt.Errorf("load deal %d: %w", id, err)`.
- Compare with `errors.Is` / `errors.As`, never by string.
- Repositories translate driver errors to domain errors (`pgx.ErrNoRows` → `ErrDealNotFound`; constraint violations by **constraint name**). No `pgx`/`redis` error types escape a repository.
- HTTP status mapping lives only in `delivery/httpapi/errors.go`. Responses use `{"code","message","request_id"}`; never return raw `err.Error()` to clients.
- Log an error **once**, at the edge (handler or worker). Inner layers only wrap.
- `panic` only for programmer errors detectable at startup (e.g. missing embedded Lua script). Never in the request path. No `log.Fatal` outside `main`.
- Redis unavailable → `ErrStockUnavailable` (503). The service fails **closed**.

## Concurrency

- Stock correctness lives **only** in Redis Lua scripts and Postgres conditional `UPDATE`s + `CHECK` constraints. Never coordinate stock with an in-process `sync.Mutex` (there are N replicas).
- Never implement read-modify-write on stock (`SELECT` then `UPDATE`).
- Every goroutine has an owner, an exit path via `ctx.Done()`, and is awaited (`errgroup` or `sync.WaitGroup`). No fire-and-forget `go f()`.
- Never use `r.Context()` for work that outlives the request; use `context.WithoutCancel(ctx)` plus its own timeout (e.g. stock compensation).
- Bound concurrency with `errgroup.SetLimit`. No unbounded goroutine-per-item loops.
- Only the sender closes a channel.
- Every external call (Postgres, Redis, HTTP) has a timeout.
- Hot reads use a short-TTL cache + `singleflight`.

## Data access

- Explicit, parameterized SQL (`$1`, `$2`) as constants in the repository package. No ORM, no string concatenation.
- Transactions only through `TxManager.WithinTx(ctx, fn)`; the transaction travels in the context. Use cases never see `pgx.Tx`.
- Migrations: add new numbered files in `migrations/` (up + down). Never edit a migration that has been applied.
- Redis keys use hash tags so related keys share a slot: `deal:{id}:stock`, `deal:{id}:buyers`.
- Lua scripts live in `repository/redisrepo/scripts/`, are embedded with `//go:embed` and executed via `redis.NewScript` (EVALSHA).

## HTTP API

- Router: `go-chi/chi/v5` on top of `net/http`. Versioned routes under `/v1`.
- Middleware order: request ID → recoverer; on `/v1`: tracing → logging → metrics → timeout → auth (JWT) → (admin: RequireAdmin) → (create: idempotency).
- `POST` endpoints that create resources require an `Idempotency-Key` header.
- Any contract change updates `api/openapi.yaml` in the same commit.
- `http.Server` always sets `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`. Graceful shutdown on `SIGTERM`.

## Observability

- Logging: `log/slog`, JSON, structured attributes (`request_id`, `deal_id`, `user_id`, `reservation_id`). No `fmt.Println` or `log.Printf`.
- Never log secrets, tokens or full request bodies.
- Metrics: Prometheus; include business metrics (`reservations_total{result}`, `stock_remaining`, `reservations_expired_total`).
- Tracing: OpenTelemetry spans around use cases and every Redis/Postgres call.

## Testing

- Unit tests: table-driven, `t.Run` with behavior-describing names in English (`"returns ErrSoldOut when stock is exhausted"`), `t.Parallel()` where safe.
- Use-case tests use hand-written fakes from `usecase/fakes_test.go`. No mock generators by default.
- Integration tests: `//go:build integration`, `testcontainers-go`, real Postgres and Redis. Never mock the database in repository tests.
- Concurrency invariants (no oversell, per-user limit, idempotency) must have integration tests run with `-race`.
- Assertions: `testify/require`.
- Use `t.Context()` and a fake `Clock` for time-dependent logic; no `time.Sleep` in tests.

## Dependencies

Allowed: `go-chi/chi/v5`, `jackc/pgx/v5`, `redis/go-redis/v9`, `golang-migrate/migrate`, `caarlos0/env`, `prometheus/client_golang`, OpenTelemetry SDK, `golang.org/x/sync`, a UUID library, `stretchr/testify`, `testcontainers-go`, `golang-jwt/jwt/v5` (ADR 0008).

Anything else requires an ADR in `docs/adr/` and explicit approval. Never add a web framework (Gin, Echo, Fiber), an ORM (GORM) or a DI framework.

## Git

- **Conventional Commits**, English, imperative mood, subject ≤ 72 chars:
  `feat(reservation): add idempotency middleware`
  Types: `feat`, `fix`, `refactor`, `perf`, `test`, `docs`, `build`, `ci`, `chore`.
- Body explains **why**, wrapped at 72 chars. One logical change per commit.
- Branches: `feat/<short-topic>`, `fix/<short-topic>`.
- Never commit secrets, `.env` files or binaries. Use `.env.example`.
- Never force-push, rebase shared branches or rewrite history without explicit approval.

## Never

- `panic` for business or I/O errors; `log.Fatal` outside `main`.
- Global mutable variables or `init()` side effects.
- `json`/`db` tags on domain entities.
- Business logic in handlers, repositories or SQL.
- Read-modify-write on stock, or in-process locks for stock.
- Goroutines without an owner and an exit path.
- Raw internal errors in API responses.
- Packages named `utils`, `common`, `helpers` or `models`.
- Spanish in code, comments, logs, commits or docs other than `docs/ARCHITECTURE.md`.

## Definition of done

- [ ] `make lint test` passes (includes `-race`); `make test-integration` passes if repositories changed.
- [ ] New behavior has tests; concurrency-sensitive code has an integration test.
- [ ] No layer rule violated (depguard clean).
- [ ] `api/openapi.yaml`, migrations and README updated if affected.
- [ ] Non-obvious decisions recorded as an ADR.
- [ ] Commit message follows Conventional Commits.
