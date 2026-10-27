# Phase 7: chaos under load

`make test-chaos` ([`test/chaos`](../chaos/chaos_test.go)) breaks the
running Docker Compose stack while 10 workers keep reserving units of one
deal (about 500 attempts per second). It checks the two degradation promises
in `docs/ARCHITECTURE.md`:

- With Redis down, the service fails closed with `503` and sells nothing
  without the gate.
- On `SIGTERM`, requests in flight finish and the process exits with code 0.

Both tests check the same correctness invariant at the end. The deal's
reserved plus sold units in PostgreSQL must equal the number of `201`
answers the workers received. A sale without the gate would make PostgreSQL
hold more than buyers were told. So would a request completed on the server
but cut short on the wire.

## Redis stopped under load

The test runs `docker compose stop redis` for 5 s, then
`docker compose start redis`. Each request is counted under the phase in
which it was **sent**.

| Phase           | Answers                                    | Slowest |
| --------------- | ------------------------------------------ | ------- |
| before          | 999 × `201`                                | 18 ms   |
| stopping        | 105 × `201`, 328 × `503 STOCK_UNAVAILABLE` | 53 ms   |
| **outage**      | **2 500 × `503 STOCK_UNAVAILABLE`**        | **12 ms** |
| Redis restarting | 30 × `201`, 720 × `503 STOCK_UNAVAILABLE` | 20 ms   |
| after           | 1 500 × `201`                              | 13 ms   |

- **Every reservation sent with Redis down was refused**, with no `500`, no
  timeout and no sale.
- **The service recovered on its own.** No restart was needed. The deal's
  keys survived the restart, so activating it again (which the test does,
  since activation is idempotent) changed nothing.
- **Zero overselling.** PostgreSQL held exactly the 2,634 units answered
  `201`.

### What the first run found

The first run passed on correctness but failed on speed. **Every `503` took
5.0 s.** go-redis retried each failed command 3 times and each failed dial 5
times. Every dial waited out the 2 s timeout, because Docker's DNS stops
resolving a stopped container. So requests hung until the 5 s request
timeout. Under a real flash sale, that is thousands of requests held open
for the whole outage.

The client now retries neither commands nor dials, and the dial timeout
defaults to 500 ms ([ADR 0007](../../docs/adr/0007-fail-fast-redis-client.md)).
The test requires every outage answer within 1 s.

## API stopped (`SIGTERM`) under load

The test runs `docker compose stop api` (`SIGTERM`, with a 20 s grace
period), then checks the container's exit code and log.

| Phase   | Answers                                                       |
| ------- | ------------------------------------------------------------- |
| before  | 997 × `201`                                                   |
| stopping | 60 × `201`, 388 connections closed by the port proxy         |
| stopped | 370 connections refused, 107 closed by the port proxy         |

- **Exit code 0**, and `"msg":"stopped"` logged after the HTTP and admin
  servers shut down.
- **No `5xx`.** Every request the server accepted was answered.
- **No answer lost.** PostgreSQL held exactly the 1,057 units answered
  `201`.
- **The closed connections come from Docker Desktop's port proxy.** It keeps
  accepting TCP connections on `localhost:8080` while the container behind
  it is going away, then closes them unanswered. The client cannot tell
  those from a request cut short. PostgreSQL can, through the invariant
  above.

## How to run

```bash
make up
make migrate
make test-chaos
```

The tests stop and start containers of the running stack, so they run
sequentially and leave every service running again when they finish.
