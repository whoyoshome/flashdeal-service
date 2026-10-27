# 0001. A Redis Lua script gates stock before PostgreSQL

- Status: accepted
- Date: 2026-10-27

This record is retrospective. It writes down a decision made in the original
design (`docs/ARCHITECTURE.md`, sections 2 and 3) and implemented in phase 4.

## Context

A flash sale puts a small stock in front of a crowd. With 1,000 units and
50,000 buyers, 98 % of requests must be turned away, and they all arrive in
the first seconds. Every unit is counted on one `deals` row. If every attempt
went to PostgreSQL, all of them would queue for that row's lock, rejections
included.

Phase 3 measured exactly that with PostgreSQL alone (scenario B): sold-out
rejections took 794 ms at p99, and 28 % of the offered load was never served.

The options considered:

- **PostgreSQL only.** Simplest, and correct thanks to the conditional
  `UPDATE` and `CHECK`, but the hot row serializes everything.
- **Distributed locks (Redlock) around a counter.** Several round trips per
  attempt, lease expiry to get right, and fragile under failover. All of
  that is heavier than the counter it protects.
- **An in-process counter.** It is wrong with more than one replica.
- **An atomic Lua script in Redis.** One round trip checks the stock and the
  buyer's per-user limit and decrements both. Redis runs scripts one at a
  time, so concurrent buyers on any number of replicas cannot interleave.

## Decision

Reservations pass a Redis gate first (`reserve.lua`, run with `EVALSHA`).
Only buyers it admits reach PostgreSQL, which still inserts the reservation
and increments `reserved` with a conditional `UPDATE`, guarded by
`CHECK (reserved + sold <= total_stock)`. PostgreSQL remains the source of
truth.

- Keys share the deal's hash tag (`deal:{id}:stock`, `deal:{id}:buyers`), so
  the script works on Redis Cluster.
- Activating a deal loads its stock with `SET NX`, so activating it again
  never resets a live counter.
- If the PostgreSQL write fails, the units are given back to the gate
  (`release.lua`), detached from the request's cancellation.
- If Redis is unreachable, reservations fail closed with `503`
  ([ADR 0007](0007-fail-fast-redis-client.md)).

## Consequences

- **Sold-out buyers never touch PostgreSQL.** In scenario B their p99 fell
  from 794 ms to 6.5 ms, and the service absorbed almost all of the offered
  load.
- **Two stores can drift.** A release lost after a commit leaves the gate
  understating the stock. The reconciler repairs it from PostgreSQL
  ([ADR 0005](0005-stock-gate-reconciliation.md)). Overselling stays
  impossible either way, because of the `CHECK`.
- **The per-user limit lives only in the gate.** PostgreSQL does not enforce
  it.
- **Redis is now on the critical path.** Its outage stops sales, by design,
  instead of selling without control.
