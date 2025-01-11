#!/bin/bash

function start() {
  docker-compose up -d
}

function down() {
  docker-compose down
}
