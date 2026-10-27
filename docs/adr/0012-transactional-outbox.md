# 0012. Publish order.confirmed through a transactional outbox

- Status: accepted
- Date: 2026-10-27

## Context

Other systems (fulfilment, email, analytics) need to know when an order is
placed. Publishing straight from the confirmation use case has two ways to
fail:

- Publish, then commit: if the commit fails, consumers see an order that
  does not exist.
- Commit, then publish: if the publish fails or the process dies in
  between, an order exists that nobody hears about.

The stack has no message broker. Adding Kafka or NATS would add a
dependency and a service to run. Redis already runs next to every
replica, and its Streams support consumer groups and acknowledgements.

## Decision

- **Record the event with the order.** `OrderRepo.Create` inserts the
  order and an `outbox` row in one SQL statement. They commit or roll back
  together, and a duplicate order (`ErrAlreadyExists`) records no second
  event. The confirmation use case is unchanged.
- **Relay.** Every replica runs a worker (`OUTBOX_EVERY`, 1 s by default).
  Each pass works in batches (`OUTBOX_BATCH_SIZE`, 100). For each batch, in
  one transaction, it:
  1. locks the oldest events with `FOR UPDATE SKIP LOCKED`, so relays on
     different replicas take disjoint batches;
  2. appends them to the Redis Stream `OUTBOX_STREAM` (`flashdeal:events`)
     with one pipelined `XADD` per event;
  3. deletes them, then commits.

  The table therefore only holds unpublished events.
- **At least once.** If the transaction fails after the `XADD` (a crash, a
  failed delete or commit), the events are published again on the next
  pass. Each entry carries `event_id` (the outbox row ID), which consumers
  use to drop duplicates.
- **Entry format.** Each stream entry has these fields:
  - `event_id`;
  - `type`: `order.confirmed`;
  - `aggregate_id`: the order ID;
  - `occurred_at`: RFC 3339, UTC;
  - `payload`: a JSON object with `order_id`, `reservation_id`, `deal_id`,
    `user_id`, `quantity`, `total_cents`, `currency` and `created_at`.

  Fields may be added, never renamed or removed.
- **Bounded stream.** `XADD` trims the stream to about
  `OUTBOX_STREAM_MAXLEN` entries (100,000), so it cannot fill Redis when no
  consumer reads it.
- **Observability.** `outbox_events_published_total` counts published
  events. A failed pass is logged once by the worker and retried on the
  next tick.

## Consequences

- No order is ever announced without existing, and none goes unannounced
  while PostgreSQL keeps it.
- **No global order.** Within one batch, events are appended in ID order,
  but two replicas' batches can interleave. There is one event per order,
  so consumers need no ordering between orders.
- **Redis down.** The stock gate already fails closed, so no new orders are
  placed. Orders confirmed before the outage stay in the outbox and are
  published once Redis returns.
- **Retention is Redis's.** An entry stays published once the outbox row is
  deleted. A consumer further behind than `OUTBOX_STREAM_MAXLEN` loses
  entries. Redis persistence (AOF) decides whether entries survive a Redis
  restart. Compose runs Redis with its defaults and no volume, so
  recreating its container loses the stream.
- **Latency.** An event reaches the stream up to `OUTBOX_EVERY` after its
  commit. The relay adds one `XADD` pipeline and one `DELETE` per batch,
  off the request path.
- **Moving to a broker later.** A Kafka or NATS publisher replaces only the
  `EventPublisher` adapter; the outbox and its guarantees stay.
