#!/bin/bash
# Dumps every database (plus roles) to S3, then prunes old backups. Any error
# aborts the run before anything is pruned, and pings BACKUP_HEALTHCHECK_URL
# (if set) with /fail so a broken backup doesn't go unnoticed.
set -euo pipefail

DB_HOST="${DB_HOST:-database}"
DB_PORT="${DB_PORT:-5432}"
# How many backups of each database to keep in S3.
BACKUP_KEEP="${BACKUP_KEEP:-14}"
# A dump smaller than this (bytes) is treated as failed.
MIN_BACKUP_BYTES="${MIN_BACKUP_BYTES:-1024}"

export PGPASSWORD="$POSTGRES_PASSWORD"

ping_healthcheck() {
  if [ -n "${BACKUP_HEALTHCHECK_URL:-}" ]; then
    wget -q -O /dev/null -T 10 "${BACKUP_HEALTHCHECK_URL}$1" || true
  fi
}
trap 'echo "Backup FAILED"; ping_healthcheck /fail' ERR

ping_healthcheck /start

TIMESTAMP=$(date +"%Y-%m-%d_%H-%M-%S")

databases=$(psql -h "$DB_HOST" -p "$DB_PORT" -U "$POSTGRES_USER" -d postgres -At \
  -c "SELECT datname FROM pg_database WHERE datname NOT IN ('template0', 'template1', 'postgres')")

# Roles and their passwords live outside every database dump.
GLOBALS_FILE="/tmp/globals_${TIMESTAMP}.sql.gz"
pg_dumpall -h "$DB_HOST" -p "$DB_PORT" -U "$POSTGRES_USER" --globals-only | gzip > "$GLOBALS_FILE"
aws s3 cp "$GLOBALS_FILE" "s3://$S3_BUCKET/_globals/$(basename "$GLOBALS_FILE")"
rm "$GLOBALS_FILE"

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
done

echo "All database backups completed and uploaded successfully"

# Only reached when every backup above succeeded.
prune() {
  local prefix="$1"
  # `aws s3 ls` lines start with the date, so a plain sort is oldest first.
  local backups
  backups=$(aws s3 ls "s3://$S3_BUCKET/$prefix/" | sort | awk '{print $4}' | sed '/^$/d')
  local count
  count=$(printf '%s\n' "$backups" | sed '/^$/d' | wc -l)
  if [ "$count" -gt "$BACKUP_KEEP" ]; then
    printf '%s\n' "$backups" | head -n "$((count - BACKUP_KEEP))" | while read -r backup; do
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
