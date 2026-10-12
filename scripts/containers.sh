#!/bin/bash

function start() {
  generate_certs

  echo "Building backups and pgbouncer containers"
  docker-compose build backups pgbouncer

  echo "Starting postgres and pgbouncer containers"
  docker-compose up -d database pgbouncer
}

function generate_env_files() {
  echo "Generating .env file"
  echo "AWS_REGION=${AWS_REGION}" > .env

  echo "Generating .db.env file"
  # Hex only: base64's '/', '+' and '=' break connection URLs that aren't
  # percent-encoded.
  dbuser=db_user_$(openssl rand -hex 4)
  dbpass=$(openssl rand -hex 32)
  echo "POSTGRES_USER=${dbuser}" > .db.env
  echo "POSTGRES_PASSWORD=${dbpass}" >> .db.env
  echo "POSTGRES_DB=just-a-db" >> .db.env

  echo "S3_BUCKET=${S3_BUCKET}" > .backup.env
  echo "# Optional: healthchecks.io-style URL, pinged on success (and /start, /fail)" >> .backup.env
  echo "BACKUP_HEALTHCHECK_URL=" >> .backup.env
}

# Self-signed certificate for pgbouncer's TLS. Clients use sslmode=require
# (encrypted, the certificate isn't verified). Kept if it already exists.
function generate_certs() {
  if [ -f certs/server.crt ] && [ -f certs/server.key ]; then
    echo "TLS certificate already exists in certs/"
    return
  fi
  echo "Generating self-signed TLS certificate in certs/"
  mkdir -p certs
  openssl req -new -x509 -days 3650 -nodes \
    -subj "/CN=just-a-db" \
    -keyout certs/server.key -out certs/server.crt
  chmod 600 certs/server.key
}


function register_backup_cron() {
  cron="$1"
  if [ -z "$cron" ]; then 
    cron="2 0 * * *"
  fi

  echo "Adding cron job to run backup at '${cron}'"
  crontab -l | { cat; echo "${cron} cd /home/ec2-user/just-a-db && /usr/local/bin/docker-compose up -d backups"; } | crontab -
}


function get_credentials() {
  ip=$(get_instance_ip)

  echo "HOST=${ip}" > credentials.txt
  echo "PORT=6432 (pgbouncer, sslmode=require) — 5432 is direct Postgres" >> credentials.txt
  ssh -o StrictHostKeyChecking=accept-new -i just-a-db.pem ec2-user@${ip} "cat /home/ec2-user/just-a-db/.db.env" >> credentials.txt
}
