# 0015. Public clickable demo on a free cloud host

- Status: accepted
- Date: 2026-10-27

## Context

Recruiters open the repository from a CV and will not run Docker. The
project needed the same kind of “click the URL and try it” surface as a
typical SaaS portfolio deploy (Render + Neon + Upstash), without turning
the production API into an open registration system.

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
  `X-Forwarded-For` behind Render.
- **`MIGRATE_ON_START`.** A single Render web service has no migrate Job;
  the API applies embedded migrations before listen when this flag is set.
- **`REDIS_URL`.** Upstash (and similar) expose `rediss://…`. When set, it
  replaces `REDIS_ADDR` / password / DB.

## Consequences

- A recruiter can open the service URL, start a sale, reserve and confirm
  without installing anything. Cold starts on free tiers may take 30–60 s.
- Each session inserts a deal row; free Neon quotas are enough for light
  traffic. Abuse is bounded by the rate limiter and by deal size (5 units).
- Demo mode must stay off outside the portfolio deploy: it issues tokens
  without an IdP.
