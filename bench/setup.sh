#!/usr/bin/env bash
# zipgo bench — put a throwaway bench target on a zipgo host.
#
#   bench/setup.sh --host bench.example.com --target user@server:/base/domains \
#                  [--build DIR] [--pb ~/services/bench-pocketbase] [--network services_default]
#                  [-- <extra zipgo deploy flags, e.g. --no-precompress>]
#
#   --build DIR   deploy DIR (a copy of a real site's build) at https://<host>/
#   --pb DIR      also start bench/pocketbase in DIR on the server (Docker, on
#                 --network, the one zipgo is on) and point https://pb.<host>/
#                 at it with a zipgo "rewrite" (the upstream must be allowed by
#                 ZIPGO_REWRITE_ALLOW — bench-pocketbase:8090 matches
#                 *-pocketbase:8090)
#
# Prints the BENCH_KEY that bench/run.sh --bench-key needs for api-write.
# bench/teardown.sh removes all of it.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
host="" target="" build="" pbdir="" network="services_default"
while [ $# -gt 0 ]; do
  case "$1" in
    --host) host=$2; shift 2 ;;
    --target) target=$2; shift 2 ;;
    --build) build=$2; shift 2 ;;
    --pb) pbdir=$2; shift 2 ;;
    --network) network=$2; shift 2 ;;
    --) shift; break ;;
    -h|--help) sed -n '2,18p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
[ -n "$host" ] && [ -n "$target" ] || { echo "--host and --target are required" >&2; exit 2; }
server=${target%%:*}
zipgo=${ZIPGO:-zipgo}

if [ -n "$build" ]; then
  echo "== site https://$host/ ← $build"
  "$zipgo" deploy "$build" -d "$host" --ssh "$target" --no-history "$@"
fi

if [ -n "$pbdir" ]; then
  echo "== PocketBase in $server:$pbdir"
  ssh "$server" "mkdir -p $pbdir"
  rsync -a --delete --exclude=pb_data --exclude=.env "$here/pocketbase/" "$server:$pbdir/"
  # Secrets live only on the server: the write key and a superuser password
  # (bench/pocketbase/logs.sh uses it to switch request logging).
  ssh "$server" "cd $pbdir && if [ ! -f .env ]; then umask 077; \
      printf 'BENCH_KEY=%s\nBENCH_SU_PASS=%s\nBENCH_NETWORK=%s\n' \
        \$(head -c 32 /dev/urandom | base64 | tr -dc A-Za-z0-9 | head -c 24) \
        \$(head -c 32 /dev/urandom | base64 | tr -dc A-Za-z0-9 | head -c 24) $network > .env; fi; \
      [ -d pb_data ] || install -d -m 2775 pb_data; \
      docker compose up -d --quiet-pull 2>&1 | tail -2; \
      for i in \$(seq 30); do docker exec bench-pocketbase wget -qO- http://127.0.0.1:8090/api/health >/dev/null 2>&1 && break; sleep 1; done; \
      . ./.env; docker exec bench-pocketbase /usr/local/bin/pocketbase superuser upsert bench@zipgo.invalid \"\$BENCH_SU_PASS\" --dir=/pb_data >/dev/null"
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  printf '{"rewrite": "bench-pocketbase:8090"}\n' > "$tmp/.zipgoconfig.json"
  "$zipgo" deploy "$tmp" -d "pb.$host" --ssh "$target" --no-history --no-precompress 2>/dev/null \
    || "$zipgo" deploy "$tmp" -d "pb.$host" --ssh "$target" --no-history
  key=$(ssh "$server" "grep ^BENCH_KEY= $pbdir/.env | cut -d= -f2")
  echo "BENCH_KEY=$key"
fi
