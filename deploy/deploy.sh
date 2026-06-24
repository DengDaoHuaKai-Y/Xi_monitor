#!/usr/bin/env sh
set -eu

docker compose pull
docker compose up -d
docker image prune -f
