#!/bin/bash

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$DIR/base.sh"
source "$DIR/containers.sh"

action=$1

if [ -z "$action" ]; then
  echo "Usage: $0 <action>"
  exit 1
fi

case $action in
  "start")
    start
    ;;
  "stop")
    down
    ;;
esac
