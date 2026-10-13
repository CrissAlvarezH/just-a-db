---
name: deploy-server
description: Apply changes from this repo to the running just-a-db server (EC2) over SSH — pull, rebuild, restart only what changed, verify the apps that depend on it. Use for any change to docker-compose.yaml, pgbouncer/, backups/, caddy/ or panel/ that has to reach production.
---

# Deploying to the just-a-db server

The server is the production database of several apps (Moldure, wompi-router,
content-studio…). Restarting the wrong container is downtime for all of them.

## Access

- Instance `i-06266763004b66904` (`just-a-db`), us-east-1c, Elastic IP
  **44.207.50.93**, Amazon Linux 2 (EOL), arm64 (t4g.small: 2 vCPU, 1.9 GB RAM,
  **no swap**), 20 GB gp3 root disk. Repo checked out at `~/just-a-db` (branch
  `main`), docker-compose **2.17.2** (old: no `!reset`, no `env_file.required`).
- SSH: `~/Documents/personal/just-a-db/connect.sh` (next to `just-a-db.pem`,
  outside this repo). `./connect.sh` opens a shell; `./connect.sh '<cmd>'` runs
  a command. User `ec2-user`.
- **Never connect to the databases themselves** (psql, `docker exec … psql`)
  unless the user explicitly allows it for that task: they hold production
  data. Read container logs, `docker ps`, `ss`, CloudWatch instead.

## What restarts what

| Change | Command | Impact |
|---|---|---|
| `pgbouncer/` | `docker-compose build pgbouncer && docker-compose up -d --no-deps pgbouncer` | ~1 s: every app's connections drop and reconnect |
| `docker-compose.yaml` → `database` (flags, image, ports) | `docker-compose up -d --no-build database` | **Postgres restarts** (~2–12 s of errors in every app). Do it at a quiet hour, after a backup |
| `panel/` | see the `ship-panel-image` skill, then `docker-compose up -d --no-build --no-deps panel` | panel only |
| `caddy/Caddyfile`, `.panel.env` domain | `docker-compose up -d --no-deps caddy` | panel only |
| `backups/` | `docker-compose build backups` | none (runs nightly via cron: `docker-compose up -d backups` at 02:00 UTC) |

`--no-deps` keeps compose from touching `database`. Careful: the nightly cron's
`up -d backups` also reconciles `database`, so a committed-but-unapplied change
to the `database` service gets applied at 02:00 UTC by itself.

## Procedure

1. Commit and push to `main` (author: `Cristian Alvarez H
   <alvarezhernandezcristian1@gmail.com>`; push with
   `git push git@github.com-personal:CrissAlvarezH/just-a-db.git main`, since the
   default GitHub account on this machine is a work one without access).
2. If Postgres will restart: run a backup first —
   `./connect.sh 'cd ~/just-a-db && docker-compose run --rm backups'` — and
   check it reached S3 (`aws s3 ls s3://just-a-db-backups/<db>/`).
3. On the server: `cd ~/just-a-db && git pull --ff-only origin main`, then the
   row of the table above. Don't build the panel on the server (Go needs
   ~1 GB RAM next to Postgres with no swap).
4. Verify:
   - `docker ps` — all Up, `database` (healthy), restart count 0
     (`docker inspect -f '{{.RestartCount}}' <name>`).
   - Apps: `curl https://www.moldure.com/templates/alterno` → 200;
     `curl https://payments.sanninlabs.org/api/health` → `"healthy":true` with
     `"table":"fresh"`; `curl https://db.sanninlabs.org/login` → 200.
   - pgbouncer logs: `docker logs --since 10m just-a-db-pgbouncer-1 | grep -iE "error|warning"`
     (`unexpected eof` = a client that closed without a goodbye, harmless).
5. Clean up: `docker image prune -f` (the disk is 20 GB; images are ~0.5 GB each).

## Facts worth knowing

- Ports open in the security group: 22, 80, 443 (caddy → panel), 6432
  (pgbouncer, TLS required). 5432 is closed and Postgres binds to
  `127.0.0.1:5432` only.
- Env files on the server (gitignored): `.env` (AWS_REGION), `.db.env`
  (superuser), `.backup.env` (S3_BUCKET=just-a-db-backups), `.panel.env`
  (domain db.sanninlabs.org, Brevo key, ALLOWED_EMAILS; mode 600). Create a
  missing one before any compose command, or compose fails.
- `certs/` holds pgbouncer's self-signed certificate
  (`sh scripts/entrypoint.sh generate-certs` creates it if missing).
- Logs go to CloudWatch through Docker's awslogs driver: `just-a-db/database-logs`
  (Postgres, pgbouncer, panel) and `just-a-db/backups-logs`. On the server,
  `docker logs <container>` is quicker; from a laptop, the AWS CLI. Postgres
  logs mask role names as `[SENSITIVE]`.
- Container RAM at rest: Postgres ~120 MB, pgbouncer 3 MB, panel 5 MB, caddy
  14 MB; ~1.4 GB free.
