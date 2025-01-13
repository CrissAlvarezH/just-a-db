#!/bin/bash
set -e

DB_HOST="database"
DB_PORT="5432"

# Backup file name and timestamp
TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
BACKUP_FILE="backup_all_databases_$TIMESTAMP.sql.gz"

# pg_dumpall needs the password to be exported
export PGPASSWORD="$POSTGRES_PASSWORD"

echo "Starting backup of all databases on the Postgres server..."

echo "generated backup: /tmp/$BACKUP_FILE"
pg_dumpall -h "$DB_HOST" -p "$DB_PORT" -U "$POSTGRES_USER" 2>/dev/null | gzip > "/tmp/$BACKUP_FILE"

s3_path="s3://$S3_BUCKET/$BACKUP_FILE"
echo "Uploading the file to S3 in $s3_path"
aws s3 cp "/tmp/$BACKUP_FILE" "$s3_path"

echo "Backup completed and uploaded successfully to $s3_path"
