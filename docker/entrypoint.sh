#!/bin/sh
set -e

if [ -z "${SILICONFLOW_API_KEY}" ]; then
  echo "error: SILICONFLOW_API_KEY is required"
  exit 1
fi

: "${RAGS_NAME:=bk7258-docs}"
: "${RAGS_PORT:=18787}"
: "${RAGS_PUBLIC_HOST:=127.0.0.1}"
: "${QDRANT_URL:=http://qdrant:6333}"
: "${DOCS_PATH:=/data/docs}"

echo "Waiting for Qdrant at ${QDRANT_URL}..."
until wget -q -O- "${QDRANT_URL}/collections" >/dev/null 2>&1; do
  sleep 1
done
echo "Qdrant is ready."

export RAGS_NAME RAGS_PORT RAGS_PUBLIC_HOST QDRANT_URL DOCS_PATH

rags docker-init

if [ "${RAGS_AUTO_UPDATE:-0}" = "1" ]; then
  echo "Running incremental update..."
  cd "/srv/rags/${RAGS_NAME}" && rags update
fi

echo "Starting rags serve..."
exec rags serve --name "${RAGS_NAME}"
