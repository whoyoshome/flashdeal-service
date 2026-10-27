# Contributing

Thanks for taking an interest in FlashDeal. The notes below keep a pull
request reviewable and aligned with the design in
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) and the engineering rules in
[`CLAUDE.md`](CLAUDE.md).

## Before you start

1. Read the relevant section of `docs/ARCHITECTURE.md`. If a change would
   contradict it, open an issue or an ADR draft first.
2. Ask before: adding a dependency, changing the database schema, changing
   the public API contract (`api/openapi.yaml`), or touching more than
   about eight files in one PR.

Allowed Go dependencies are listed in `CLAUDE.md`. Anything else needs an
ADR under `docs/adr/`.

## Local setup

You need Docker Compose v2, GNU make and Go (the version in `go.mod`). On
Windows, `go test -race` needs a C compiler such as MinGW-w64.

```bash
make up          # API, Postgres, Redis; add observability with the default profile
make migrate
make token       # mint a local JWT (TOKEN_SUB, TOKEN_ROLE=user|admin)
make lint test   # must be green before you open a PR
```

If a local PostgreSQL or Redis already holds 5432 or 6379, set
`POSTGRES_HOST_PORT` / `REDIS_HOST_PORT` in a `.env` file (see
`.env.example`). Containers keep using 5432 and 6379 on the Compose
network.

Useful targets: `make test-integration`, `make test-e2e`, `make test-chaos`,
`make load`, `make lint-api`, `make lint-k8s`, `make demo`.

## Pull requests

- Branch from `main`: `feat/<topic>` or `fix/<topic>`.
- One logical change per PR. Conventional Commits, English, imperative
  mood, subject ≤ 72 characters (for example
  `feat(reservation): add idempotency middleware`).
- Update `api/openapi.yaml` in the same commit as any contract change.
  Update migrations as new numbered files; never edit an applied
  migration.
- Write or update tests with the code. Concurrency-sensitive paths need an
  integration test run with `-race`.
- Do not weaken a failing test to get green; fix the code or explain the
  blocker.
- CI must pass: lint (including `govulncheck` and OpenAPI / kubeconform
  checks), unit tests with `-race`, integration, e2e and the image build.

## Language and style

- Repository content is English: identifiers, comments, logs, errors, API
  fields, tests, commits, PRs, README and ADRs.
- The only Spanish design document is `docs/ARCHITECTURE.md`.
- Dependencies point inward: `delivery → usecase → domain`. Handlers stay
  thin; stock correctness lives only in Redis Lua scripts and PostgreSQL
  conditional updates / `CHECK` constraints.

## Security

See [`SECURITY.md`](SECURITY.md) for how to report a vulnerability. Never
commit secrets, `.env` files or binaries.
