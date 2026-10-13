---
name: backups
description: How just-a-db's backups work and how to run, check, find logs for, and restore them. Use when touching backups/run.sh, investigating a failed backup, or restoring a database.
---

# Backups

`backups/run.sh` (bash, in the `backups` image and also inside the panel's):

1. Prints `=== Backup run started (<all|db>)`; on exit
   `=== Backup run finished: <ok|failed> (<all|db>)` (the panel's "Últimas
   corridas" looks for these lines in CloudWatch).
2. Roles: `pg_dumpall --globals-only` → `s3://<bucket>/_globals/`.
3. Each database (or only `$1`): `pg_dump -Fc`, rejected if under
   `MIN_BACKUP_BYTES` (200; an empty database dumps to ~850 bytes) or if
   `pg_restore --list` can't read it, then uploaded to
   `s3://<bucket>/<db>/backup_<db>_<UTC timestamp>.dump`.
4. Only if everything succeeded: keeps the newest `BACKUP_KEEP` (14) per
   folder. Any error aborts before pruning.
5. Healthcheck pings (`BACKUP_HEALTHCHECK_URL`, optional) only for full runs.

`aws s3 ls` exits 1 on an empty prefix: wrap it (`|| true`) under `pipefail`.

## Running

- Nightly: host cron `0 2 * * *` (UTC) → `docker-compose up -d backups`.
- By hand on the server: `./connect.sh 'cd ~/just-a-db && docker-compose run --rm backups'`
  (or `… backups /app/run.sh <db>` for one database).
- From the panel: "Hacer backup ahora" (all) or, on a database's delete page,
  "Hacer backup de X ahora" (one). Manual runs' output goes to the panel's
  stdout prefixed with `[backup] `.

## Logs

- Nightly: CloudWatch `just-a-db/backups-logs` (Docker awslogs driver).
- Manual: CloudWatch `just-a-db/database-logs`, the panel container's stream,
  lines prefixed `[backup] `.
- `aws logs filter-log-events --log-group-name just-a-db/backups-logs --filter-pattern '"Backup run finished"'`.

## Restoring

- Into a new database from the panel: "+ Nueva base de datos" → Contenido =
  a backup (`.dump` via `pg_restore --no-owner --no-privileges --role=<new user>
  --single-transaction`, or an older `.sql.gz` via psql with owner/GRANT lines
  filtered out). One transaction: a failure leaves nothing behind.
- By hand: `pg_restore -h <host> -U <user> -d <new_db> --no-owner <file>.dump`.
- Locally (laptop), an old plain dump into a Postgres 16:
  `gunzip -c x.sql.gz | grep -v -E "OWNER TO|^GRANT |^REVOKE |^SET transaction_timeout" | psql -d <db>`.
