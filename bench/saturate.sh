#!/usr/bin/env bash
# zipgo bench — the ceiling: a closed loop of N clients on one URL (k6/saturate.js),
# for each N, with the same server samples as run.sh. Appends to <run>/sat.jsonl,
# which bench/report.py shows as that run's "Ceiling" table.
#
#   bench/saturate.sh --into bench/results/<run> --url https://bench.example.com/vite.svg \
#                     [--resolve IP] [--ssh user@host] [--vus "16 64 256"] [--new-conn] [--step 20s]
#                     [--verify] [--k6]
#
# --new-conn runs connload (bench/connload: a new TLS connection per request,
# ~0.2 ms of client CPU each) — k6 can't open them as fast as a server takes
# them; --k6 uses k6 anyway. Certificates aren't verified unless --verify
# (client CPU, not server work). This machine's CPU is sampled too: the report
# marks a step whose client was past 80 % as client-bound.
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
. "$here/lib.sh"
into="" url="" resolve="" ssh_host="" vus="16 64 256" newconn=0 step="20s" verify=0 usek6=0
iface="${BENCH_IFACE:-eth0}"
while [ $# -gt 0 ]; do
  case "$1" in
    --into) into=$2; shift 2 ;; --url) url=$2; shift 2 ;; --resolve) resolve=$2; shift 2 ;;
    --ssh) ssh_host=$2; shift 2 ;; --vus) vus=$2; shift 2 ;; --new-conn) newconn=1; shift ;;
    --step) step=$2; shift 2 ;; --verify) verify=1; shift ;; --k6) usek6=1; shift ;;
    -h|--help) sed -n '2,14p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
[ -n "$into" ] && [ -n "$url" ] || { echo "--into and --url are required" >&2; exit 2; }
mkdir -p "$into"
sshc=(ssh -o ConnectTimeout=10 -o ControlMaster=auto -o ControlPath="${XDG_RUNTIME_DIR:-/tmp}/zipgo-bench-%C" -o ControlPersist=120)
probe() { "${sshc[@]}" "$ssh_host" "head -1 /proc/stat; grep ' $iface:' /proc/net/dev; cat /sys/class/net/$iface/queues/tx-*/tx_timeout 2>/dev/null | awk '{s+=\$1} END {print s+0}'" 2>/dev/null || true; }
kind=$([ "$newconn" = 1 ] && echo handshake || echo keepalive)
step_s=${step%s}
connload=""
[ "$newconn" = 1 ] && [ "$usek6" = 0 ] && connload=$(connload_bin)
for n in $vus; do
  sdir="$into/sat-$kind-$n"; mkdir -p "$sdir"
  [ -n "$ssh_host" ] && probe > "$sdir/before.txt"
  [ -n "$ssh_host" ] && { ( sleep $((step_s / 2)); "${sshc[@]}" "$ssh_host" "docker stats --no-stream --format '{{json .}}'" > "$sdir/docker.jsonl" 2>/dev/null || true ) & }
  client_stat > "$sdir/client-before.txt"
  t0=$(date +%s.%N)
  if [ "$newconn" = 1 ] && [ "$usek6" = 0 ]; then
    "$connload" -url "$url" ${resolve:+-resolve "$resolve"} -c "$n" -d "$step" \
      -json "$sdir/connload.json" $([ $verify = 1 ] && echo -verify) > "$sdir/connload.log" 2>&1 || true
  else
    docker run --rm --network host -u "$(id -u):$(id -g)" -v "$here/k6:/scripts:ro" -v "$sdir:/out" \
      -e URL="$url" -e VUS="$n" -e DURATION="$step" -e RESOLVE="$resolve" -e NEW_CONN="$newconn" \
      -e INSECURE="$((1 - verify))" grafana/k6:latest run -q --no-color /scripts/saturate.js > "$sdir/k6.log" 2>&1 || true
  fi
  t1=$(date +%s.%N)
  client_stat > "$sdir/client-after.txt"
  [ -n "$ssh_host" ] && probe > "$sdir/after.txt"
  wait 2>/dev/null || true
  line=$(python3 -I "$here/report.py" step --dir "$sdir" --scenario "sat-$kind" --rate "$n" \
    --seconds "$(awk -v a="$t0" -v b="$t1" 'BEGIN {print b - a}')" --budget 1e9 --step-seconds 0)
  echo "$line" >> "$into/sat.jsonl"
  python3 -I "$here/report.py" satline --json "$line"
  sleep 5
done
