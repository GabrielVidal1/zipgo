#!/usr/bin/env bash
# Deploy the zipgo.xyz landing pages to the zipgo host.
#
# Mirrors this repo's domains/<domain>/ tree to the host's domains folder via
# rsync. zipgo watches that folder and hot-reloads, so no container restart is
# needed. Only the named domain is touched — other domains on the host (e.g.
# gabvdl.xyz subdomains) are left alone.
#
# Overridable via env:
#   DEPLOY_HOST    ssh host        (default: raspy2)
#   DEPLOY_DIR     compose project (default: /home/gabrielvidal/services)
#   DEPLOY_DOMAIN  domain folder   (default: zipgo.xyz)
set -euo pipefail

HOST="${DEPLOY_HOST:-raspy2}"
DIR="${DEPLOY_DIR:-/home/gabrielvidal/services}"
DOMAIN="${DEPLOY_DOMAIN:-zipgo.xyz}"

cd "$(dirname "$0")/.."

SRC="domains/${DOMAIN}/"
DEST="${HOST}:${DIR}/domains/${DOMAIN}/"

[ -d "domains/${DOMAIN}" ] || { echo "no such domain folder: domains/${DOMAIN}" >&2; exit 1; }

# Install scripts are generated from scripts/parts/ — regenerate before syncing.
if [ -d "domains/${DOMAIN}/install." ]; then
  bash scripts/populate_script.sh "domains/${DOMAIN}/install."
fi

echo "Deploying $SRC -> $DEST ..."
rsync -az --delete --itemize-changes "$SRC" "$DEST"
echo "Done. zipgo hot-reloads on folder changes."
