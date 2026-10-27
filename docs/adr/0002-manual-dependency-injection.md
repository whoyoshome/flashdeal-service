# 0002. Wire dependencies by hand in main

- Status: accepted
- Date: 2026-10-27

This record is retrospective. It writes down a decision made in the original
design (`docs/ARCHITECTURE.md`, sections 3 and 5.3) and followed since
phase 0.

## Context

The service has a handful of adapters (PostgreSQL repositories, the Redis
gate, cache and idempotency store), about ten use cases, an HTTP router and
three workers. Each layer depends only on the small interfaces the next one
inward declares. Something has to build the concrete graph once, at
startup, and own its lifecycle.

The options considered:

- **Google Wire.** Compile-time code generation. It adds a generator to the
  build and generated code to review, for a graph that fits on one screen.
- **Uber Fx or Dig.** Reflection-based containers with their own lifecycle
  hooks. Wiring errors surface at runtime, and the container decides start
  and stop order instead of plain code.
- **Manual wiring in `main`.** Explicit, no reflection, no generated code.

## Decision

`cmd/api/main.go` is the composition root and the only place that knows
every concrete type. It builds the graph from the outside in:

1. configuration;
2. telemetry (tracing and metrics);
3. infrastructure (PostgreSQL pool, Redis client);
4. adapters;
5. use cases;
6. the HTTP and admin servers;
7. the workers.

Everything then runs in one `errgroup`. `run` returns an error instead of
exiting, so every `defer` runs and startup is testable.

Rules that keep this sustainable:

- Constructors are `NewX(deps...) *X` with explicit dependencies. There is no
  service locator or global container.
- Configuration is loaded once in `platform/config` and passed as a struct.
- A use case that needs more than about six dependencies is doing too much
  and is split.

## Consequences

- **The whole graph reads top to bottom in one file.** It is about 200
  lines, and a wiring mistake is a compile error.
- **Tests build exactly what they need,** with hand-written fakes and no
  container.
- **Adding a dependency means editing `main` by hand.** That is acceptable
  at this size. A much larger codebase might justify Wire.
