# 0013. Reserve in a single statement when a shard is free

- Status: accepted
- Date: 2026-10-27

## Context

With 32 stock shards (ADR 0011), scenario A (10,000 units at 5,000
attempts per second) still had a reservation p99 of 111–181 ms, against a
50 ms target ([pg-profile.md](../../test/load/results/pg-profile.md)).
PostgreSQL sessions spent most of their time on CPU and the API waited for
pool connections. Each reservation held a connection for five round trips:
`BEGIN`, `SET LOCAL synchronous_commit = off`, the reservation `INSERT`,
the `SAVEPOINT` plus shard `UPDATE` batch, and `COMMIT`. The shard row
stayed locked from its `UPDATE` until the `COMMIT` round trip came back.

The transactional path must stay. It tells apart the cases a reservation
can end in: an idempotent retry, a key reused for another request, an
unknown deal, a sold-out deal, and every shard with room being held by
another reservation, in which case it waits for one.

## Decision

`ReservationRepo.Create` first runs one statement outside any explicit
transaction, so PostgreSQL commits it as its own implicit transaction:

- A CTE takes the units from a free shard (`FOR NO KEY UPDATE SKIP LOCKED`,
  with the room re-checked after the lock), and the `INSERT` stores the
  reservation with that shard. Both happen or neither does.
- `set_config('synchronous_commit', 'off', true)` in the same statement
  keeps the asynchronous commit of ADR 0006, scoped to that transaction.
- It runs detached from the caller's cancellation, with its own 2 s
  deadline, like `COMMIT` in `TxManager`. Otherwise a cancelled request
  could hear a failure for a sale that committed, and give its units back
  to the Redis gate.

Create falls back to the transactional path, unchanged, when the
statement stores nothing (no free shard with room, or an unknown deal) or
violates `reservations_idempotent` (a retry). It also takes that path when
the context already carries a transaction, where a failed statement would
abort the caller's transaction.

## Consequences

- **One round trip for most sales.** The Redis gate turns away most
  requests that would find the deal sold out, so the statement usually
  succeeds. The shard row is locked only while the statement runs, with no
  network round trip before `COMMIT`.
- **Measured** on the 4-CPU development VM (Compose, 1 replica):
  - Scenario A's reservation p99 fell to 43.7–114.9 ms, from 111–181 ms,
    and its p50 to 1.4–5.8 ms, from 13–40 ms.
  - Dropped iterations fell to 0–188, from 570–934.
  - Scenario B's p99 is 5.6–8.2 ms.
- **The fallback costs one extra round trip.** A retry, or a reservation
  that finds every shard with room locked, runs the single statement and
  then the five round trips. A retry that races its original also leaves a
  unique-violation error in the PostgreSQL log.
- **Two paths to keep in sync.** The single statement repeats the shard
  selection of `reserveOnFreeShard`. An integration test pins the statement
  sequence of each path and the asynchronous commit of both.
