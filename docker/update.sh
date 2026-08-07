#!/bin/sh
set -e

cd "$(dirname "$0")"

if ! docker compose ps --status running --services 2>/dev/null | grep -qx rags; then
  echo "error: rags container is not running; start with: docker compose up -d" >&2
  exit 1
fi

exec docker compose exec rags sh -c 'cd /data/docs && rags update'
