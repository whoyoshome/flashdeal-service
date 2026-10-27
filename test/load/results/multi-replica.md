# Multi-replica load (kind, 2 API pods)

Measured on 2026-10-27 against a local kind cluster running the hardened
manifests (`deploy/k8s/`, `replicas: 2`) with ephemeral Postgres and Redis
from `deploy/k8s/smoke/`. Traffic entered through the ClusterIP Service via
`kubectl port-forward` so both pods shared the load.

## Setup

| Piece | Value |
| --- | --- |
| Cluster | kind `flashdeal-smoke` |
| API replicas | 2 (`deploy/k8s/deployment.yaml`) |
| Image | locally built `flashdeal-service:ci` |
| Stock | 1 000 |
| Peak attempt rate | 1 000 rps for 8 s (2 s ramp) |
| Thresholds | correctness only (`THRESHOLDS=correctness`) |
| Auth | HS256 JWT (`make token` secret) |
| Rate limit | defaults (50/s, burst 100); each k6 buyer uses a unique `sub` |

Raw summary: [`multi-replica.json`](multi-replica.json).

## Results

| Metric | Value |
| --- | --- |
| Reservations created | **1 000** (exactly stock) |
| Sold-out responses | 8 000 |
| Oversell | **0** |
| Unexpected responses | **0** |
| Timed out / 5xx | **0** |
| `reserve_ok` p50 / p99 | 2.23 ms / 72.56 ms |
| `reserve_sold_out` p50 / p99 | 1.42 ms / 9.77 ms |
| Offered iterations | 9 000 completed |

Correctness held with two replicas behind one Service: Redis Lua and
PostgreSQL constraints, not an in-process lock, decide stock. Latency is
not the laptop target from the architecture doc (that needs a tuned multi-
node cluster); the point of this run is zero overselling under multi-
replica contention.

## How to reproduce

```bash
# Build, kind cluster, smoke (2 ready replicas + /readyz + one reserve)
docker build -t flashdeal-service:ci .
go run sigs.k8s.io/kind@v0.30.0 create cluster --name flashdeal-smoke --wait 120s
go run sigs.k8s.io/kind@v0.30.0 load docker-image flashdeal-service:ci --name flashdeal-smoke
# also load postgres:17-alpine and redis:7.4-alpine into kind
IMAGE=flashdeal-service:ci ./deploy/k8s/smoke/run.sh

# Keep a port-forward, then:
make load-k8s STOCK=1000 PEAK_RPS=1000 RAMP=2s HOLD=8s THRESHOLDS=correctness
```
