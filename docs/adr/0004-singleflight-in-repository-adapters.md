# 0004. Allow golang.org/x/sync in repository adapters

- Status: accepted
- Date: 2026-10-26

## Context

The architecture document places the hot-read deal cache in
`repository/redisrepo/deal_cache.go` and requires "a short-TTL cache +
`singleflight`" so thousands of simultaneous reads of a deal opening collapse
into one load. The layer table in `CLAUDE.md`, enforced by depguard, only let
`usecase` import `golang.org/x/sync`, so the two documents contradicted each
other.

The alternative was a `singleflight` decorator in `usecase` wrapping a
Redis-only cache. It would keep the old rule but move caching mechanics into
the business layer.

## Decision

`internal/repository/*` may import `golang.org/x/sync`. The deal cache lives
entirely in `redisrepo`, as the architecture document describes. `CLAUDE.md`
and `.golangci.yml` are updated together.

## Consequences

- Caching stays an adapter concern; use cases keep depending on the small
  `DealReader` port and do not know whether reads are cached.
- `golang.org/x/sync` is effectively a standard-library extension with no
  transitive dependencies, so allowing it in adapters does not couple layers.
- The rest of the layer rules are unchanged.
