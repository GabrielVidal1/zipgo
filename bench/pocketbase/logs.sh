#!/usr/bin/env bash
# zipgo bench — switch the bench PocketBase's request log on or off.
#
#   bench/pocketbase/logs.sh --api https://pb.bench.example.com --server user@host \
#                            [--pb ~/services/bench-pocketbase] [--resolve IP] on|off|status
#
# PocketBase writes a row to auxiliary.db for every request it serves, so each
# API read is also a SQLite write. "off" sets the log retention to 0 days,
# which stops it persisting them; "on" puts the default (5 days) back.
# "status" prints the retention and how many log rows there are.
set -euo pipefail

api="" server="" pbdir="~/services/bench-pocketbase" resolve="" action=""
while [ $# -gt 0 ]; do
  case "$1" in
    --api) api=${2%/}; shift 2 ;;
    --server) server=$2; shift 2 ;;
    --pb) pbdir=$2; shift 2 ;;
    --resolve) resolve=$2; shift 2 ;;
    on|off|status) action=$1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
[ -n "$api" ] && [ -n "$server" ] && [ -n "$action" ] || { sed -n '2,11p' "$0"; exit 2; }

curlx=(curl -sS --fail-with-body)
if [ -n "$resolve" ]; then
  h=${api#https://}; h=${h%%/*}
  curlx+=(--resolve "$h:443:$resolve")
fi
pass=$(ssh "$server" "grep ^BENCH_SU_PASS= $pbdir/.env | cut -d= -f2")
token=$("${curlx[@]}" -H 'Content-Type: application/json' \
  -d "{\"identity\": \"bench@zipgo.invalid\", \"password\": \"$pass\"}" \
  "$api/api/collections/_superusers/auth-with-password" | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')

case "$action" in
  on) days=5 ;;
  off) days=0 ;;
esac
if [ "$action" != status ]; then
  "${curlx[@]}" -X PATCH -H "Authorization: $token" -H 'Content-Type: application/json' \
    -d "{\"logs\": {\"maxDays\": $days}}" "$api/api/settings" >/dev/null
fi
days=$("${curlx[@]}" -H "Authorization: $token" "$api/api/settings" | python3 -c 'import json,sys; print(json.load(sys.stdin)["logs"]["maxDays"])')
rows=$("${curlx[@]}" -H "Authorization: $token" "$api/api/logs?perPage=1" | python3 -c 'import json,sys; print(json.load(sys.stdin)["totalItems"])')
echo "request log: maxDays=$days, $rows rows"
