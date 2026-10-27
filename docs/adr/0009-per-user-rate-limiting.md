# 0009. Per-user rate limiting with a Redis token bucket

- Status: accepted
- Date: 2026-10-27

## Context

Authenticated routes were unbounded per caller. Stock correctness still
held (Lua gate + PostgreSQL), but a single buyer (or a stolen token)
could hammer reserve/confirm/cancel and waste capacity that should serve
other buyers during a flash sale.

Alternatives considered:

- **In-process token bucket.** Wrong under N replicas: each pod would
  admit its own burst.
- **API gateway / mesh rate limit.** Correct in production at scale, but
  this service must stay self-contained for local Compose, e2e and load
  tests.
- **Fixed window counter.** Simpler, but thundering herds at window
  edges; a token bucket spreads refill evenly.

## Decision

- After JWT auth, spend one Redis token per request for the caller's
  `sub`, via an atomic Lua token bucket (`ratelimit:{sub}`).
- Defaults: 50 tokens/s refill, burst 100 (`RATE_LIMIT_RPS`,
  `RATE_LIMIT_BURST`). `RATE_LIMIT_RPS=0` disables the limiter.
- Over budget → `429 RATE_LIMITED` with `Retry-After` and
  `X-RateLimit-*` headers.
- Redis unavailable → fail closed as `503 STOCK_UNAVAILABLE` (same as
  the stock gate: without Redis the service must not admit unlimited
  authenticated traffic).
- Public reads, health and metrics are not rate-limited.

## Consequences

- Abuse and accidental retry storms are bounded per subject across all
  replicas.
- Load tests that mint a unique `sub` per buyer are unaffected; a
  single-subject burst larger than `RATE_LIMIT_BURST` will see 429s.
- Migrating the limiter to an edge gateway later means dropping the
  middleware; handlers stay unchanged.
