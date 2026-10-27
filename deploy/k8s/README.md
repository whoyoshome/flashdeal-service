# Kubernetes manifests

Minimal cloud-native deployment for FlashDeal: Deployment, Service, ConfigMap,
Secret example, HPA and probes that match `/healthz` and `/readyz`.

These manifests are a baseline, not a production chart. Adapt
image registry, secrets and resource requests before any real cluster.

## Apply (kind / minikube / any cluster)

```bash
# Build and load the image into your local cluster first, then:
kubectl apply -f deploy/k8s/namespace.yaml
kubectl -n flashdeal create secret generic flashdeal-secrets \
  --from-literal=postgres-url='postgres://flashdeal:flashdeal@postgres:5432/flashdeal?sslmode=disable' \
  --from-literal=jwt-secret='replace-with-at-least-32-bytes-secret!!'
kubectl apply -f deploy/k8s/
```

Postgres and Redis are expected to already exist in the cluster (or via an
operator). The ConfigMap points at service DNS names `postgres` and `redis`.

## Contents

| Manifest | Purpose |
| --- | --- |
| `deployment.yaml` | Rolling updates, resource limits, liveness + readiness probes |
| `service.yaml` | ClusterIP for in-cluster traffic |
| `hpa.yaml` | Horizontal Pod Autoscaler on CPU |
| `configmap.yaml` / Secret | Twelve-Factor config; secrets not in the image |
| Probes | `/healthz` (liveness) vs `/readyz` (readiness) |
