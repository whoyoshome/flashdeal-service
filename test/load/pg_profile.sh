#!/usr/bin/env bash
# Runs the k6 flash sale against the Compose stack while sampling what
# PostgreSQL's active sessions are doing, then prints where the time went.
#
# Sessions are sampled from pg_stat_activity every 200 ms: an active
# session with no wait event is on CPU, otherwise it is waiting for a lock
# (Lock/LWLock), for I/O (IO, e.g. WALWrite or WALSync) or for the client.
# WAL and transaction counters are diffed around the run, and container CPU
# is sampled with docker stats.
#
# Usage: STOCK=10000 PEAK_RPS=5000 RAMP=2s HOLD=20s ./test/load/pg_profile.sh [out-dir]
# Requires the stack to be up (make up && make migrate).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="${1:-$ROOT/test/load/results/pg-profile}"
mkdir -p "$OUT"
cd "$ROOT"

psql() { docker compose exec -T postgres psql -U flashdeal -d flashdeal -X -At "$@"; }

# Timing of WAL writes and syncs is off by default; turn it on for this run
# only and put the previous value back on exit.
prev_wal_timing=$(psql -c 'SHOW track_wal_io_timing')
restore() {
  psql -c "ALTER SYSTEM SET track_wal_io_timing = '$prev_wal_timing'" -c 'SELECT pg_reload_conf()' >/dev/null || true
}
trap restore EXIT
psql -c "ALTER SYSTEM SET track_wal_io_timing = on" -c 'SELECT pg_reload_conf()' >/dev/null

counters="SELECT
  (SELECT xact_commit + xact_rollback FROM pg_stat_database WHERE datname = current_database()),
  wal_records, wal_bytes, wal_write, wal_sync, round(wal_write_time), round(wal_sync_time)
FROM pg_stat_wal"
before=$(psql -F ' ' -c "$counters")

# One session samples itself in a loop, so each sample costs a query, not a
# docker exec. Only sessions of the API's database that are running a
# statement count.
samples=$(( ${SAMPLE_SECONDS:-40} * 5 ))
# Every sample emits at least one row (idle when nothing runs), stamped with
# its time, so samples can be counted.
psql -F '|' >"$OUT/waits.txt" <<SQL &
SELECT to_char(clock_timestamp(), 'HH24:MI:SS.MS'), coalesce(a.type, 'idle'), coalesce(a.event, '-'), count(a.pid)
FROM (SELECT 1) AS one
LEFT JOIN (
  SELECT pid, coalesce(wait_event_type, 'CPU') AS type, coalesce(wait_event, 'running') AS event
  FROM pg_stat_activity
  WHERE datname = current_database() AND state = 'active' AND pid <> pg_backend_pid()
) AS a ON true
GROUP BY 1, 2, 3
\watch interval=0.2 count=$samples
SQL
sampler=$!

(
  for _ in $(seq 1 "${CPU_SAMPLES:-15}"); do
    docker stats --no-stream --format '{{.Name}} {{.CPUPerc}}'
  done
) >"$OUT/cpu.txt" 2>&1 &
cpu=$!

set +e
make --no-print-directory load LOAD_SUMMARY=pg-profile.json 2>&1 | tee "$OUT/k6.txt" |
  grep -E 'reserve_ok_duration\.|reserve_sold_out_duration\.|reservations_created\.|reservations_timed_out\.|dropped_iterations|http_reqs\.'
set -e
wait "$sampler" "$cpu" || true

after=$(psql -F ' ' -c "$counters")

echo
echo "== Active PostgreSQL sessions by wait event (share of all samples)"
awk -F'|' 'NF == 4 { seen[$1] = 1; if ($2 != "idle") { n[$2 "/" $3] += $4; total += $4; busy[$1] += $4 } }
  END { for (k in n) printf "%6.1f%%  %s\n", 100 * n[k] / total, k | "sort -rn"; close("sort -rn");
        s = 0; for (t in seen) s++; b = 0; for (t in busy) b++;
        printf "samples: %d, with activity: %d, active sessions per busy sample: %.1f\n", s, b, (b ? total / b : 0) }' "$OUT/waits.txt"

echo
echo "== Counters during the run"
read -r tx0 rec0 bytes0 wr0 sync0 wrt0 synct0 <<<"$before"
read -r tx1 rec1 bytes1 wr1 sync1 wrt1 synct1 <<<"$after"
echo "transactions:   $((tx1 - tx0))"
echo "WAL records:    $((rec1 - rec0))"
echo "WAL bytes:      $((bytes1 - bytes0))"
echo "WAL writes:     $((wr1 - wr0)) ($((wrt1 - wrt0)) ms)"
echo "WAL syncs:      $((sync1 - sync0)) ($((synct1 - synct0)) ms)"

echo
echo "== Container CPU (docker stats, % of one CPU; max and mean over the samples)"
awk '$2 ~ /%$/ { v = $2 + 0; s[$1] += v; c[$1]++; if (v > m[$1]) m[$1] = v }
  END { for (k in s) if (m[k] > 1) printf "%-28s max %6.1f%%  mean %6.1f%%\n", k, m[k], s[k] / c[k] }' "$OUT/cpu.txt"
