#!/bin/bash

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$DIR/base.sh"
source "$DIR/infra.sh"
source "$DIR/containers.sh"

action=$1

if [ -z "$action" ]; then
  echo "Usage: $0 <action>"
  exit 1
fi

case $action in
  "setup")
    setup_stack
    ;;
  "status")
    get_stack_status
    ;;
  "destroy")
    destroy_stack
    ;;
  "connect")
    connect_to_instance
    ;;
  "user-data")
    get_user_data_output
    ;;
  "install-docker")
    install_docker
    ;;
  "register-backup-cron")
    register_backup_cron "$2"
    ;;
  "generate-env-files")
    generate_env_files
    ;;
  "start")
    start
    ;;
  "credentials")
    get_credentials
    ;;
  *)
    echo "not supported action"
    exit 1
    ;;
esac