# Kubernetes manifests

A baseline deployment of the FlashDeal API, not a production chart. Adapt
the secrets and resource requests to the cluster before using it.

PostgreSQL and Redis are expected to already exist in the cluster (or via
an operator). The ConfigMap points at the service DNS names `postgres` and
`redis`.

## Layout

`kubectl apply -f deploy/k8s/` applies only the top-level files. The
subdirectories are applied explicitly, so neither the example secret nor
the migration Job runs by accident.

| File | Purpose |
| --- | --- |
| `namespace.yaml` | Namespace with the `restricted` Pod Security Standard enforced |
| `configmap.yaml` | Non-secret configuration (Twelve-Factor); pprof bound to loopback |
| `serviceaccount.yaml` | Dedicated identity with no Kubernetes API token |
| `deployment.yaml` | Pinned release image, non-root read-only container, probes, zero-downtime rollouts, replicas spread over nodes |
| `service.yaml` | ClusterIP for in-cluster traffic on port 8080 only |
| `hpa.yaml` | Horizontal Pod Autoscaler on CPU, 2 to 10 replicas |
| `pdb.yaml` | Keeps at least one replica through node drains |
| `jobs/migrate.yaml` | Runs `/flashdeal migrate` from the same image before a rollout |
| `examples/secret.yaml` | Shape of the secret; never apply it as is |

## Deploy a release

The image is `ghcr.io/whoyoshome/flashdeal-service:<version>`, a public
package published by CI from the `v<version>` git tag, so pulling it needs
no credentials. The manifests pin `0.8.0`.

```bash
kubectl apply -f deploy/k8s/namespace.yaml

# Secrets: the database URL and a JWT key of at least 32 bytes.
kubectl -n flashdeal create secret generic flashdeal-secrets \
  --from-literal=postgres-url='postgres://flashdeal:<password>@postgres:5432/flashdeal?sslmode=disable' \
  --from-literal=jwt-secret='<at least 32 random bytes>'
# Schema first, then the API.
kubectl apply -f deploy/k8s/serviceaccount.yaml
kubectl apply -f deploy/k8s/jobs/migrate.yaml
kubectl -n flashdeal wait --for=condition=complete --timeout=5m job/flashdeal-migrate-0-8-0
kubectl apply -f deploy/k8s/
```

To release a new version, tag it (`git tag v0.9.0 && git push origin
v0.9.0`), wait for CI to publish the image, then update the version in
`deployment.yaml` and `jobs/migrate.yaml` (including the Job's name) and
repeat the last three commands.

A fork that publishes a private package needs a pull secret: create it with
a token that has `read:packages`, and reference it from the service account.

```bash
kubectl -n flashdeal create secret docker-registry ghcr-pull \
  --docker-server=ghcr.io --docker-username=<github-user> \
  --docker-password=<token>
kubectl -n flashdeal patch serviceaccount flashdeal-api \
  -p '{"imagePullSecrets":[{"name":"ghcr-pull"}]}'
```

## Profiling

pprof listens on the pod's loopback only, so it is never reachable through
the Service or from other pods:

```bash
kubectl -n flashdeal port-forward deploy/flashdeal-api 6060:6060
go tool pprof http://localhost:6060/debug/pprof/profile?seconds=10
```

## Validation

CI checks every manifest against the Kubernetes schemas with
[kubeconform](https://github.com/yannh/kubeconform). The `k8s-smoke` job
also creates a kind cluster, applies `smoke/` + the hardened manifests
and curls `/readyz` so the Deployment is not decorative.
