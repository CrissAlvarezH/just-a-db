#!/bin/bash

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$DIR/base.sh"
source "$DIR/containers.sh"
source "$DIR/infra.sh"

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
  "start")
    start
    ;;
  "stop")
    down
    ;;
  *)
    echo "not supported action"
    exit 1
    ;;
esac