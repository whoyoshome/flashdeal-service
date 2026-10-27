# 0015. Public clickable demo mode

- Status: accepted
- Date: 2026-10-27

## Context

The HTTP API is JWT-protected. A hosted instance still needs a way for
anyone opening the service URL to try reserve → confirm without minting
tokens by hand or running Docker.

## Decision

- **`DEMO_ENABLED=true`** serves an embedded HTML UI at `GET /` and
  `POST /v1/demo/session`. The session creates a **private** 5-unit deal,
  loads it into the Redis gate, and returns an HS256 JWT for a fresh
  `demo-*` user (30 minutes). The page then calls the real reserve and
  confirm routes.
- **HS256 only.** Minting tokens needs `JWT_SECRET`. Demo mode rejects
  `JWT_JWKS_URL`.
- **Rate limit.** Session creation uses the same Redis token bucket as
  authenticated routes, keyed by client IP (`demo:<ip>`), including
  `X-Forwarded-For` behind a reverse proxy.
- **`MIGRATE_ON_START`.** Useful when the process is the only place that
  can apply schema (no separate migrate Job).
- **`REDIS_URL`.** Managed Redis often exposes `rediss://…`. When set, it
  replaces `REDIS_ADDR` / password / DB.

## Consequences

- Opening the service root is enough to walk through a sale.
- Each session inserts a deal row; traffic is bounded by the rate limiter
  and by deal size (5 units).
- Leave `DEMO_ENABLED` off when tokens come from a real IdP.
