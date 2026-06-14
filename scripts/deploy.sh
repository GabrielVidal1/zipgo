#!/usr/bin/env bash
# Rebuild and restart the zipgo container on raspy2.
#
# The image is built locally on the host from docker/Dockerfile, which pulls
# the latest zipgo release binary from GitHub — no registry pull/push needed.
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
  docker compose build --no-cache '$SERVICE' && \
  docker compose up -d '$SERVICE' && \
  docker image prune -f >/dev/null && \
  docker compose ps '$SERVICE'"
echo "Done."
