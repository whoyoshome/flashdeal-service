# 0011. Split each deal's stock over shard rows

- Status: accepted
- Date: 2026-10-27

## Context

After ADR 0006 every successful reservation still updated the same row,
`deals`, so concurrent sales of one deal queued on that row's lock for one
round trip each. Phase 7 left the successful reservation p99 at 86 ms in
scenario B (1,000 units at 5,000 attempts per second) and the 2-replica
kind run at 72 ms, against a 50 ms target.

ADR 0006 named stock shards as the next step. The alternatives were the
ones it already rejected: backpressure does not raise throughput, and
server-wide relaxed durability is not the service's to change.

## Decision

- A new table `deal_stock_shards (deal_id, shard, capacity, reserved, sold)`
  holds the counters. `CHECK (reserved + sold <= capacity)`
  (`deal_stock_shards_not_exceeded`) replaces `deals_stock_not_exceeded`,
  and `deals.reserved` and `deals.sold` are dropped. Migration 000004
  moves existing counters to shard 0.
- Creating a deal inserts `n` shards in the same statement, with
  `n = max(1, min(STOCK_SHARDS, total_stock / per_user_limit))`. Capacities
  differ by at most one unit. Capping `n` keeps every shard at least
  `per_user_limit` units, so a buyer's largest allowed request fits in an
  empty shard. `STOCK_SHARDS` defaults to 32 (1 to 256); see the update
  below.
- A reservation takes one shard with room, chosen with
  `FOR NO KEY UPDATE SKIP LOCKED`, so concurrent sales spread over shards
  instead of queuing on one row. If every shard with room is locked, it
  waits on one. If none has room, it checks whether any shard does. That
  covers the race where the chosen shard filled while it waited. After
  three attempts it returns `SOLD_OUT`. The chosen shard is stored on the
  reservation (`reservations.stock_shard`).
- PostgreSQL keeps a row lock taken by `FOR NO KEY UPDATE` even when the
  re-check then rejects the row. A failed attempt could therefore keep a
  shard locked while the next attempt waits for another shard, and two
  reservations could deadlock. Each attempt runs after
  `SAVEPOINT reserve_shard`, and each retry first runs
  `ROLLBACK TO SAVEPOINT reserve_shard`, which releases those locks. Both
  statements are sent in one pgx batch with the attempt, so the common path
  still takes one round trip. A reservation now only waits while holding
  shards locked earlier in the same statement, which locks in shard order,
  so the waits cannot form a cycle.
- Confirmation moves the units on the reservation's own shard. Cancellation
  and expiry release units on their shards, grouped and applied in
  `(deal_id, shard)` order so two expiry batches cannot deadlock.
- Reads (`GET /v1/deals`, the gate reconciler) sum the shards.
- The Redis gate is unchanged: it still sees one counter per deal.

## Consequences

- **Correctness is unchanged.** Each shard has its own `CHECK`, the
  conditional `UPDATE` has no read-modify-write, and the capacities add up
  to `total_stock`. The no-oversell integration test asserts every shard
  ends full.
- **Latency.** Scenario B with a warm API on the 4-CPU development VM
  ([stock-shards.md](../../test/load/results/stock-shards.md)):
  - 1 shard: successful reservation p99 16–20 ms.
  - 8 shards: 7–11 ms.
  - 2 replicas on kind (1,000 attempts per second): 15–17 ms.
- **Cold start is the rest of the old tail.** The first sale after the API
  starts paid for opening pool connections: p99 80–90 ms. Compose and the
  Kubernetes ConfigMap now set `POSTGRES_MIN_CONNS` equal to
  `POSTGRES_MAX_CONNS` (20). With that setting the first sale measured
  44–51 ms.
- **Scenario A is still hardware-bound.** With 10,000 units at 5,000
  attempts per second, the p99 drops from 872 ms to 519–659 ms. About 88 %
  of pool acquires wait on the 20-connection pool. A 50-connection pool
  was worse on this VM (3.6 % timeouts). That ceiling is CPU shared with
  k6, not one row's lock.
- **Fragmentation: accepted.** Near the end of a sale, the remaining units
  can be spread over shards so that none of them fits a multi-unit
  request. That request gets `SOLD_OUT` while `GET /v1/deals` still shows
  units left. Those units still sell to 1-unit buyers. Moving units
  between shards would add a multi-row write to the hot path. This only
  matters when `per_user_limit > 1`.
- **Schema cost.** Every query that read `deals.reserved` or `deals.sold`
  now joins the shards. The down migration rebuilds the columns from the
  shard sums.

## Update: 32 shards by default

Profiling scenario A ([pg-profile.md](../../test/load/results/pg-profile.md))
found that with 8 shards, 65–82 % of PostgreSQL's active session time was
spent waiting on shard row locks. Up to 20 pool connections compete for 8
rows, so `SKIP LOCKED` often found every shard taken and fell back to
waiting. With 32 shards, more than the pool size, those waits fell to
19–32 %. Scenario A's p99 dropped from 258–500 ms to 111–181 ms, and
scenario B's stayed at 5.6–15.4 ms. The default is now 32.

Smaller shards make the accepted fragmentation case a little more likely
for multi-unit requests. A shard is still never smaller than
`per_user_limit`.
