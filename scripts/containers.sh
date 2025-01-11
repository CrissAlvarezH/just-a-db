#!/bin/bash
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$DIR/base.sh"


function start() {
  echo "Starting postgres container"
  docker-compose up -d database
}

function generate_env_files() {
  echo "Generating .env file"
  echo "AWS_REGION=${AWS_REGION}" > .env

  echo "Generating .db.env file"
  dbuser=$(openssl rand -base64 16)
  dbpass=$(openssl rand -base64 16)
  echo "DB_USER=${dbuser}" > .db.env
  echo "DB_PASSWORD=${dbpass}" >> .db.env
  echo "DB_NAME=just_a_db" >> .db.env
  echo "DB_HOST=localhost" >> .db.env
  echo "DB_PORT=5432" >> .db.env

  echo "S3_BUCKET=${S3_BUCKET}" >> .backup.env
}


function install_docker() {
  echo "Instaling docker and docker-compose"
  amazon-linux-extras install docker -y
  service docker start
  usermod -a -G docker ec2-user

  sudo curl -L "https://github.com/docker/compose/releases/download/v2.17.2/docker-compose-$(uname -s)-$(uname -m)" -o /usr/local/bin/docker-compose
  sudo chmod +x /usr/local/bin/docker-compose
  docker-compose --version
}


function register_backup_cron() {
  cron=$1
  if [ -z "$cron" ]; then 
    cron="2 0 * * *"
  fi

  echo "Adding cron job to run backup at '${cron}'"
  crontab -l | { cat; echo "${cron} cd /home/ec2-user/just-a-db && docker-compose run --rm backup >> /var/log/backup.log 2>&1"; } | crontab -

  echo "Add log rotationg configuration"
  echo "/var/log/backup.log {
    rotate 7
    daily
    compress
    missingok
    notifempty
    create 644 root root
  }" > /etc/logrotate.d/db-backup
}
