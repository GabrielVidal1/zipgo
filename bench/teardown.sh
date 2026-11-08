#!/usr/bin/env bash
# zipgo bench — remove what bench/setup.sh put on the server.
#
#   bench/teardown.sh --host bench.example.com --target user@server:/base/domains \
#                     [--pb ~/services/bench-pocketbase]
#
# Deletes the bench site's folder (and every subdomain under it, pb.<host>
# included) and, with --pb, stops the bench PocketBase and deletes its folder,
# data included. The host must be a subdomain: a domain's root is never removed.
set -euo pipefail

host="" target="" pbdir=""
while [ $# -gt 0 ]; do
  case "$1" in
    --host) host=$2; shift 2 ;;
    --target) target=$2; shift 2 ;;
    --pb) pbdir=$2; shift 2 ;;
    -h|--help) sed -n '2,9p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
[ -n "$host" ] && [ -n "$target" ] || { echo "--host and --target are required" >&2; exit 2; }
server=${target%%:*} base=${target#*:}

# zipgo's layout: <base>/<domain>/<sub>./<subsub>./… for sub…subsub.domain.
IFS=. read -r -a labels <<< "$host"
n=${#labels[@]}
[ "$n" -ge 3 ] || { echo "refusing: $host is not a subdomain" >&2; exit 2; }
dir="$base/${labels[n-2]}.${labels[n-1]}"
for ((i = n - 3; i >= 0; i--)); do dir="$dir/${labels[i]}."; done

if [ -n "$pbdir" ]; then
  echo "== PocketBase $server:$pbdir"
  # pb_data belongs to the container's uid: remove it from a container.
  ssh "$server" "cd $pbdir 2>/dev/null && docker compose down 2>&1 | tail -1; \
    docker run --rm --entrypoint rm -v $pbdir:/d ghcr.io/muchobien/pocketbase:0.39.3 -rf /d/pb_data; \
    rm -rf $pbdir"
fi
echo "== site $server:$dir"
ssh "$server" "rm -rf '$dir'"
