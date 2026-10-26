# 0003. Idempotent reservation creation in PostgreSQL

- Status: accepted
- Date: 2026-10-26

## Context

Clients retry `POST /v1/deals/{id}/reservations` after timeouts. The
`reservations_idempotent UNIQUE (user_id, idempotency_key)` constraint stops a
retry from creating a second row, but a plain `INSERT` that violates it aborts
the transaction, so the repository could not read back the original
reservation, and the stock increment for the retry must not run at all.

A key reused for a *different* request (another deal or quantity) is a client
bug. Returning the original reservation would silently answer a request the
client did not make.

## Decision

`ReservationRepository.Create` runs, in one transaction (joining the caller's
if present):

1. `INSERT ... ON CONFLICT ON CONSTRAINT reservations_idempotent DO NOTHING`.
2. If no row was inserted, read the stored reservation by `(user_id,
   idempotency_key)`:
   - same deal and quantity: return it with `created = false` and reserve
     nothing;
   - otherwise: return `domain.ErrIdempotencyKeyReused` (409).
3. If the row was inserted, reserve stock with the conditional
   `UPDATE deals ... WHERE reserved + sold + qty <= total_stock`. Zero affected
   rows means `domain.ErrSoldOut` and the insert rolls back.

The port returns `created` explicitly so the use case can compensate the Redis
stock gate on a replay (phase 4) without comparing IDs.

## Consequences

- Concurrent retries with one key create exactly one reservation and reserve
  once (covered by `TestReservationRepoConcurrentRetries`).
- The HTTP idempotency middleware planned for phase 4 (cached response in
  Redis) stays the fast path; this is the durable backstop.
- Matching a replay on deal and quantity only is a deliberate simplification:
  those are the only request fields that change the outcome.
