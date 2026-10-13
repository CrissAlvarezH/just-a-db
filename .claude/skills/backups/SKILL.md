---
name: backups
description: How just-a-db's backups work and how to run, check, find logs for, and restore them. Use when touching backups/run.sh, investigating a failed backup, or restoring a database.
---

# Backups

`backups/run.sh` (bash, in the `backups` image and also inside the panel's):

1. Prints `=== Backup run started (<all|db>)`; on exit (also on SIGTERM)
   `=== Backup run finished: <ok|failed> (<all|db>)`. Its whole output goes to
   its own CloudWatch stream, `just-a-db/backup-runs` /
   `<UTC start>_<all|db>_<nightly|manual>` (`BACKUP_TRIGGER=manual` from the
   panel), sent after each step and at the end; a CloudWatch error never fails
   the backup. The panel's "Últimas corridas" lists the latest streams; one
   with no "finished" line and no news for 30 min shows as "interrumpida".
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
  "Hacer backup de X ahora" (one).

## Logs

- Every run (nightly or manual): CloudWatch `just-a-db/backup-runs`, one
  stream per run, linked from the panel (Backups → Últimas corridas).
- `aws logs describe-log-streams --log-group-name just-a-db/backup-runs --order-by LogStreamName --descending --max-items 5`.
- The backups container itself uses Docker's `local` driver: `docker logs`
  on the server only.

## Restoring

- Into a new database from the panel: "+ Nueva base de datos" → Contenido =
  a backup (`.dump` via `pg_restore --no-owner --no-privileges --role=<new user>
  --single-transaction`, or an older `.sql.gz` via psql with owner/GRANT lines
  filtered out). One transaction: a failure leaves nothing behind.
- By hand: `pg_restore -h <host> -U <user> -d <new_db> --no-owner <file>.dump`.
- Locally (laptop), an old plain dump into a Postgres 16:
  `gunzip -c x.sql.gz | grep -v -E "OWNER TO|^GRANT |^REVOKE |^SET transaction_timeout" | psql -d <db>`.
