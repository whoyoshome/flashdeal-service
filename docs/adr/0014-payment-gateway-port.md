# 0014. Charge through a PaymentGateway port before confirming

- Status: accepted
- Date: 2026-10-27

## Context

Confirmation creates an order and assumes the buyer has paid. A real
flash sale would talk to a payment provider (authorize or capture) before
the order exists. Calling that provider inside the PostgreSQL transaction
would hold the reservation row lock for the whole round trip. Skipping a
port altogether leaves no place for a provider to plug in.

## Decision

- **Port.** `usecase.PaymentGateway` exposes one method,
  `Charge(ctx, PaymentCharge)`, with the reservation ID, buyer, amount in
  cents and currency. Confirm calls it **before** `WithinTx`.
- **Idempotency.** Implementations must be idempotent on
  `ReservationID`: a retry after a successful charge and a failed write
  must not bill twice.
- **Stub.** `repository/payment.AlwaysApprove` always succeeds and
  remembers charged reservation IDs in process memory. It is what
  `cmd/api` wires today.
- **Decline.** `domain.ErrPaymentDeclined` maps to HTTP 402. Confirm does
  not open a transaction when the charge is refused.
- **Compensation.** If the charge succeeds and the write transaction
  fails, a real provider would void or refund by reservation ID. The stub
  has nothing to undo; Confirm documents that gap in a comment.

Confirm still loads the reservation once without a lock to build the
charge, then locks it inside the transaction for the status transition.
A concurrent expiry between those steps leaves a charged reservation that
never becomes an order: the same compensation path as above.

## Consequences

- Interviewers and operators see where a card charge belongs, without
  taking a Stripe (or similar) dependency.
- Swapping in a real provider is an adapter behind the same port; the
  use case and HTTP contract stay.
- The stub does not survive process restarts: two API replicas can each
  charge once for the same reservation ID. A real provider's idempotency
  key (the reservation ID) is what closes that gap.
