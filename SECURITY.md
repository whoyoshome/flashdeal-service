# Security Policy

## Supported versions

Only the latest release on `main`, and the container tags published from
it (`latest`, the matching `v*` version), receive security fixes.

## Reporting a vulnerability

Please **do not** open a public GitHub issue for a security finding.

Open a [private security
advisory](https://github.com/whoyoshome/flashdeal-service/security/advisories/new)
on this repository with:

- a description of the issue and its impact;
- steps to reproduce, or a proof of concept if you have one;
- the affected version or image tag, if you know it.

You should hear back within a few days. If the report is confirmed, a
fix will land on `main` and a new `v*` tag will be cut when the change
is ready. Credit is welcome unless you ask to stay anonymous.

## Scope

In scope: authentication and authorization, secret handling, the HTTP
API, the Redis stock gate and the PostgreSQL data path, the published
container image and the Kubernetes manifests under `deploy/k8s/`.

Out of scope by design:

- the shared HS256 `JWT_SECRET` used only for local demos (`make token`,
  Compose, k6); production is expected to verify tokens from an OIDC IdP
  ([ADR 0008](docs/adr/0008-jwt-authentication.md));
- credentials in `.env.example` and Compose, which are for local
  development only;
- denial-of-service that simply exhausts a single laptop VM under the
  published load scenarios.

## Hardening already in place

- Distroless, non-root image; Kubernetes pods enforce the `restricted`
  Pod Security Standard (read-only root filesystem, no capabilities).
- Secrets (Postgres URL, JWT key) come from a Kubernetes Secret, never
  from the image.
- pprof listens on the pod's loopback only.
- Dependencies are scanned in CI with `govulncheck`; Dependabot opens
  PRs for Go modules, Actions and Docker base images.
