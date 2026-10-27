# 0008. JWT authentication and role-based authorization

- Status: accepted
- Date: 2026-10-27

## Context

Callers were identified with a free-form `X-User-ID` header, and admin
routes (`POST /v1/deals`, activate) were open. Any client could act as
any buyer or create deals, so the per-user limit and admin operations
were not actually protected. The auth middleware was already the seam
for a real check.

Alternatives considered:

- **Keep `X-User-ID` and add an admin API key.** Simple, but buyers
  could still impersonate each other and roles would not be carried in a
  verifiable credential.
- **Full OAuth 2.0 / OpenID Connect against an IdP.** Correct for
  production at scale, but it would force every local run and load test
  through an external issuer.
- **Asymmetrically signed JWTs (RS256).** Better when many services
  verify tokens issued by one IdP. Here the API both issues (dev tooling)
  and verifies with one shared secret, so HS256 is enough.

## Decision

- Every authenticated route requires
  `Authorization: Bearer <JWT>` (HS256).
- Claims: `sub` (user id), `role` (`user` or `admin`), `iss`, `iat`,
  `exp`. Algorithm is pinned to HS256; tokens with another `alg` are
  rejected.
- Reservation, confirm and cancel require any valid token (`sub` is the
  caller). Create and activate deals require `role=admin`.
- Public reads (`GET /v1/deals…`), health and metrics stay unauthenticated.
- Local Compose and tests share a documented HMAC secret. Tokens for demos
  and load tests are minted with `make token` / `cmd/token` (same library
  the API uses to verify). There is no public token-issuing HTTP endpoint:
  in production an IdP (OAuth 2.0 / OIDC) would issue the JWTs; this
  service only validates them.

Dependency: `github.com/golang-jwt/jwt/v5` (explicitly allowed by this
ADR; not previously on the default allow-list in `CLAUDE.md`).

## Consequences

- Spoofing another user requires forging a signed token.
- Admin actions are no longer open on a public port.
- k6, e2e and chaos tests mint JWTs with the shared secret.
- Migrating to an external OIDC issuer later means swapping the verifier
  (JWKS / RS256) behind `platform/auth` without changing handlers.
