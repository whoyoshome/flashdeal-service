# FlashDeal Service — Documento de Arquitectura

Oct 15, 2026 · @Willian

## 0. Cómo usar este documento con Claude Code

Este documento es la fuente de verdad del diseño; el comportamiento del agente se rige por `CLAUDE.md` en la raíz del repositorio (contenido completo en 0.2). Cualquier código que contradiga ambos requiere actualizar primero el documento o registrar un ADR en `docs/adr/`.

- Exportar este documento a Markdown y guardarlo como `docs/ARCHITECTURE.md`.
- Copiar el bloque de la sección 0.2 a `CLAUDE.md` en la raíz. Claude Code lo carga automáticamente al iniciar cada sesión en el proyecto.
- Pedir a Claude Code una fase a la vez (sección 7), nunca "implementa todo". Cada fase termina con un commit revisado por ti.
- Cuando Claude Code cometa un error recurrente, agregar la regla correspondiente a `CLAUDE.md`: el archivo evoluciona con el proyecto.

### 0.1 Convenciones de idioma

Todo lo que entra al repositorio se escribe en inglés; solo este documento de diseño y la conversación con el agente están en español.

| Artefacto | Idioma | Ejemplo |
| --- | --- | --- |
| Identificadores (paquetes, tipos, funciones, variables) | Inglés | `ReserveInput`, `dealID` |
| Comentarios y doc comments | Inglés | `// Execute reserves stock atomically and persists the hold.` |
| Mensajes de log y de error | Inglés | `"stock compensation failed"`, `"deal sold out"` |
| Campos JSON y contrato OpenAPI | Inglés, `snake_case` | `"request_id"`, `"expires_at"` |
| Nombres de tests | Inglés, describen comportamiento | `"returns ErrSoldOut when stock is exhausted"` |
| Commits | Inglés, Conventional Commits, modo imperativo | `feat(reservation): add idempotency middleware` |
| Ramas, PRs, README, ADRs | Inglés | `feat/stock-gate`, `docs/adr/0001-redis-stock-gate.md` |
| `docs/ARCHITECTURE.md` | Español | Este documento |
| Conversación con Claude Code | Español | Regla explícita en `CLAUDE.md` |

El README en inglés amplía la audiencia del portafolio (líderes técnicos de cualquier país de la región) y es la convención en equipos de ingeniería que trabajan en español y portugués a la vez.

### 0.2 CLAUDE.md (raíz del repositorio)

Este es el contrato de trabajo del agente: reglas verificables, comandos exactos y una definición de terminado. Está en inglés porque gobierna artefactos en inglés y porque los modelos siguen con más precisión instrucciones escritas en el mismo idioma del código.

```markdown
# CLAUDE.md — FlashDeal Service

High-concurrency flash-sale API in Go. Sells a limited stock during a short window with **zero overselling** under heavy load.

**Source of truth:** `docs/ARCHITECTURE.md`. Read the relevant section before starting any task. If a request contradicts it, stop and ask; do not silently diverge.

## Language

- Talk to the developer in **Spanish**.
- Everything committed to the repository is in **English**: identifiers, comments, log messages, error messages, API fields, test names, commit messages, PR titles and descriptions, README, ADRs, OpenAPI spec.
- Only exception: `docs/ARCHITECTURE.md` (Spanish design document).

## Commands

- `make up` / `make down` — start/stop Postgres, Redis, Jaeger, Prometheus, Grafana via Docker Compose
- `make migrate` — apply migrations
- `make test` — unit tests with `-race`
- `make test-integration` — integration tests (`-tags=integration`, testcontainers)
- `make lint` — `golangci-lint run` + `govulncheck ./...`
- `make load` — k6 scenario in `test/load`
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
| `internal/repository/postgres` | `domain`, `usecase` (ports), `pgx`, `platform` | `delivery`, `redis` |
| `internal/repository/redisrepo` | `domain`, `usecase` (ports), `go-redis`, `platform` | `delivery`, `pgx` |
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
- Middleware order: request ID → recoverer → logging → metrics → timeout → auth.
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

Allowed: `go-chi/chi/v5`, `jackc/pgx/v5`, `redis/go-redis/v9`, `golang-migrate/migrate`, `caarlos0/env`, `prometheus/client_golang`, OpenTelemetry SDK, `golang.org/x/sync`, a UUID library, `stretchr/testify`, `testcontainers-go`.

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
```

## 1. Visión general y alcance

FlashDeal Service vende un stock limitado de un producto (por ejemplo, 1.000 unidades) durante una ventana corta, garantizando cero sobreventa aunque miles de usuarios compren en el mismo segundo.

El dominio es directo: una **Deal** (oferta) tiene stock, precio, ventana de vigencia y límite por usuario. Un usuario crea una **Reservation** (reserva con TTL, por defecto 5 minutos) y luego la confirma, generando una **Order**. Si no confirma a tiempo, la reserva expira y el stock vuelve a la oferta.

### Problemas de concurrencia que resuelve

| Problema | Qué pasa sin control | Cómo lo resuelve FlashDeal |
| --- | --- | --- |
| Sobreventa (race condition) | Dos requests leen stock = 1 y ambos venden | Decremento atómico en Redis (script Lua) + `CHECK` en PostgreSQL como última línea de defensa |
| Hot key / hot row | Toda la carga golpea una sola fila de `deals`; los locks serializan todo | Redis actúa como compuerta: solo pasan a PostgreSQL tantas requests como stock exista; el resto recibe `409 SOLD_OUT` en memoria |
| Thundering herd al abrir la oferta | Miles de lecturas de `GET /deals/{id}` saturan la base | Caché de lectura + `singleflight` para colapsar requests idénticas |
| Doble compra por reintento | El cliente reintenta tras un timeout y compra dos veces | Header `Idempotency-Key` + restricción `UNIQUE` en base de datos |
| Abuso por usuario | Un usuario acapara el stock | Límite por usuario evaluado dentro del mismo script atómico |
| Reservas abandonadas | Stock bloqueado para siempre | Worker de expiración con `FOR UPDATE SKIP LOCKED`, seguro con varias réplicas |
| Divergencia Redis vs PostgreSQL | Contadores desincronizados tras un fallo | Compensación inmediata + job de reconciliación periódico; PostgreSQL es la fuente de verdad |

### Alcance

Dentro: administración de ofertas, consulta de ofertas, reserva, confirmación, cancelación, expiración, idempotencia, observabilidad y pruebas de carga.

Fuera (simulado o delegado): pagos reales (la confirmación asume pago aprobado), un proveedor de identidad real (el usuario llega en un JWT HS256 firmado con un secreto compartido; integrar OAuth 2.0 / OIDC con JWKS queda fuera, ver ADR 0008), catálogo de productos y notificaciones.

### Requisitos no funcionales (objetivos a medir con k6)

| Atributo | Objetivo |
| --- | --- |
| Correctitud | 0 unidades sobrevendidas en cualquier prueba de carga |
| Latencia de reserva | p99 < 50 ms con la oferta activa en carga sostenida |
| Rechazo por agotado | p99 < 10 ms (no toca PostgreSQL) |
| Disponibilidad | Réplicas sin estado; apagado ordenado sin perder requests en vuelo |
| Degradación | Si Redis cae, el servicio falla cerrado (`503`) en lugar de vender sin control |

Los números son metas de diseño, no promesas: la fase 7 los mide y el README publica los resultados reales.

## 2. Arquitectura del sistema

El servicio es un monolito modular sin estado con Clean Architecture pragmática: cuatro capas, una sola regla de dependencias apuntando hacia el dominio, y réplicas horizontales detrás de un balanceador.

&#91;embedded content: arquitectura por capas · 5 capas, 2 almacenes\]

Domain no depende de nada; Repository implementa las interfaces que declara UseCase, y es la única capa que toca Redis y PostgreSQL.

### Capas y responsabilidades

| Capa | Paquete | Responsabilidad | No debe |
| --- | --- | --- | --- |
| Domain | `internal/domain` | Entidades, invariantes, value objects, errores de negocio | Importar `pgx`, `redis`, `net/http` ni tags `json`/`db` |
| UseCase | `internal/usecase` | Orquestar un caso de negocio; declarar las interfaces (ports) que necesita | Conocer HTTP, SQL o Redis concretos |
| Delivery | `internal/delivery/httpapi` | Decodificar y validar requests, mapear DTO ↔ dominio, mapear errores a HTTP | Contener reglas de negocio ni abrir transacciones |
| Repository | `internal/repository/postgres`, `internal/repository/redisrepo` | Implementar los ports con SQL y Lua; traducir errores de driver a errores de dominio | Filtrar tipos de `pgx` o `redis` hacia arriba |

La regla en Go: **las interfaces se declaran donde se consumen** (en `usecase`), son pequeñas (1 a 3 métodos) y los constructores aceptan interfaces y devuelven structs concretos. `cmd/api/main.go` es el único lugar que conoce todas las implementaciones (composition root).

### Flujo de datos: Delivery → UseCase → Repository

1. **Delivery** recibe `POST /v1/deals/{id}/reservations`, valida el body (cantidad > 0), toma el usuario del `sub` del JWT (validado por el middleware de auth), lee `Idempotency-Key`, y construye un `usecase.ReserveInput`. Nunca pasa `*http.Request` hacia abajo.
2. **UseCase** (`Reserve.Execute`) obtiene la oferta (caché + `singleflight`), valida invariantes de dominio (`deal.CanReserve(now, qty)`), y llama al port `StockGate.TryReserve`.
3. **Repository Redis** ejecuta un script Lua atómico: verifica stock y límite por usuario, decrementa y responde. Si no hay stock, devuelve `domain.ErrSoldOut` y el flujo termina sin tocar PostgreSQL.
4. **UseCase** crea la entidad `domain.Reservation` y llama a `ReservationRepository.Create` dentro de una transacción (`TxManager.WithinTx`).
5. **Repository PostgreSQL** inserta la reserva e incrementa `reserved` en uno de los shards de stock de la oferta (`deal_stock_shards`), elegido con `FOR NO KEY UPDATE SKIP LOCKED` y protegido por `WHERE reserved + sold + $qty <= capacity`. El shard queda guardado en `reservations.stock_shard`. Si ningún shard tiene sitio, devuelve `domain.ErrSoldOut` (defensa en profundidad). Ver ADR 0011.
6. Si el paso 5 falla, **UseCase** compensa con `StockGate.Release` usando `context.WithoutCancel` (la compensación no debe morir porque el cliente cerró la conexión).
7. **Delivery** mapea el resultado: `201` con el DTO de la reserva, o el error de dominio a su código HTTP (sección 5).

La confirmación (`POST /v1/reservations/{id}/confirm`) solo toca PostgreSQL: en una transacción bloquea la reserva (`SELECT ... FOR UPDATE`), verifica que siga `pending` y no expirada, la marca `confirmed`, mueve la unidad de `reserved` a `sold` en el shard de la reserva e inserta la `order`.

### Por qué Redis primero y PostgreSQL después

Con 1.000 unidades y 50.000 intentos, el 98 % de las requests debe fallar. Si todas llegaran a PostgreSQL, competirían por el lock de una sola fila. Con Redis como compuerta, PostgreSQL recibe como máximo \~1.000 escrituras por oferta, repartidas entre varios shards, y la restricción `CHECK (reserved + sold <= capacity)` de cada shard garantiza la correctitud aunque Redis diverja (las capacidades suman `total_stock`).

## 3. Stack tecnológico justificado

El criterio es librería estándar primero y dependencias solo cuando eliminan código propenso a errores; ningún framework decide la arquitectura por nosotros.

| Área | Elección | Por qué | Descartado y motivo |
| --- | --- | --- | --- |
| Lenguaje | Go, última versión estable fijada en `go.mod` | Concurrencia nativa, binario estático, arranque rápido, coincide con la oferta | — |
| HTTP | `net/http` + `go-chi/chi/v5` | Chi es 100 % compatible con `http.Handler`: grupos de rutas y middlewares sin encerrar el código en un framework | Gin/Echo (contexto propio, acoplamiento), Fiber (usa fasthttp, rompe compatibilidad con `net/http`). Alternativa válida: `http.ServeMux` con patrones método + ruta |
| PostgreSQL | `jackc/pgx/v5` + `pgxpool` | Driver nativo más rápido, soporte de tipos de Postgres, control fino de transacciones y locks | GORM (oculta SQL y locking, reflexión, N+1), `database/sql` + lib/pq (lib/pq está en mantenimiento) |
| Migraciones | `golang-migrate/migrate` | Archivos SQL versionados up/down, ejecutables en CI y en contenedor | Automigrate de ORMs (no auditable) |
| Redis | `redis/go-redis/v9` + scripts Lua | Atomicidad real en una sola llamada (`EVALSHA`), pipelines, soporte de Cluster | Locks distribuidos tipo Redlock (más lentos y frágiles para un contador) |
| Configuración | Variables de entorno + `caarlos0/env` | Twelve-Factor, validación y defaults en un struct tipado | Viper (pesado, demasiada magia para un servicio) |
| Logging | `log/slog` (stdlib) en JSON | Logs estructurados sin dependencia, con `request_id` y `trace_id` | Logrus (en mantenimiento), Zap (innecesario con slog) |
| Métricas | `prometheus/client_golang` | Estándar de facto; histogramas de latencia y contadores de negocio | — |
| Trazas | OpenTelemetry SDK + exportador OTLP (Jaeger en local) | Seguir una request a través de HTTP → Redis → Postgres | — |
| Concurrencia | `golang.org/x/sync` (`errgroup`, `singleflight`) | Ciclo de vida de goroutines con cancelación y deduplicación de cargas | — |
| Tests | `testing` + `testify/require` + `testcontainers-go` | Tests table-driven, integración contra Postgres y Redis reales | Mocks de base de datos (no prueban SQL ni Lua) |
| Mocks | Fakes escritos a mano en `usecase` | Interfaces pequeñas hacen los fakes triviales y legibles | Generadores de mocks para todo (tests frágiles) |
| Carga | k6 | Escenarios declarativos, umbrales que fallan el pipeline | — |
| Calidad | `golangci-lint`, `govulncheck`, `go test -race` | Linters, vulnerabilidades y data races en CI | — |
| Contenedores | Docker multi-stage + imagen `distroless/static` + Docker Compose | Imagen mínima, sin shell, usuario no root | — |
| CI | GitHub Actions | Lint, tests unitarios, integración y build en cada PR | — |
| Inyección de dependencias | Manual en `main.go` | Explícita, sin reflexión, sin generación de código | Wire, Fx/Dig (justificables en monorepos grandes, no aquí) |

Decisión explícita: el contrato de la API vive en `api/openapi.yaml`, escrito a mano y validado en CI; no se genera código del servidor a partir de él para mantener los handlers idiomáticos.

## 4. Estructura de directorios

Todo el código de negocio vive en `internal/` (el compilador impide importarlo desde fuera del módulo) y se organiza por capa; no existen carpetas `utils`, `common` ni `helpers`.

```text
flashdeal-service/
├── cmd/
│   └── api/
│       └── main.go                  # composition root: config, wiring, arranque y apagado
├── internal/
│   ├── domain/
│   │   ├── deal.go                  # entidad Deal + invariantes (CanReserve, IsActive)
│   │   ├── deal_test.go
│   │   ├── reservation.go           # entidad Reservation + transiciones de estado
│   │   ├── reservation_test.go
│   │   ├── order.go
│   │   ├── money.go                 # value object: centavos int64 + moneda ISO 4217
│   │   └── errors.go                # errores de dominio tipados
│   ├── usecase/
│   │   ├── ports.go                 # interfaces: DealRepository, StockGate, TxManager, Clock...
│   │   ├── reserve.go               # caso de uso: reservar
│   │   ├── reserve_test.go          # tests con fakes
│   │   ├── confirm.go
│   │   ├── cancel.go
│   │   ├── expire.go                # liberar reservas vencidas
│   │   ├── manage_deals.go          # crear y activar ofertas (admin)
│   │   └── fakes_test.go            # fakes en memoria de los ports
│   ├── delivery/
│   │   └── httpapi/                 # paquete httpapi: evita sombrear net/http
│   │       ├── router.go            # rutas chi y cadena de middlewares
│   │       ├── deal_handler.go
│   │       ├── reservation_handler.go
│   │       ├── dto.go               # structs de request/response con tags json
│   │       ├── errors.go            # mapeo error de dominio → status HTTP + JSON
│   │       ├── respond.go           # helpers de escritura JSON
│   │       ├── handler_test.go      # httptest contra fakes de usecase
│   │       └── middleware/
│   │           ├── request_id.go
│   │           ├── logging.go
│   │           ├── recoverer.go
│   │           ├── timeout.go
│   │           ├── auth.go          # JWT Bearer + RequireAdmin
│   │           └── metrics.go
│   ├── repository/
│   │   ├── postgres/
│   │   │   ├── pool.go              # pgxpool con límites y health check
│   │   │   ├── tx.go                # TxManager: transacción viaja en el context
│   │   │   ├── deal_repo.go
│   │   │   ├── reservation_repo.go
│   │   │   ├── order_repo.go
│   │   │   ├── rows.go              # structs de fila + mapeo fila ↔ dominio
│   │   │   ├── errors.go            # pgx.ErrNoRows / 23505 → errores de dominio
│   │   │   └── *_integration_test.go
│   │   └── redisrepo/               # paquete redisrepo: evita chocar con go-redis
│   │       ├── stock_gate.go        # TryReserve / Release / Load
│   │       ├── idempotency_store.go
│   │       ├── deal_cache.go
│   │       ├── scripts/
│   │       │   ├── reserve.lua
│   │       │   └── release.lua      # embebidos con //go:embed
│   │       └── *_integration_test.go
│   ├── worker/
│   │   ├── expirer.go               # ticker + lotes con SKIP LOCKED
│   │   └── reconciler.go            # compara Redis vs PostgreSQL y corrige
│   └── platform/                    # piezas transversales, sin lógica de negocio
│       ├── config/config.go
│       ├── logger/logger.go
│       ├── observability/           # métricas, tracing, pprof
│       └── clock/clock.go           # interfaz Clock para tests deterministas
├── migrations/
│   ├── 000001_create_deals.up.sql
│   ├── 000001_create_deals.down.sql
│   └── ...
├── api/
│   └── openapi.yaml
├── test/
│   ├── e2e/                         # flujo completo contra docker compose
│   └── load/
│       ├── flash_sale.js            # escenario k6: apertura de oferta
│       └── README.md
├── deploy/
│   ├── prometheus.yml
│   └── grafana/dashboards/
├── docs/
│   ├── ARCHITECTURE.md              # este documento
│   └── adr/
│       ├── 0001-redis-stock-gate.md
│       └── 0002-manual-dependency-injection.md
├── .github/workflows/ci.yml
├── .golangci.yml
├── docker-compose.yml               # postgres, redis, jaeger, prometheus, grafana, api
├── Dockerfile                       # multi-stage → distroless, usuario no root
├── Makefile                         # up, down, migrate, test, test-integration, lint, load
├── CLAUDE.md
├── go.mod
└── README.md
```

| Ruta | Para qué existe |
| --- | --- |
| `cmd/api` | Punto de entrada. Solo cablea; si `main.go` supera \~150 líneas, algo de lógica se filtró |
| `internal/domain` | El corazón: Go puro, testeable sin Docker. Ninguna dependencia externa |
| `internal/usecase` | Un archivo por caso de uso; los ports viven aquí porque el consumidor define la interfaz |
| `internal/delivery/httpapi` | Traducción HTTP ↔ aplicación. Los DTO están separados de las entidades |
| `internal/repository` | Adaptadores. Los structs con tags `db` nunca salen de este paquete |
| `internal/worker` | Procesos en segundo plano con el mismo ciclo de vida del servidor |
| `internal/platform` | Infraestructura transversal; no puede importar `usecase` ni `domain` salvo para tipos de error |
| `migrations` | Esquema versionado; la única forma de cambiar la base |
| `test/e2e`, `test/load` | Pruebas que necesitan el sistema completo levantado |
| `docs/adr` | Architecture Decision Records: contexto, decisión, consecuencias |

Los tests unitarios conviven con el código (`_test.go` en el mismo paquete). Los de integración usan el build tag `//go:build integration` para que `go test ./...` siga siendo rápido.

## 5. Decisiones de diseño y patrones

Tres decisiones sostienen el código: los errores son valores tipados que se traducen solo en el borde, la correctitud del stock nunca depende de memoria del proceso, y todas las dependencias se construyen a mano en un único lugar.

### 5.1 Manejo de errores sin panic

El dominio define errores con un código estable para clientes. Cada capa agrega contexto con `%w`; solo `delivery` traduce a HTTP; los detalles internos van al log, nunca a la respuesta.

```go
// internal/domain/errors.go
package domain

import "errors"

type Code string

const (
	CodeNotFound           Code = "NOT_FOUND"
	CodeValidation         Code = "VALIDATION_ERROR"
	CodeSoldOut            Code = "SOLD_OUT"
	CodeDealNotActive      Code = "DEAL_NOT_ACTIVE"
	CodeUserLimit          Code = "USER_LIMIT_REACHED"
	CodeReservationExpired Code = "RESERVATION_EXPIRED"
	CodeConflict           Code = "CONFLICT"
	CodeUnavailable        Code = "STOCK_UNAVAILABLE"
)

// Error is a business error with a stable, client-facing code.
type Error struct {
	Code    Code
	Message string
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

var (
	ErrDealNotFound       = &Error{Code: CodeNotFound, Message: "deal not found"}
	ErrSoldOut            = &Error{Code: CodeSoldOut, Message: "deal sold out"}
	ErrDealNotActive      = &Error{Code: CodeDealNotActive, Message: "deal is not active"}
	ErrUserLimitReached   = &Error{Code: CodeUserLimit, Message: "per-user limit reached"}
	ErrReservationExpired = &Error{Code: CodeReservationExpired, Message: "reservation expired"}
	ErrStockUnavailable   = &Error{Code: CodeUnavailable, Message: "stock service unavailable"}
)

func NewValidation(msg string) *Error { return &Error{Code: CodeValidation, Message: msg} }

// AsDomain finds a *Error anywhere in the wrapped error chain.
func AsDomain(err error) (*Error, bool) {
	var de *Error
	ok := errors.As(err, &de)
	return de, ok
}
```

El repositorio traduce errores del driver; nada de `pgx` sube de capa:

```go
// internal/repository/postgres/deal_repo.go
func (r *DealRepo) GetByID(ctx context.Context, id int64) (domain.Deal, error) {
	var row dealRow
	err := r.db(ctx).QueryRow(ctx, selectDealByID, id).Scan(row.scanTargets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Deal{}, domain.ErrDealNotFound
	}
	if err != nil {
		return domain.Deal{}, fmt.Errorf("query deal %d: %w", id, err)
	}
	return row.toDomain(), nil
}
```

Delivery mapea el código a HTTP y loguea una sola vez lo inesperado:

```go
// internal/delivery/httpapi/errors.go
func statusFor(c domain.Code) int {
	switch c {
	case domain.CodeNotFound:
		return http.StatusNotFound
	case domain.CodeValidation:
		return http.StatusBadRequest
	case domain.CodeSoldOut, domain.CodeDealNotActive, domain.CodeUserLimit, domain.CodeConflict:
		return http.StatusConflict
	case domain.CodeReservationExpired:
		return http.StatusGone
	case domain.CodeUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func (h *handlers) writeError(w http.ResponseWriter, r *http.Request, err error) {
	reqID := middleware.RequestIDFrom(r.Context())
	if de, ok := domain.AsDomain(err); ok {
		writeJSON(w, statusFor(de.Code), errorBody{Code: string(de.Code), Message: de.Message, RequestID: reqID})
		return
	}
	h.log.ErrorContext(r.Context(), "unhandled error", slog.Any("error", err))
	writeJSON(w, http.StatusInternalServerError, errorBody{Code: "INTERNAL", Message: "internal error", RequestID: reqID})
}
```

Reglas:

- Comparar con `errors.Is` / `errors.As`, nunca con `err.Error() == "..."`.
- Loguear un error una sola vez, en el borde (handler o worker); las capas internas solo envuelven.
- `panic` solo para errores de programador detectables al arrancar (por ejemplo, un script Lua embebido que no existe). El middleware `recoverer` es una red de seguridad que registra el stack y responde `500`, no un mecanismo de control de flujo.
- Timeouts y caídas de Redis se traducen a `ErrStockUnavailable` (`503`): el servicio falla cerrado.

### 5.2 Concurrencia y goroutines

Con varias réplicas, un `sync.Mutex` solo protege un proceso; por eso la atomicidad del stock vive en Redis y PostgreSQL, y las goroutines se usan para ciclo de vida y rendimiento.

| Dónde | Herramienta | Por qué |
| --- | --- | --- |
| Cada request HTTP | La goroutine que ya crea `net/http` | No lanzar goroutines extra dentro de los handlers |
| Reserva de stock | Script Lua en Redis + `UPDATE` condicional + `CHECK` | Atómico entre N réplicas |
| Lectura de una oferta caliente | Caché con TTL corto + `singleflight.Group` | 5.000 lecturas simultáneas se convierten en 1 consulta |
| Servidor HTTP + workers | `errgroup.WithContext` | Si uno falla, se cancela el resto y el apagado es ordenado |
| Expiración de reservas | `time.Ticker` + lotes con `FOR UPDATE SKIP LOCKED` | Varias réplicas procesan filas distintas sin coordinarse |
| Compensación tras un fallo | `context.WithoutCancel(ctx)` + timeout propio | No se pierde si el cliente cierra la conexión |
| Estado local en memoria | `sync.RWMutex` o `sync/atomic`; canales para transferir propiedad | Lo más simple que sea correcto |

Script de reserva (un solo viaje de red, atómico por diseño de Redis):

```lua
-- internal/repository/redisrepo/scripts/reserve.lua
-- KEYS[1] = deal:{<id>}:stock   KEYS[2] = deal:{<id>}:buyers
-- ARGV[1] = user_id   ARGV[2] = qty   ARGV[3] = per_user_limit
local stock = redis.call('GET', KEYS[1])
if not stock then
  return -2 -- deal not loaded or not active
end
stock = tonumber(stock)
local qty = tonumber(ARGV[2])
local bought = tonumber(redis.call('HGET', KEYS[2], ARGV[1]) or '0')
if bought + qty > tonumber(ARGV[3]) then
  return -3 -- per-user limit reached
end
if stock < qty then
  return -1 -- sold out
end
redis.call('DECRBY', KEYS[1], qty)
redis.call('HINCRBY', KEYS[2], ARGV[1], qty)
return stock - qty
```

El hash tag `{<id>}` en las claves asegura que ambas caen en el mismo slot si mañana se usa Redis Cluster.

Caso de uso con compensación:

```go
// internal/usecase/reserve.go
func (uc *Reserve) Execute(ctx context.Context, in ReserveInput) (domain.Reservation, error) {
	deal, err := uc.deals.GetByID(ctx, in.DealID)
	if err != nil {
		return domain.Reservation{}, fmt.Errorf("load deal: %w", err)
	}
	now := uc.clock.Now()
	if err := deal.CanReserve(now, in.Quantity); err != nil {
		return domain.Reservation{}, err
	}
	if err := uc.gate.TryReserve(ctx, deal.ID, in.UserID, in.Quantity, deal.PerUserLimit); err != nil {
		return domain.Reservation{}, fmt.Errorf("stock gate: %w", err)
	}

	res := domain.NewReservation(uc.ids.New(), deal.ID, in.UserID, in.Quantity, in.IdempotencyKey, now.Add(uc.ttl))
	err = uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		return uc.reservations.Create(ctx, res)
	})
	if err != nil {
		uc.compensate(ctx, deal.ID, in)
		return domain.Reservation{}, fmt.Errorf("persist reservation: %w", err)
	}
	return res, nil
}

func (uc *Reserve) compensate(ctx context.Context, dealID int64, in ReserveInput) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := uc.gate.Release(ctx, dealID, in.UserID, in.Quantity); err != nil {
		uc.log.ErrorContext(ctx, "stock compensation failed, reconciler will fix it",
			slog.Int64("deal_id", dealID), slog.Any("error", err))
	}
}
```

Expiración segura con varias réplicas:

```sql
WITH due AS (
  SELECT id FROM reservations
  WHERE status = 'pending' AND expires_at < now()
  ORDER BY expires_at
  LIMIT $1
  FOR UPDATE SKIP LOCKED
)
UPDATE reservations r SET status = 'expired', updated_at = now()
FROM due WHERE r.id = due.id
RETURNING r.deal_id, r.user_id, r.quantity;
```

En la misma transacción se descuenta `reserved` en el shard de cada reserva (agrupado y en orden `(deal_id, shard)` para no provocar deadlocks); tras el commit se libera el stock en Redis con `release.lua`.

Test que demuestra cero sobreventa (el más importante del repositorio):

```go
//go:build integration

func TestStockGate_NoOversell(t *testing.T) {
	ctx := t.Context()
	gate := newTestGate(t) // real Redis via testcontainers-go
	const stock, buyers = 100, 1000
	require.NoError(t, gate.Load(ctx, dealID, stock))

	var ok, soldOut atomic.Int64
	var wg sync.WaitGroup
	for i := range buyers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := gate.TryReserve(ctx, dealID, fmt.Sprintf("user-%d", i), 1, 1)
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, domain.ErrSoldOut):
				soldOut.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, stock, ok.Load())
	require.EqualValues(t, buyers-stock, soldOut.Load())
}
```

Reglas:

- Toda goroutine tiene dueño, una salida por `ctx.Done()` y alguien que la espera (`errgroup` o `WaitGroup`).
- Nunca usar `r.Context()` en una goroutine que sobrevive a la request.
- Concurrencia acotada: `errgroup.SetLimit(n)`, jamás un `go` por elemento sin límite.
- Solo quien envía cierra un canal.
- `go test -race ./...` es obligatorio en CI.

### 5.3 Inyección de dependencias manual

`main.go` construye todo de forma explícita, de afuera hacia adentro: config → infraestructura → repositorios → casos de uso → delivery → servidor y workers. La función `run` devuelve `error`, lo que hace el arranque testeable y evita `log.Fatal` disperso.

```go
// cmd/api/main.go
package main

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Getenv); err != nil {
		fmt.Fprintf(os.Stderr, "flashdeal: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	log := logger.New(cfg.LogLevel)

	// Infrastructure
	pool, err := postgres.NewPool(ctx, cfg.Postgres)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pool.Close()

	rdb, err := redisrepo.NewClient(ctx, cfg.Redis)
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	defer rdb.Close()

	metrics := observability.NewMetrics()

	// Repositories (adapters)
	txm := postgres.NewTxManager(pool)
	dealRepo := postgres.NewDealRepo(pool)
	resRepo := postgres.NewReservationRepo(pool)
	orderRepo := postgres.NewOrderRepo(pool)
	gate := redisrepo.NewStockGate(rdb)
	dealCache := redisrepo.NewDealCache(rdb, dealRepo, cfg.DealCacheTTL)
	clk := clock.System{}
	ids := uuidgen.V7{}

	// Use cases
	reserve := usecase.NewReserve(dealCache, gate, resRepo, txm, clk, ids, cfg.ReservationTTL, log)
	confirm := usecase.NewConfirm(resRepo, orderRepo, txm, clk)
	expire := usecase.NewExpire(resRepo, dealRepo, gate, txm)

	// Delivery
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewRouter(httpapi.Deps{Reserve: reserve, Confirm: confirm, Log: log, Metrics: metrics}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Lifecycle
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		log.Info("http server listening", slog.String("addr", cfg.HTTPAddr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error { return worker.NewExpirer(expire, cfg.ExpireEvery, log, metrics).Run(gctx) })
	g.Go(func() error {
		<-gctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	})
	return g.Wait()
}
```

Reglas:

- Constructores `NewX(deps...) *X` con dependencias explícitas; nada de service locator ni contenedor global.
- La configuración se lee una vez y se pasa como struct; ningún paquete llama a `os.Getenv` por su cuenta.
- Si un caso de uso necesita más de \~6 dependencias, está haciendo demasiado: dividirlo.
- `uuidgen.V7` es un adaptador mínimo sobre la librería de UUID elegida; el dominio solo conoce la interfaz `IDGenerator`.

## 6. Qué hacer y qué no hacer

Cada fila es una regla verificable en code review; si Claude Code genera algo de la columna "No hacer", se rechaza el cambio.

### Buenas prácticas (sí hacer)

| Práctica | Por qué |
| --- | --- |
| `context.Context` como primer parámetro en toda operación de I/O | Cancelación, timeouts y trazas se propagan solos |
| Interfaces pequeñas declaradas en el consumidor | Desacoplan capas y hacen triviales los fakes |
| Aceptar interfaces, devolver structs | El llamador decide la abstracción; el tipo concreto conserva su API completa |
| Structs de fila (`rows.go`) separados de entidades de dominio | El esquema de la base puede cambiar sin tocar el negocio |
| Dinero en `int64` de centavos + moneda | Sin errores de redondeo de `float64` |
| Restricciones en la base (`CHECK`, `UNIQUE`, `FK`) | La correctitud no depende de que el código sea perfecto |
| Timeouts explícitos en `http.Server`, pool de pgx y cliente Redis | Un dependiente lento no tumba el servicio |
| Tests table-driven y `t.Parallel()` donde aplique | Casos legibles, suites rápidas |
| Inyectar `Clock` e `IDGenerator` | Tests deterministas de expiración y TTL |
| Logs estructurados con `request_id`, `deal_id`, `user_id` | Depurar un incidente en minutos, no en horas |
| Métricas de negocio (`reservations_total{result}`, `stock_remaining`) además de técnicas | Ver una oferta agotándose en tiempo real |
| Graceful shutdown con `srv.Shutdown` y workers atados al mismo `ctx` | Despliegues sin requests cortadas |
| `/healthz` (vivo) separado de `/readyz` (dependencias listas) | El orquestador distingue reiniciar de sacar del balanceador |
| ADR por cada decisión no obvia | El reviewer entiende el porqué sin preguntarte |

### Anti-patterns (no hacer)

| Anti-pattern | Problema | Alternativa |
| --- | --- | --- |
| Variables globales mutables (`var DB *pgxpool.Pool`) | Estado oculto, tests acoplados, data races | Inyección por constructor |
| `panic` para errores de negocio o de I/O | Tumba la goroutine y oculta el flujo de error | Devolver `error` tipado |
| `log.Fatal` fuera de `main` | Salta los `defer` y el apagado ordenado | Devolver el error hasta `run` |
| Entidades de dominio con tags `db` o `json` | Acopla el negocio a la base y al contrato HTTP | DTO en delivery, structs de fila en repository |
| Lógica de negocio en handlers o en SQL disperso | Reglas imposibles de testear sin HTTP o base | Reglas en `domain`, orquestación en `usecase` |
| Leer stock, calcular en Go y escribir (`SELECT` + `UPDATE`) | Race condition clásica: sobreventa | Operación atómica (Lua, `UPDATE ... WHERE` condicional) |
| `sync.Mutex` para coordinar stock entre réplicas | Solo protege un proceso | Atomicidad en Redis / PostgreSQL |
| Goroutines sin dueño (`go doSomething()` suelto) | Fugas de goroutines y trabajo perdido al apagar | `errgroup` con `ctx` |
| Usar `r.Context()` en trabajo asíncrono | Se cancela cuando responde el handler | `context.WithoutCancel` + timeout propio |
| Interfaces gigantes "por si acaso" o declaradas junto a la implementación | Abstracción prematura, fakes enormes | Interfaces de 1 a 3 métodos donde se usan |
| Paquetes `utils`, `common`, `models` compartidos | Imanes de dependencias cíclicas | Código en el paquete que lo necesita |
| Ignorar errores (`_ = rows.Close()`, `defer tx.Rollback()` sin revisar en el commit) | Fallos silenciosos | Revisar todo error; `Rollback` tras `Commit` exitoso es inofensivo y se ignora a propósito |
| ORM con automigrate | Esquema no versionado, SQL invisible | Migraciones SQL + pgx |
| Mocks de la base de datos en tests de repositorio | Prueban el mock, no el SQL | testcontainers-go con Postgres y Redis reales |
| Devolver `err.Error()` crudo al cliente | Filtra detalles internos | Código + mensaje de dominio; detalle al log |

## 7. Plan de implementación por fases

Nueve fases, cada una entregable y demostrable por sí sola; la fase 3 construye a propósito una versión solo con PostgreSQL para medirla contra la versión con Redis de la fase 4 y contar esa mejora con números reales.

Cómo pedírselo a Claude Code: "Lee `docs/ARCHITECTURE.md` y `CLAUDE.md`. Implementa solo la fase N. Antes de escribir código, muéstrame el plan de archivos. Termina con `make lint test` en verde."

1. **Fase 0 — Bootstrap del repositorio**
   - `go mod init`, árbol de carpetas de la sección 4, `Makefile` (`up`, `down`, `migrate`, `test`, `test-integration`, `lint`, `load`).
   - `docker-compose.yml` con PostgreSQL y Redis (con healthchecks), `Dockerfile` multi-stage a distroless con usuario no root.
   - `config`, `logger` (slog JSON), servidor con `/healthz` y `/readyz`, graceful shutdown.
   - `.golangci.yml` y workflow de CI: lint, `go test -race`, `govulncheck`, build de imagen.
   - Listo cuando: `make up` levanta todo y `curl localhost:8080/readyz` responde `200`; CI en verde.
2. **Fase 1 — Dominio**
   - Entidades `Deal`, `Reservation`, `Order`, value object `Money`, errores de dominio.
   - Invariantes: ventana válida, stock > 0, transiciones `pending → confirmed | expired | cancelled` (cualquier otra devuelve error).
   - Listo cuando: tests table-driven cubren cada invariante y transición; cobertura de `domain` ≥ 90 %.
3. **Fase 2 — Persistencia en PostgreSQL**
   - Migraciones del anexo, `pgxpool`, `TxManager` (la transacción viaja en el `context`), repositorios y mapeo fila ↔ dominio.
   - Traducción de errores: `ErrNoRows` → `NotFound`, `23505` → `Conflict`, `violación de deal_stock_shards_not_exceeded → SoldOut (por nombre de constraint)`.
   - Listo cuando: tests de integración con testcontainers-go pasan con `make test-integration`.
4. **Fase 3 — Casos de uso y API HTTP (línea base solo PostgreSQL)**
   - Casos de uso `ManageDeals`, `GetDeal`, `Reserve`, `Confirm`, `Cancel`, con fakes en tests.
   - Router chi, DTOs, validación, mapeo de errores, middlewares (request ID, logging, recover, timeout, auth por header).
   - `StockGate` implementado temporalmente sobre PostgreSQL (`UPDATE` condicional), `api/openapi.yaml`.
   - Listo cuando: el flujo reservar → confirmar funciona con `curl`; tests de handlers con `httptest`; primer script k6 corre y sus resultados quedan guardados como línea base.
5. **Fase 4 — Compuerta de concurrencia con Redis**
   - `StockGate` con `reserve.lua` / `release.lua` embebidos, carga de stock al activar una oferta (`SET NX`).
   - Límite por usuario, compensación, middleware de idempotencia (respuesta cacheada en Redis 24 h + `UNIQUE` en base).
   - Caché de oferta con `singleflight`.
   - Listo cuando: `TestStockGate_NoOversell` pasa con `-race`; un test e2e reintenta la misma `Idempotency-Key` y obtiene una sola reserva.
6. **Fase 5 — Expiración y reconciliación**
   - Worker `Expirer` con `FOR UPDATE SKIP LOCKED` en lotes; worker `Reconciler` que compara contadores y corrige Redis desde PostgreSQL.
   - Ambos dentro del `errgroup` de `main`.
   - Listo cuando: un test con `Clock` falso verifica que una reserva vencida devuelve el stock; dos instancias del worker no procesan la misma fila.
7. **Fase 6 — Observabilidad**
   - Métricas Prometheus (latencia por ruta, `reservations_total{result}`, `stock_remaining`, `expired_total`), trazas OpenTelemetry hacia Jaeger, `pprof` en un puerto de administración.
   - Dashboard de Grafana versionado en `deploy/grafana`.
   - Listo cuando: durante una prueba k6 se ve en Grafana cómo el stock baja a 0 y la tasa de `SOLD_OUT` sube.
8. **Fase 7 — Rendimiento y resiliencia**
   - Escenario k6 de apertura de oferta (rampa a miles de usuarios virtuales en segundos) con umbrales que fallan si p99 o la tasa de errores `5xx` superan lo definido.
   - Comparación línea base (fase 3) vs compuerta Redis (fase 4): RPS, p50/p95/p99, unidades vendidas.
   - Prueba de caos: detener Redis durante la carga y verificar `503` y cero sobreventa; perfilar con `pprof` y documentar un cuello de botella resuelto.
   - Listo cuando: los resultados reales están en el README con gráficos o capturas.
9. **Fase 8 — Pulido de portafolio**
   - README: problema, diagrama, cómo correrlo en un comando, resultados de carga, decisiones (enlaces a ADRs), limitaciones conocidas y próximos pasos.
   - Sección "How I Used AI": qué delegaste a Claude Code, qué revisaste y qué corregiste.
   - Listo cuando: alguien que no te conoce clona el repo y ve la demo funcionando en menos de 5 minutos.

Extensiones opcionales si sobra tiempo, en orden de valor: patrón Outbox para publicar `order.confirmed`, rate limiting por usuario con token bucket en Redis, despliegue en Kubernetes con HPA.

## Anexo. Contrato de API, modelo de datos y criterios de aceptación

### Endpoints (v1)

| Método | Ruta | Descripción | Respuestas |
| --- | --- | --- | --- |
| `POST` | `/v1/deals` | Crear oferta (JWT con `role=admin`) | `201`, `400`, `401`, `403` |
| `POST` | `/v1/deals/{id}/activate` | Cargar stock en Redis y abrir la oferta (JWT con `role=admin`) | `200`, `401`, `403`, `404`, `409` |
| `GET` | `/v1/deals/{id}` | Consultar oferta y stock aproximado | `200`, `404` |
| `GET` | `/v1/deals?status=active` | Listar ofertas activas (paginado por cursor) | `200` |
| `POST` | `/v1/deals/{id}/reservations` | Reservar; requiere `Authorization: Bearer <JWT>` e `Idempotency-Key` | `201`, `400`, `401`, `404`, `409`, `503` |
| `POST` | `/v1/reservations/{id}/confirm` | Confirmar y crear la orden (JWT) | `200`, `401`, `404`, `409`, `410` |
| `DELETE` | `/v1/reservations/{id}` | Cancelar y devolver stock (JWT) | `204`, `401`, `404`, `409` |
| `GET` | `/healthz`, `/readyz` | Liveness y readiness | `200`, `503` |
| `GET` | `/metrics` | Métricas Prometheus | `200` |

Formato de error único:

```json
{
  "code": "SOLD_OUT",
  "message": "deal sold out",
  "request_id": "01J9Z8..."
}
```

### Modelo de datos

```sql
CREATE TABLE deals (
  id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  product_id     TEXT        NOT NULL,
  title          TEXT        NOT NULL,
  price_cents    BIGINT      NOT NULL CHECK (price_cents > 0),
  currency       CHAR(3)     NOT NULL,
  total_stock    INT         NOT NULL CHECK (total_stock > 0),
  per_user_limit INT         NOT NULL DEFAULT 1 CHECK (per_user_limit > 0),
  starts_at      TIMESTAMPTZ NOT NULL,
  ends_at        TIMESTAMPTZ NOT NULL,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT deals_window_valid CHECK (ends_at > starts_at)
);

-- ADR 0011: n = max(1, min(STOCK_SHARDS, total_stock / per_user_limit))
-- filas por oferta; las capacidades suman total_stock.
CREATE TABLE deal_stock_shards (
  deal_id  BIGINT   NOT NULL REFERENCES deals(id),
  shard    SMALLINT NOT NULL CHECK (shard >= 0),
  capacity INT      NOT NULL CHECK (capacity > 0),
  reserved INT      NOT NULL DEFAULT 0 CHECK (reserved >= 0),
  sold     INT      NOT NULL DEFAULT 0 CHECK (sold >= 0),
  PRIMARY KEY (deal_id, shard),
  CONSTRAINT deal_stock_shards_not_exceeded CHECK (reserved + sold <= capacity)
);

CREATE TABLE reservations (
  id              UUID        PRIMARY KEY,
  deal_id         BIGINT      NOT NULL REFERENCES deals(id),
  user_id         TEXT        NOT NULL,
  quantity        INT         NOT NULL CHECK (quantity > 0),
  status          TEXT        NOT NULL CHECK (status IN ('pending', 'confirmed', 'expired', 'cancelled')),
  idempotency_key TEXT        NOT NULL,
  stock_shard     SMALLINT    NOT NULL DEFAULT 0,
  expires_at      TIMESTAMPTZ NOT NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT reservations_idempotent UNIQUE (user_id, idempotency_key)
);

CREATE INDEX reservations_pending_expiry_idx
  ON reservations (expires_at) WHERE status = 'pending';
CREATE INDEX reservations_deal_user_idx ON reservations (deal_id, user_id);

CREATE TABLE orders (
  id             UUID        PRIMARY KEY,
  reservation_id UUID        NOT NULL UNIQUE REFERENCES reservations(id),
  deal_id        BIGINT      NOT NULL REFERENCES deals(id),
  user_id        TEXT        NOT NULL,
  quantity       INT         NOT NULL,
  total_cents    BIGINT      NOT NULL,
  currency       CHAR(3)     NOT NULL,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

El repositorio traduce las violaciones por nombre de constraint, no solo por código SQLSTATE: `deal_stock_shards_not_exceeded` → `ErrSoldOut`, `reservations_idempotent` → devolver la reserva existente.

Claves de Redis:

| Clave | Tipo | Contenido | TTL |
| --- | --- | --- | --- |
| `deal:{id}:stock` | String | Unidades disponibles | Hasta `ends_at` + margen |
| `deal:{id}:buyers` | Hash | `user_id` → unidades reservadas | Igual que el stock |
| `deal:{id}:cache` | String (JSON) | Snapshot de la oferta para lecturas | 1 a 5 s |
| `idem:{user_id}:{key}` | String (JSON) | Respuesta original de la request | 24 h |

### Criterios de aceptación globales

- [ ] Con stock N y M > N compradores concurrentes, se venden exactamente N unidades (test de integración y prueba k6).
- [ ] Reintentar una reserva con la misma `Idempotency-Key` devuelve la misma reserva y no consume stock extra.
- [ ] Un usuario no supera `per_user_limit` aunque envíe requests en paralelo.
- [ ] Una reserva no confirmada vuelve al stock tras su TTL, con varias réplicas corriendo.
- [ ] Con Redis caído, la reserva responde `503` y no se vende ninguna unidad.
- [ ] `SIGTERM` durante carga: las requests en vuelo terminan y el proceso sale con código 0.
- [ ] `go test -race ./...`, `golangci-lint` y `govulncheck` en verde en CI.
- [ ] README con resultados reales de carga y comparación línea base vs compuerta Redis.
