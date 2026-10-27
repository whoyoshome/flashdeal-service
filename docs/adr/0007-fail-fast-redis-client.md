# 0007. Fail fast when Redis is down: no client retries

- Status: accepted
- Date: 2026-10-27

## Context

The service fails closed: with Redis unreachable, reservations answer `503
STOCK_UNAVAILABLE` instead of selling without the gate. The phase 7 chaos
test (`make test-chaos`) stops Redis under load. It showed the answer was
correct but slow.

- **The defaults stacked up.** go-redis retries a failed command up to 3
  times (`MaxRetries`). It also retries a failed dial up to 5 times
  (`DialerRetries`, with a value below 1 meaning the default). With the
  container stopped, Docker's DNS no longer resolves `redis`, so each dial
  waited out the 2 s `DialTimeout`.
- **Every reservation hung until the request timeout.** That was 5 s before
  the `503`. Under a flash sale of about 4,500 requests per second, that is
  some 22,000 requests held open, each with a goroutine and a connection,
  while the outage lasts.
- **Retries were also a correctness hazard.** A command retried after a lost
  reply can run `reserve.lua` twice. The gate then takes the buyer's units
  twice while PostgreSQL records them once. Nothing is oversold, but the
  gate understates the stock and may block the buyer's per-user limit until
  the reconciler repairs it.

## Decision

- **No command retries.** The Redis client sets `MaxRetries: -1`.
- **One dial attempt.** It sets `DialerRetries: 1`.
- **A shorter dial timeout.** `REDIS_DIAL_TIMEOUT` defaults to 500 ms
  instead of 2 s. Redis runs next to the API, where a connection takes about
  a millisecond.

A failed call surfaces as `ErrStockUnavailable`. Retrying is the client's
job, with the same `Idempotency-Key`, which makes a retry safe end to end.

## Consequences

- **Answers in milliseconds.** In the chaos test, all 2,500 reservations
  sent during the outage got `503 STOCK_UNAVAILABLE`, the slowest in 12 ms
  (5 s before). Sales resumed on their own when Redis returned. PostgreSQL
  held exactly the units buyers were told they had.
- **No silent recovery from a transient blip.** A single dropped packet
  becomes a `503` instead of being absorbed by a retry. The gate fails
  closed by design, and the client-side retry with the same key is the
  intended recovery path.
- **Startup is less tolerant.** The startup ping makes one attempt, so the
  service exits if Redis is not up yet. Compose starts the API only once
  Redis is healthy, and an orchestrator restarts it otherwise. The Redis
  testcontainer now also waits for its port to accept connections.
