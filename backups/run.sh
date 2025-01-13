#!/bin/bash
set -e

DB_HOST="database"
DB_PORT="5432"

export PGPASSWORD="$POSTGRES_PASSWORD"

TIMESTAMP=$(date +"%Y-%m-%d_%H-%M-%S")

databases=$(psql -h "$DB_HOST" -p "$DB_PORT" -U "$POSTGRES_USER" -d postgres -t -c "SELECT datname FROM pg_database WHERE datname NOT IN ('template0', 'template1', 'postgres')")

# Backup each database
for db in $databases; do
  BACKUP_FILE="backup_${db}_${TIMESTAMP}.sql.gz"
  echo "Backing up database: $db to /tmp/$BACKUP_FILE"
  pg_dump -h "$DB_HOST" -p "$DB_PORT" -U "$POSTGRES_USER" "$db" 2>/dev/null | gzip > "/tmp/$BACKUP_FILE"
  
  s3_path="s3://$S3_BUCKET/$db/$BACKUP_FILE"
  echo "Uploading $db backup to S3 in $s3_path"
  aws s3 cp "/tmp/$BACKUP_FILE" "$s3_path"

  rm "/tmp/$BACKUP_FILE"
done

echo "All database backups completed and uploaded successfully"

echo "Deleting old backups"

# Keep only the 3 most recent backups for each database
for db in $databases; do
  # List all backups for this database, sorted by date (oldest first)
  backups=$(aws s3 ls "s3://$S3_BUCKET/$db/" | sort | awk '{print $4}')
  backup_count=$(echo "$backups" | wc -l)

  # If we have more than 3 backups, delete the oldest ones
  if [ "$backup_count" -gt 3 ]; then
    # Calculate how many backups to delete
    delete_count=$((backup_count - 3))
    
    # Get the oldest backups that need to be deleted
    to_delete=$(echo "$backups" | head -n "$delete_count")
    
    # Delete each old backup
    for backup in $to_delete; do
      echo "Deleting old backup: s3://$S3_BUCKET/$db/$backup"
      aws s3 rm "s3://$S3_BUCKET/$db/$backup"
    done
  fi
done
