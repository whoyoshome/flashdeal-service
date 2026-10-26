# 0005. Reconcile the stock gate only on stable drift, with compare-and-set

- Status: accepted
- Date: 2026-10-26

## Context

Stock lives in two stores. The Redis gate (`deal:{id}:stock` and the
per-buyer hash `deal:{id}:buyers`) admits buyers. PostgreSQL holds the
committed truth and its `CHECK` constraints make overselling impossible.
Cancellation, expiry and failed reservations commit in PostgreSQL first and
then release the gate. If that release is lost (Redis unreachable, the
process dies in between), the gate understates the stock. Units stay
unsellable, and the buyer still counts them against their per-user limit.

The architecture document asks for a periodic job that "compares the
counters and corrects Redis from PostgreSQL". Done naively, by setting the
gate to PostgreSQL's available units on every pass, it breaks things,
because the two stores also disagree legitimately while an operation is
half-way:

- A reservation decrements the gate, then commits in PostgreSQL up to
  `REQUEST_TIMEOUT` plus a detached `COMMIT` later.
- A cancellation or expiry commits, then releases the gate.

Overwriting the gate during that window would add a unit that the
in-flight reservation then commits (the gate overstates), or erase a buyer
the gate just admitted.

## Decision

`usecase.Reconcile` runs every `RECONCILE_EVERY` (default 30s) in each
replica and repairs a deal only when all of the following hold:

1. **The drift is stable.** The gate's stock differs from PostgreSQL's
   available units, and the gate stock, `reserved` and `sold` are all
   identical to what the previous pass saw.
2. **PostgreSQL has not moved since this pass listed the deal.** The
   per-buyer holdings are read in a single statement together with the
   available units, and that value must equal the one listed.
3. **The repair is a compare-and-set.** `restore.lua` rewrites the stock
   (`KEEPTTL`) and rebuilds the buyers hash from PostgreSQL's pending and
   confirmed reservations. It does so only if the gate still holds the
   stock this pass read.

Configuration validation enforces
`RECONCILE_EVERY > REQUEST_TIMEOUT + 5s`, so a pass outlasts the longest
operation: a request, its detached `COMMIT` (2s) and its gate compensation
(2s).

Why this is safe, given that bound:

- **An operation in flight at the first pass** finishes before the second
  pass. It changes one of the observed values, so the second pass waits.
- **A reservation starting after the second pass's read** moves the gate
  stock, so the compare-and-set refuses.
- **A cancellation or expiry committing after the deal was listed but
  before the holdings were read** fails condition 2.
- **One committing after the holdings were read** releases the gate on top
  of the restored state, which is exactly right.

The reconciler never loads a deal that is not in the gate (activation
does), and it only looks at active deals, since ended ones accept no
reservations.

## Alternatives considered

- **Set the gate from PostgreSQL every pass.** Simple, but it fights
  in-flight operations, as described in the context.
- **A version counter shared by both stores.** Redis and PostgreSQL cannot
  update it atomically together, so it moves the race instead of removing
  it.
- **Distributed locks around every reservation.** Rejected by the
  architecture: slower and more fragile than the Lua gate for a counter.

## Consequences

- **Repairs are slow.** Drift is fixed two to three passes after it
  appears (about 60–90s by default). A deal under constant load is never
  "stable", so it is repaired once the burst calms down. Until then the
  gate only understates the stock: units are lost temporarily, never
  oversold.
- **Residual risk, accepted.** The safety argument assumes no operation
  takes longer than a pass, for example through a process stall of tens of
  seconds between `COMMIT` and the gate release. If that happens, a repair
  can leave the gate overstating a deal by the stalled units until a later
  pass:
  - Overselling stays impossible: PostgreSQL's conditional `UPDATE` and
    `CHECK` reject the extra buyers.
  - The **per-user limit** is enforced only in the gate (`reserve.lua`).
    In that same corner case a buyer whose holdings were rebuilt without
    the stalled reservation could exceed the limit by that reservation's
    units. Enforcing the limit in PostgreSQL as well would close this, at
    the cost of a schema change and a per-user aggregate on the hot path.
    It is out of scope for now.
- **Buyer-only drift is not detected.** Drift in the buyers hash alone,
  with a correct stock, is not detected. Every script that touches the
  hash also moves the stock, so lost releases always show up in the stock.
- **Repairs are logged.** Each one is logged at `WARN` as
  `stock gate drift repaired` with the deal and both values. Phase 6
  turns it into a metric.
