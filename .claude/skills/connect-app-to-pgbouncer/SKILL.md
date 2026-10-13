---
name: connect-app-to-pgbouncer
description: Move an app (usually a Next.js project on Vercel) to connect through just-a-db's pgbouncer on port 6432 with TLS, or give it its own database — code changes per driver, Vercel env vars, testing on a preview before production. Use when an app connects to just-a-db or gets a new database.
---

# Connecting an app through pgbouncer

pgbouncer listens on **db.sanninlabs.org:6432** (44.207.50.93), transaction
pooling, **TLS required** with a self-signed certificate (so: encrypt, don't
verify). Port 5432 is closed to the outside. Any role created in Postgres can
log in (pgbouncer looks them up with `auth_query`), any database works.

## Which apps use it (2026-10-13)

| Vercel project | Database | Driver | Notes |
|---|---|---|---|
| moldure | `moldure` | postgres.js | `DB_SSLMODE` in `src/env.ts`, `prepare: false` |
| wompi-router (payments.sanninlabs.org) | `moldure` → moving to `wompi_router_prod` | pg (node-postgres) | handles payments: preview first |
| content-studio | `content-studio-dev` (Preview + custom env `develop`) | postgres.js | production (`main`) has no DB yet; `content-studio` db reserved |
| mevaku-app | `mevaku_db` | — | paused on Vercel, still on the old setup |

They use the env vars `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASS`, `DB_NAME`
and `DB_SSLMODE`.

## Code changes

- **postgres.js**: add `?sslmode=require` to the URL when `DB_SSLMODE` is set
  (postgres.js reads it: TLS without verifying) and `prepare: false` in
  `postgres()` — named prepared statements belong to one server connection.
  Do it where the URL is built, so the app, drizzle-kit migrations and seed
  scripts all get it.
- **pg (node-postgres) 8**: `sslmode=require` in the URL means *verify* there,
  which fails on the self-signed certificate. Pass
  `ssl: { rejectUnauthorized: false }` to `Pool`/`Client` instead, when
  `DB_SSLMODE=require`. Its parameterised queries are unnamed, so nothing to
  do about prepared statements.
- **Session state doesn't survive transaction pooling**: `SET` (use `SET LOCAL`
  inside a transaction), `LISTEN`, session advisory locks
  (`pg_advisory_lock`). Transaction-scoped ones (`pg_advisory_xact_lock`) are
  fine. wompi-router's migration runner had a session lock: replaced by a
  per-file transaction with `pg_advisory_xact_lock`.
- **Startup parameters**: pgbouncer refuses unknown ones. `statement_timeout`
  (node-postgres sends it when the Pool sets it) is in
  `ignore_startup_parameters` in `pgbouncer/entrypoint.sh`; add others there
  if an app fails with `unsupported startup parameter: …`.

## Test locally before touching Vercel

Run this repo's pgbouncer image in front of the app's local Postgres, with
TLS (certs must be under a path Docker Desktop shares, e.g. `~/`, not
`/private/tmp`):

```shell
docker build -t pgb-test pgbouncer
mkdir -p ~/.pgb-certs && cp certs/* ~/.pgb-certs/   # sh scripts/entrypoint.sh generate-certs first
docker run -d --name pgbt -p 16432:6432 -e DB_HOST=host.docker.internal -e DB_PORT=5432 \
  -e POSTGRES_USER=<local user> -e POSTGRES_PASSWORD=<local pass> \
  -v ~/.pgb-certs:/certs:ro pgb-test
# then run the app's migrations, seeds and a few queries with
# DB_HOST=localhost DB_PORT=16432 DB_SSLMODE=require
docker rm -f pgbt; rm -rf ~/.pgb-certs
```

Check a connection without TLS is refused, and that pgbouncer's log shows
`login attempt … tls=TLSv1.3`.

## Vercel

- Env vars change only on new deployments. Order: set the vars, then push (or
  redeploy) so code and vars go out together.
- Shared records (one var for Production + Preview) are edited once.
  The REST API is easiest for projects the CLI isn't linked to: token in
  `~/Library/Application Support/com.vercel.cli/auth.json`, team
  `team_GNeRjGHuKTVy17DkqNSW1Df5`; `PATCH /v9/projects/<p>/env/<id>` with
  `{"value": …}` or `{"target": […]}`, `POST /v10/projects/<p>/env`. Custom
  environments go in `customEnvironmentIds`.
- **Try it on a preview first** for anything that matters (payments):
  `vercel deploy -b KEY=V -e KEY=V` overrides vars for that deployment only,
  **but not "sensitive" ones at runtime** (the project's value wins: it fails
  with `SASL authentication failed`). For credentials, split the var instead:
  PATCH the existing record to `target: ["production"]` and add a
  preview-only record with the new value.
- Preview URLs are behind Vercel Authentication: use
  `vercel curl /api/health --deployment <url>`. `vercel link` in an app repo
  writes `.vercel/` and a `.env.local` with an OIDC token: remove both after.
- Watch the build log (`vercel inspect --logs <url>`): migrations run there,
  through pgbouncer.

## Giving an app its own database

In the panel (https://db.sanninlabs.org) → "+ Nueva base de datos": it creates
the database and a role of the same name that owns it, closed to other roles,
and shows the DB_* lines once. Point the app's vars at it, deploy a preview
(its migrations create the tables), move any data the app needs (an admin UI
is better than SQL: we don't touch production databases by hand), then switch
production.
