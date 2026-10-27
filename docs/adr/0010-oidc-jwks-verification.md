# 0010. Optional OIDC JWKS (RS256) verification

- Status: accepted
- Date: 2026-10-27

## Context

ADR 0008 introduced HS256 with a shared secret so local Compose, e2e and
k6 stay self-contained. Production at scale issues tokens from an OIDC
IdP and publishes public keys at a JWKS URL; the API should verify those
without changing handlers.

## Decision

- `JWT_JWKS_URL`, when set, selects `auth.JWKSVerifier` (RS256, keys
  fetched and cached from the URL). `JWT_SECRET` is then unused for
  verification.
- When `JWT_JWKS_URL` is empty, behaviour is unchanged: HS256 via
  `auth.Service` and a required `JWT_SECRET`.
- Claims stay `sub`, `role`, `iss`, `iat`, `exp`. Algorithm is pinned to
  RS256 in JWKS mode (HS256 tokens are rejected).
- The HTTP layer depends only on `middleware.Authenticator`, so switching
  verifiers is a composition-root change in `cmd/api`.
- Local minting (`make token`) remains HS256; it is not an OIDC IdP.

## Consequences

- Connecting a real IdP is configuration (`JWT_JWKS_URL`, `JWT_ISSUER`),
  not a handler rewrite.
- Tests cover accept/reject against an in-process JWKS HTTP server with a
  generated RSA key; no external IdP is required in CI.
- Key rotation is handled by the cache TTL (default 5 minutes) and by
  re-fetching on unknown `kid`.
