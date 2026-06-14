#!/usr/bin/env bash
# Update the running zipgo container on raspy2 by pulling the latest image.
#
# Assumes the multi-arch image has already been pushed (`make docker-buildx`).
# Overridable via env:
#   DEPLOY_HOST    ssh host         (default: raspy2)
#   DEPLOY_DIR     compose project  (default: /home/gabrielvidal/services)
#   DEPLOY_SERVICE compose service  (default: zipgo)
set -euo pipefail

HOST="${DEPLOY_HOST:-raspy2}"
DIR="${DEPLOY_DIR:-/home/gabrielvidal/services}"
SERVICE="${DEPLOY_SERVICE:-zipgo}"

echo "Deploying '$SERVICE' on $HOST ($DIR) ..."
ssh "$HOST" "cd '$DIR' && \
  docker compose pull '$SERVICE' && \
  docker compose up -d '$SERVICE' && \
  docker image prune -f >/dev/null && \
  docker compose ps '$SERVICE'"
echo "Done."
