#!/usr/bin/env bash
# Applies the hardened manifests to a kind cluster and checks /readyz.
# Usage: IMAGE=flashdeal-service:ci ./deploy/k8s/smoke/run.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
IMAGE="${IMAGE:?IMAGE is required (tag already loaded into the cluster)}"
NAMESPACE=flashdeal

kubectl apply -f "$ROOT/deploy/k8s/smoke/stores.yaml"
kubectl -n stores wait --for=condition=Ready pod/postgres pod/redis --timeout=120s

kubectl apply -f "$ROOT/deploy/k8s/namespace.yaml"
kubectl -n "$NAMESPACE" create secret generic flashdeal-secrets \
  --from-literal=postgres-url='postgres://flashdeal:flashdeal@postgres:5432/flashdeal?sslmode=disable' \
  --from-literal=jwt-secret='flashdeal-local-dev-secret-change-me!!' \
  --dry-run=client -o yaml | kubectl apply -f -

kubectl apply -f "$ROOT/deploy/k8s/serviceaccount.yaml"
sed "s|ghcr.io/whoyoshome/flashdeal-service:0.8.0|${IMAGE}|g" \
  "$ROOT/deploy/k8s/jobs/migrate.yaml" | kubectl apply -f -
kubectl -n "$NAMESPACE" wait --for=condition=complete --timeout=180s job/flashdeal-migrate-0-8-0

kubectl apply -f "$ROOT/deploy/k8s/configmap.yaml"
sed "s|ghcr.io/whoyoshome/flashdeal-service:0.8.0|${IMAGE}|g" \
  "$ROOT/deploy/k8s/deployment.yaml" | kubectl apply -f -
kubectl apply -f "$ROOT/deploy/k8s/service.yaml"
kubectl apply -f "$ROOT/deploy/k8s/pdb.yaml"
kubectl -n "$NAMESPACE" rollout status deploy/flashdeal-api --timeout=180s

kubectl -n "$NAMESPACE" port-forward svc/flashdeal-api 18080:8080 >/tmp/flashdeal-pf.log 2>&1 &
PF_PID=$!
trap 'kill "$PF_PID" 2>/dev/null || true' EXIT

ready=0
for _ in $(seq 1 30); do
  if curl -sf -o /dev/null http://127.0.0.1:18080/readyz; then
    ready=1
    break
  fi
  sleep 1
done
if [[ "$ready" -ne 1 ]]; then
  echo "readyz did not become ready" >&2
  kubectl -n "$NAMESPACE" get pods -o wide >&2 || true
  kubectl -n "$NAMESPACE" logs deploy/flashdeal-api --tail=50 >&2 || true
  exit 1
fi

code="$(curl -sf -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/healthz)"
[[ "$code" == "200" ]]

# One authenticated round trip through the Service (two replicas behind it).
SECRET='flashdeal-local-dev-secret-change-me!!'
ADMIN="$(cd "$ROOT" && go run ./cmd/token -secret "$SECRET" -sub ops -role admin)"
USER="$(cd "$ROOT" && go run ./cmd/token -secret "$SECRET" -sub smoke-user -role user)"
DEAL="$(curl -sf -X POST http://127.0.0.1:18080/v1/deals \
  -H "Authorization: Bearer $ADMIN" -H 'Idempotency-Key: k8s-smoke-deal' \
  -H 'Content-Type: application/json' \
  -d '{"product_id":"sku-smoke","title":"Smoke","price_cents":999,"currency":"USD","total_stock":2,"per_user_limit":1,"starts_at":"2026-01-01T00:00:00Z","ends_at":"2030-01-01T00:00:00Z"}")"
printf '%s' "$DEAL" > /tmp/flashdeal-deal.json
DEAL_ID=$(python3 <<'PY'
import json
print(json.load(open("/tmp/flashdeal-deal.json"))["id"])
PY
)
curl -sf -X POST "http://127.0.0.1:18080/v1/deals/${DEAL_ID}/activate" \
  -H "Authorization: Bearer $ADMIN" >/dev/null
RES_CODE="$(curl -s -o /tmp/flashdeal-reserve.json -w '%{http_code}' -X POST \
  "http://127.0.0.1:18080/v1/deals/${DEAL_ID}/reservations" \
  -H "Authorization: Bearer $USER" -H 'Idempotency-Key: k8s-smoke-reserve' \
  -H 'Content-Type: application/json' -d '{"quantity":1}')"
[[ "$RES_CODE" == "201" ]]

REPLICAS="$(kubectl -n "$NAMESPACE" get deploy flashdeal-api -o jsonpath='{.status.readyReplicas}')"
[[ "$REPLICAS" -ge 2 ]]

echo "k8s smoke ok (healthz=$code, readyz=200, reserve=201, readyReplicas=$REPLICAS, image=$IMAGE)"
