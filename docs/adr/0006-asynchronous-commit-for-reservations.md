# 0006. Commit reservation transactions asynchronously

- Status: accepted
- Date: 2026-10-27

## Context

Every successful reservation increments `deals.reserved` with a conditional
`UPDATE` on the deal's single row. The row stays locked from that `UPDATE`
until `COMMIT`, and by default `COMMIT` waits for the WAL to be flushed to
disk. Each sale of a deal therefore waited for the previous sale's fsync.

Measured under load in phase 7 (see
[write-path.md](../../test/load/results/write-path.md)):

- While reservations were written, sampling `pg_stat_activity` found active
  sessions in `Lock:transactionid` (waiting for the deal row) 965 times out
  of about 1,100. The session holding the lock was in `IO:WalSync` in most
  sampling rounds.
- A CPU profile of the API under the same load showed 18 % of one core,
  almost all of it network syscalls. The bottleneck was not in Go.
- Throughput was about 480 sales per second per deal. Scenario A (10,000
  units at 5,000 attempts per second) timed out 28 % of attempts, and the
  successful reservation p99 was 4.4 s, against a design target of 50 ms.

The alternatives considered:

- **Stock shards.** Split a deal's counters over K rows. This keeps
  durability, but changes the schema and every query that reads the
  counters.
- **Backpressure.** Bound the writes in flight and reject the excess with a
  fast 503. This removes the timeouts but does not raise throughput.
- **Server-wide `synchronous_commit = off` or `commit_delay`.** This would
  also weaken orders, and it is configuration the service does not own.

## Decision

The transaction that creates a reservation runs
`SET LOCAL synchronous_commit = off` as its first statement, before the deal
row is locked. Its `COMMIT` returns once the WAL record is written to the
operating system, without waiting for it to reach disk. The setting ends
with that transaction.

Confirmations (which create orders), cancellations and expiries keep the
default synchronous commit.

## Consequences

- **Throughput and latency.** The deal row is held for one round trip
  instead of one fsync:
  - Writes reach about 2,000 sales per second per deal (4.3×).
  - Scenario A has no timeouts.
  - Scenario B's successful reservation p99 drops from 1.04 s to 86 ms, and
    its p50 from 459 ms to under 1 ms.
- **Durability.** If **PostgreSQL** crashes, not the API, reservations it
  acknowledged in the last few hundred milliseconds (up to three times
  `wal_writer_delay`) can be lost. The buyer received a 201 for a hold that
  no longer exists:
  - **No overselling.** The reservation row and its `reserved` increment are
    in the same transaction, so they are lost together. The
    `deals_stock_not_exceeded` constraint and the conditional `UPDATE` are
    unaffected.
  - **The gate realigns.** The Redis gate still counts the lost holds, which
    understates the stock. The reconciler restores it, and rebuilds the
    buyers' holdings, once the drift is stable (ADR 0005).
  - **A replay can point to a lost reservation.** A retry with the same
    `Idempotency-Key` within 24 h replays the stored 201 for a reservation
    that no longer exists. Confirming it returns `404`, and the buyer
    reserves again with a new key.
  - **Orders are never affected.** A confirmed purchase stays durable.
- **Still short of the target.** On the 4-CPU development VM shared with k6
  and the observability stack, the reservation p99 still misses the 50 ms
  target: 86 ms in scenario B and about 1 s in scenario A. What remains is
  one round trip per sale on one row, plus CPU contention. Stock shards stay
  the next step if production numbers require it.

Update: stock shards were implemented in ADR 0011. The deal counters now
live in `deal_stock_shards`, and `deal_stock_shards_not_exceeded` replaces
`deals_stock_not_exceeded`. The asynchronous commit still applies.
