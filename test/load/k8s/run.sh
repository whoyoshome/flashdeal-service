#!/usr/bin/env bash
# Runs the k6 flash-sale scenario as a Job inside the cluster where
# deploy/k8s/smoke/run.sh deployed the API, and saves its summary.
# Usage: STOCK=10000 PEAK_RPS=5000 RAMP=2s HOLD=20s ./test/load/k8s/run.sh [summary.json]
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
NAMESPACE=flashdeal
OUT="${1:-$ROOT/test/load/results/k8s-job.json}"

kubectl -n "$NAMESPACE" delete job flashdeal-k6 --ignore-not-found --wait=true
kubectl -n "$NAMESPACE" create configmap flashdeal-k6-scripts \
  --from-file="$ROOT/test/load/flash_sale.js" --from-file="$ROOT/test/load/jwt.js" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "$NAMESPACE" create configmap flashdeal-k6-params \
  --from-literal=STOCK="${STOCK:-10000}" \
  --from-literal=PEAK_RPS="${PEAK_RPS:-5000}" \
  --from-literal=RAMP="${RAMP:-2s}" \
  --from-literal=HOLD="${HOLD:-20s}" \
  --from-literal=THRESHOLDS="${THRESHOLDS:-all}" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f "$ROOT/test/load/k8s/k6-job.yaml"

# Wait for either outcome: a crossed threshold fails the Job, and that run
# is still a result worth saving.
status=""
for _ in $(seq 1 600); do
  status=$(kubectl -n "$NAMESPACE" get job flashdeal-k6 \
    -o jsonpath='{.status.succeeded}-{.status.failed}')
  [[ "$status" == "1-" || "$status" == "-1" ]] && break
  sleep 1
done

logs=$(kubectl -n "$NAMESPACE" logs job/flashdeal-k6)
printf '%s\n' "$logs" | sed '/^--- k6 summary ---$/,$d'
printf '%s\n' "$logs" | sed '1,/^--- k6 summary ---$/d' >"$OUT"
echo "summary saved to $OUT"

case "$status" in
  "1-") echo "k6 job passed" ;;
  "-1") echo "k6 job failed (a threshold was crossed or k6 errored)" >&2; exit 1 ;;
  *) echo "k6 job did not finish in time (status '$status')" >&2; exit 1 ;;
esac
