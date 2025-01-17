#!/bin/bash

function start() {
  echo "Building backups container"
  docker-compose build backups

  echo "Starting postgres container"
  docker-compose up -d database
}

function generate_env_files() {
  echo "Generating .env file"
  echo "AWS_REGION=${AWS_REGION}" > .env

  echo "Generating .db.env file"
  dbuser=db-user-$(openssl rand -base64 5 | tr -dc 'a-zA-Z0-9')
  dbpass=$(openssl rand -base64 25)
  echo "POSTGRES_USER=${dbuser}" > .db.env
  echo "POSTGRES_PASSWORD=${dbpass}" >> .db.env
  echo "POSTGRES_DB=just-a-db" >> .db.env
 
  echo "S3_BUCKET=${S3_BUCKET}" >> .backup.env
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
  echo "PORT=5432" >> credentials.txt
  ssh -o StrictHostKeyChecking=no -i just-a-db.pem ec2-user@${ip} "cat /home/ec2-user/just-a-db/.db.env" >> credentials.txt
}
