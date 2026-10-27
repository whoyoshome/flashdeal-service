# Scenario A: where PostgreSQL spends the burst

Measured on 2026-10-27 on the 4-CPU development VM (Compose, 1 API
replica, pool of 20 open connections) with
[`pg_profile.sh`](../pg_profile.sh). The script runs `make load` and, at
the same time, does three things:

- It samples PostgreSQL's active sessions from `pg_stat_activity` every
  200 ms. An active session with no wait event is on CPU; otherwise it is
  waiting for a row lock (`Lock`), an internal lock (`LWLock`), I/O (`IO`)
  or the client.
- It diffs the transaction and WAL counters around the run, with
  `track_wal_io_timing` on for that run only.
- It samples container CPU with `docker stats`.

Each configuration had a warm-up run first. Scenario A sells 10,000 units
at 5,000 attempts per second.

## The sale lasts about 3 seconds

PostgreSQL had active sessions in only 14–35 of the 200 samples. Those
samples all fall in the first 3–4 seconds: all 10,000 units sell during the
ramp, at roughly 3,000 sales per second. After that the deal is sold out,
and Redis answers every request. Scenario A's reservation latency is
therefore the queue that builds up during that burst.

## 8 shards: sessions wait on each other's row locks

| Wait | Share of active session samples |
| --- | --- |
| `Lock/transactionid` and `Lock/tuple` (row locks) | 65–82 % |
| `CPU/running` | 9–18 % |
| `LWLock/BufferContent` | 4–13 % |
| `IO` (WAL writes and syncs) | 0 % |

- About 8 sessions were active per busy sample, and most of them were
  queued behind a row lock.
- With 8 shards and up to 20 transactions in flight, every shard is often
  locked. `SKIP LOCKED` then finds no free shard, and the reservation falls
  back to waiting for one (ADR 0011).
- The disk is not the limit: the whole run made 25–26 WAL syncs, 72–94 ms
  in total, with asynchronous commit (ADR 0006).
- PostgreSQL's CPU averaged 11–16 %.

## 32 shards: more free shards than connections

The same run with `STOCK_SHARDS=32`:

| Shards | `reserve_ok` p50 / p99 | Row-lock waits | Active sessions per busy sample | Dropped iterations |
| --- | --- | --- | --- | --- |
| 8 | 73.9 / 500.1 ms, then 52.9 / 258.5 ms | 65–82 % | 7.9–8.5 | 1,687–2,721 |
| 32 | 40.2 / 181.3 ms, then 13.4 / 111.1 ms | 19–32 % | 2.3–3.2 | 570–934 |

- With 32 shards, PostgreSQL sessions spend most of their time on CPU
  (53–64 %) instead of waiting.
- The API's average wait for a pool connection fell to about 37 ms
  (6,175 of 11,291 acquires waited). With 8 shards it was 99–146 ms.
- During the burst, the API used about 1 CPU, PostgreSQL 0.6–0.9, k6
  0.5–0.8 and Redis 0.3–0.4: most of the VM's 4 CPUs.
- Scenario B was not hurt. Three runs with 32 shards gave a reservation
  p99 of 9.1, 5.6 and 15.4 ms, against 7–11 ms with 8.

Scenario A still misses the 50 ms target. What remains is about 3,000
transactions per second on one VM shared with the load generator. Each
one holds a pool connection for five round trips: `BEGIN`, `SET LOCAL`,
`INSERT`, the shard update batch and `COMMIT`.

## Next levers

1. **More shards by default.** Done: `STOCK_SHARDS` now defaults to 32. It
   removes most of the lock waiting at no cost to scenario B, and a shard
   is still never smaller than `per_user_limit` (ADR 0011).
2. **Fewer round trips per reservation.** Folding the steps into fewer
   statements would shorten how long each reservation holds its pool
   connection.
3. **Dedicated hardware**, where PostgreSQL and the load generator do not
   share 4 CPUs.

## How to reproduce

```bash
make up && make migrate
STOCK=1000 PEAK_RPS=5000 RAMP=2s HOLD=10s make load   # warm-up
STOCK=10000 PEAK_RPS=5000 RAMP=2s HOLD=20s ./test/load/pg_profile.sh test/load/results/pg-profile

# The same with 32 shards (new deals only):
STOCK_SHARDS=32 docker compose up -d --wait api
```

The raw samples (`waits.txt`, `cpu.txt`, `k6.txt`) stay in the output
directory and are git-ignored.
