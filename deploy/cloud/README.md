# Public demo (Neon + Upstash + Render)

Goal: a recruiter opens the service URL from your CV or GitHub homepage,
clicks **Start demo sale**, reserves one unit and confirms an order —
without installing Docker or talking to you.

Stack (same shape as a typical portfolio SaaS):

| Piece | Service | Why |
| --- | --- | --- |
| API + demo UI | [Render](https://render.com) free web service | Builds the `Dockerfile`, HTTPS URL |
| PostgreSQL | [Neon](https://neon.tech) free | Managed Postgres (`sslmode=require`) |
| Redis | [Upstash](https://upstash.com) free | Managed Redis (`rediss://…` via `REDIS_URL`) |

`DEMO_ENABLED` serves `GET /` and `POST /v1/demo/session` (ADR 0015).
`MIGRATE_ON_START` applies schema on boot so you do not need a migrate Job.

## 1. Neon

1. Create a project and copy the connection string.
2. Ensure it includes `sslmode=require` (Neon’s UI usually does).

Example shape (not a real secret):

```text
postgres://user:pass@ep-….aws.neon.tech/neondb?sslmode=require
```

## 2. Upstash

1. Create a Redis database.
2. Copy the **Redis URL** (`rediss://default:…@….upstash.io:6379`).

## 3. Render

1. Push this branch (or `main` after merge) to GitHub.
2. In Render: **New → Blueprint**, select the repo (it reads `render.yaml`).
3. When prompted for sync-false env vars, paste:
   - `POSTGRES_URL` → Neon string
   - `REDIS_URL` → Upstash string
   - `JWT_SECRET` → at least 32 random bytes, e.g.
     `openssl rand -base64 32`
4. Deploy. First boot runs migrations, then listens. Free tier cold starts
   can take 30–60 seconds; the demo page says so.

Optional: deploy without Blueprint — **New → Web Service**, connect the
repo, Docker runtime, same env vars as in `render.yaml`.

## 4. Wire the URL so recruiters find it

1. Open the Render URL (e.g. `https://flashdeal.onrender.com`).
2. Click through: Start → Reserve → Confirm.
3. Set the GitHub repo **Homepage** to that URL.
4. Put the same link under **Live demo** in the README (replace the
   placeholder).

## Local check of the demo UI

With Compose already up:

```bash
DEMO_ENABLED=true JWT_SECRET=flashdeal-local-dev-secret-change-me!! \
  # or restart the api service with those env vars in Compose
```

Or hit a cloud deploy after step 3.

## What not to expect

- Grafana / Jaeger / k6 on the free host (keep those on `make demo` locally).
- Always-warm free Render instances (cold start is normal).
- Demo mode on a real production IdP (`JWT_JWKS_URL`); leave `DEMO_ENABLED`
  off outside this portfolio deploy.
