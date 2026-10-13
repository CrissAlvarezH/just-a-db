#!/bin/bash
# Dumps every database (plus roles) to S3, then prunes old backups. Any error
# aborts the run before anything is pruned, and pings BACKUP_HEALTHCHECK_URL
# (if set) with /fail so a broken backup doesn't go unnoticed.
#
# `run.sh <database>` backs up only that one (the panel does it before deleting
# a database); it doesn't ping the healthcheck, which tracks the nightly run.
#
# Each run's output goes to its own CloudWatch Logs stream,
# <UTC timestamp>_<all|database>_<nightly|manual> in BACKUP_RUN_LOG_GROUP
# (empty: no CloudWatch), sent after each step and at the end, failed or not.
# The panel lists the latest streams. Each run is framed by
# "=== Backup run started (<all|database>)" and
# "=== Backup run finished: <ok|failed> (<all|database>)".
set -euo pipefail

ONLY_DB="${1:-}"
SCOPE="${ONLY_DB:-all}"
TIMESTAMP=$(date -u +"%Y-%m-%d_%H-%M-%S")
# The panel sets BACKUP_TRIGGER=manual.
TRIGGER="${BACKUP_TRIGGER:-nightly}"
RUN_LOG_GROUP="${BACKUP_RUN_LOG_GROUP-just-a-db/backup-runs}"
# ':' and '*' aren't allowed in stream names.
RUN_STREAM="${TIMESTAMP}_${SCOPE//[:*]/-}_${TRIGGER}"
RUN_LOG="/tmp/backup_run_${TIMESTAMP}.log"
LOG_SENT=0 # lines of RUN_LOG already in CloudWatch

# Every line goes to the original stdout (docker logs, or the panel, which
# shows it live) and to RUN_LOG with its time in microseconds.
exec 3>&1
: > "$RUN_LOG"
exec > >(
  while IFS= read -r line || [ -n "$line" ]; do
    printf '%s\t%s\n' "${EPOCHREALTIME/./}" "$line" >> "$RUN_LOG"
    printf '%s\n' "$line" >&3
  done
) 2>&1
CAPTURE_PID=$!

# Sends RUN_LOG's new lines to the run's stream. A CloudWatch failure never
# fails the backup: the log just stays local (call it as `send_log || true`).
send_log() {
  [ -n "$RUN_LOG_GROUP" ] || return 0
  local lines count
  lines=$(tail -n +"$((LOG_SENT + 1))" "$RUN_LOG")
  [ -n "$lines" ] || return 0
  count=$(printf '%s\n' "$lines" | wc -l)
  # aws s3 cp's progress lines are overwritten with \r: keep the last one.
  printf '%s\n' "$lines" | jq -Rn '[inputs | capture("^(?<t>[0-9]+)\t(?<m>.*)$")
    | {timestamp: ((.t | tonumber) / 1000 | floor),
       message: ([.m | split("\r")[] | select(. != "")] | last // " ")}]' > "$RUN_LOG.json"
  if aws logs put-log-events --log-group-name "$RUN_LOG_GROUP" --log-stream-name "$RUN_STREAM" \
      --log-events "file://$RUN_LOG.json" > /dev/null; then
    LOG_SENT=$((LOG_SENT + count))
  else
    echo "Couldn't send this run's log to CloudWatch"
    RUN_LOG_GROUP=""
  fi
}

finish() {
  local status=$? result=ok
  [ "$status" -eq 0 ] || result=failed
  echo "=== Backup run finished: $result ($SCOPE)"
  # Back to the original stdout: the capture sees EOF and writes its last lines.
  exec >&3 2>&3
  wait "$CAPTURE_PID" 2>/dev/null || sleep 1
  send_log || true
  rm -f "$RUN_LOG" "$RUN_LOG.json"
  exit "$status"
}
trap finish EXIT
# docker stop / the panel's timeout send SIGTERM: still finish (and send the log).
trap 'exit 143' TERM INT

echo "=== Backup run started ($SCOPE)"
if [ -n "$RUN_LOG_GROUP" ] && ! aws logs create-log-stream \
    --log-group-name "$RUN_LOG_GROUP" --log-stream-name "$RUN_STREAM"; then
  echo "Couldn't create this run's CloudWatch log stream, its log stays local"
  RUN_LOG_GROUP=""
fi
send_log || true

DB_HOST="${DB_HOST:-database}"
DB_PORT="${DB_PORT:-5432}"
# How many backups of each database to keep in S3.
BACKUP_KEEP="${BACKUP_KEEP:-14}"
# A dump smaller than this (bytes) is treated as failed. An empty database
# dumps to ~850 bytes, so this only catches truncated files; pg_restore --list
# below checks the rest.
MIN_BACKUP_BYTES="${MIN_BACKUP_BYTES:-200}"

export PGPASSWORD="$POSTGRES_PASSWORD"

ping_healthcheck() {
  if [ -n "${BACKUP_HEALTHCHECK_URL:-}" ] && [ -z "$ONLY_DB" ]; then
    wget -q -O /dev/null -T 10 "${BACKUP_HEALTHCHECK_URL}$1" || true
  fi
}
trap 'echo "Backup FAILED"; ping_healthcheck /fail' ERR

ping_healthcheck /start

if [ -n "$ONLY_DB" ]; then
  # psql only interpolates :'var' in queries read from stdin, not from -c.
  databases=$(echo "SELECT datname FROM pg_database WHERE datname = :'db'" |
    psql -h "$DB_HOST" -p "$DB_PORT" -U "$POSTGRES_USER" -d postgres -At -v db="$ONLY_DB")
  if [ -z "$databases" ]; then
    echo "Database $ONLY_DB not found"
    exit 1
  fi
else
  databases=$(psql -h "$DB_HOST" -p "$DB_PORT" -U "$POSTGRES_USER" -d postgres -At \
    -c "SELECT datname FROM pg_database WHERE datname NOT IN ('template0', 'template1', 'postgres')")
fi

# Roles and their passwords live outside every database dump.
GLOBALS_FILE="/tmp/globals_${TIMESTAMP}.sql.gz"
pg_dumpall -h "$DB_HOST" -p "$DB_PORT" -U "$POSTGRES_USER" --globals-only | gzip > "$GLOBALS_FILE"
aws s3 cp "$GLOBALS_FILE" "s3://$S3_BUCKET/_globals/$(basename "$GLOBALS_FILE")"
rm "$GLOBALS_FILE"
send_log || true

for db in $databases; do
  # Custom format: compressed, and pg_restore can restore single tables.
  BACKUP_FILE="backup_${db}_${TIMESTAMP}.dump"
  echo "Backing up database: $db to /tmp/$BACKUP_FILE"
  pg_dump -h "$DB_HOST" -p "$DB_PORT" -U "$POSTGRES_USER" -Fc -f "/tmp/$BACKUP_FILE" "$db"

  size=$(stat -c %s "/tmp/$BACKUP_FILE")
  if [ "$size" -lt "$MIN_BACKUP_BYTES" ]; then
    echo "Backup of $db is only $size bytes, aborting"
    exit 1
  fi
  # Readable end to end, not just non-empty.
  pg_restore --list "/tmp/$BACKUP_FILE" > /dev/null

  s3_path="s3://$S3_BUCKET/$db/$BACKUP_FILE"
  echo "Uploading $db backup ($size bytes) to $s3_path"
  aws s3 cp "/tmp/$BACKUP_FILE" "$s3_path"
  rm "/tmp/$BACKUP_FILE"
  send_log || true
done

echo "All database backups completed and uploaded successfully"

# Only reached when every backup above succeeded.
prune() {
  local prefix="$1"
  local keep="${2:-$BACKUP_KEEP}"
  # `aws s3 ls` lines start with the date, so a plain sort is oldest first.
  local backups
  # `aws s3 ls` exits 1 when the prefix is empty (e.g. _logs on the first run).
  backups=$({ aws s3 ls "s3://$S3_BUCKET/$prefix/" || true; } | sort | awk '{print $4}' | sed '/^$/d')
  local count
  count=$(printf '%s\n' "$backups" | sed '/^$/d' | wc -l)
  if [ "$count" -gt "$keep" ]; then
    printf '%s\n' "$backups" | head -n "$((count - keep))" | while read -r backup; do
      echo "Deleting old backup: s3://$S3_BUCKET/$prefix/$backup"
      aws s3 rm "s3://$S3_BUCKET/$prefix/$backup"
    done
  fi
}

echo "Deleting backups beyond the $BACKUP_KEEP most recent"
prune _globals
for db in $databases; do
  prune "$db"
done

ping_healthcheck ""
