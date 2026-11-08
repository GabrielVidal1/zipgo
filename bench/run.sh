#!/usr/bin/env bash
# zipgo bench — step the load up on a zipgo host until it stops keeping up.
#
#   bench/run.sh --site https://bench.example.com/ [--api https://pb.bench.example.com]
#                [--resolve 192.168.1.131] [--ssh user@host] [--label baseline]
#                [--scenarios static,firstvisit,handshake,api-read,api-write,mixed]
#                [--step 30s] [--out bench/results] [--bench-key KEY] [--rates "5 10"]
#
# Each scenario runs at a rising arrival rate (the ladders below), one k6 run
# per step (Docker image grafana/k6, host network), and stops at the first step
# that breaks a limit: > 1 % failed requests, > 2 % of arrivals k6 could not
# even start (dropped: every VU stuck waiting on the server), or a p95 over the
# scenario's budget. With --ssh, every step also samples the server: CPU
# (busy / softirq), NIC throughput and TX-timeout counter, and per-container
# CPU, memory and pids (docker stats, mid-step).
#
# Results: one JSON line per step in <out>/<label>-<stamp>/steps.jsonl, and a
# Markdown report next to it (bench/report.py, which also compares runs).
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
site="" api="" resolve="" ssh_host="" label="run" step="30s" out="$here/results" key="" rates=""
scenarios="static,firstvisit,handshake,api-read,api-write,mixed"
iface="${BENCH_IFACE:-eth0}"

while [ $# -gt 0 ]; do
  case "$1" in
    --site) site=$2; shift 2 ;;
    --api) api=$2; shift 2 ;;
    --resolve) resolve=$2; shift 2 ;;
    --ssh) ssh_host=$2; shift 2 ;;
    --label) label=$2; shift 2 ;;
    --scenarios) scenarios=$2; shift 2 ;;
    --step) step=$2; shift 2 ;;
    --out) out=$2; shift 2 ;;
    --bench-key) key=$2; shift 2 ;;
    --rates) rates=$2; shift 2 ;; # one ladder for every scenario (smoke tests)
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
[ -n "$site" ] || { echo "--site is required" >&2; exit 2; }

# Rates (arrivals per second) per scenario. A visitor = the page + its assets.
declare -A ladder=(
  [static]="200 400 800 1600 3200 6400 12800"
  [handshake]="50 100 200 400 800 1600 3200"
  [firstvisit]="10 25 50 75 100 150 200 300 400"
  [api-read]="50 100 200 400 800 1600 3200"
  [api-write]="10 25 50 100 200 400 800"
  [mixed]="10 25 50 75 100 150 200 300"
)
# p95 budget (ms): a whole page load for firstvisit, one request otherwise.
declare -A budget=([static]=1000 [handshake]=1000 [firstvisit]=2000 [api-read]=1000 [api-write]=1000 [mixed]=1000)

dir="$out/$label-$(date +%Y%m%d-%H%M)"
mkdir -p "$dir"
echo "→ $dir"

# One SSH connection for every sample (a socket path must stay under 108 bytes).
sshc=(ssh -o ConnectTimeout=10 -o ControlMaster=auto -o ControlPath="${XDG_RUNTIME_DIR:-/tmp}/zipgo-bench-%C" -o ControlPersist=120)
probe() { # cpu line, NIC counters, TX timeouts — one snapshot
  "${sshc[@]}" "$ssh_host" "head -1 /proc/stat; grep ' $iface:' /proc/net/dev; cat /sys/class/net/$iface/queues/tx-*/tx_timeout 2>/dev/null | awk '{s+=\$1} END {print s+0}'" 2>/dev/null || true
}
containers() {
  "${sshc[@]}" "$ssh_host" "docker stats --no-stream --format '{{json .}}'" 2>/dev/null || true
}

{
  echo "{\"label\": \"$label\", \"site\": \"$site\", \"api\": \"$api\", \"resolve\": \"$resolve\", \"step\": \"$step\", \"started\": \"$(date -Is)\"}"
} > "$dir/run.json"

step_s=${step%s}
IFS=, read -r -a list <<< "$scenarios"
for sc in "${list[@]}"; do
  [ -n "${ladder[$sc]:-}" ] || { echo "unknown scenario $sc" >&2; exit 2; }
  case "$sc" in api-*|mixed) [ -n "$api" ] || { echo "skip $sc: no --api"; continue; } ;; esac
  for rate in ${rates:-${ladder[$sc]}}; do
    sdir="$dir/$sc-$rate"
    mkdir -p "$sdir"
    [ -n "$ssh_host" ] && probe > "$sdir/before.txt"
    if [ -n "$ssh_host" ]; then
      ( sleep $((step_s / 2)); containers > "$sdir/docker.jsonl" ) &
      sampler=$!
    fi
    t0=$(date +%s.%N)
    docker run --rm --network host -u "$(id -u):$(id -g)" \
      -v "$here/k6:/scripts:ro" -v "$sdir:/out" \
      -e SCENARIO="$sc" -e RATE="$rate" -e DURATION="$step" -e SITE="$site" -e API="$api" \
      -e RESOLVE="$resolve" -e BENCH_KEY="$key" -e STATIC_PATH="${BENCH_STATIC_PATH:-/vite.svg}" \
      grafana/k6:latest run -q --no-color /scripts/scenarios.js > "$sdir/k6.log" 2>&1 || true
    t1=$(date +%s.%N)
    [ -n "$ssh_host" ] && probe > "$sdir/after.txt"
    [ -n "${sampler:-}" ] && wait "$sampler" 2>/dev/null || true
    line=$(python3 -I "$here/report.py" step --dir "$sdir" --scenario "$sc" --rate "$rate" \
      --seconds "$(awk -v a="$t0" -v b="$t1" 'BEGIN {print b - a}')" --budget "${budget[$sc]}" \
      --step-seconds "$step_s")
    echo "$line" >> "$dir/steps.jsonl"
    python3 -I "$here/report.py" line --json "$line"
    if python3 -I -c 'import json,sys; sys.exit(0 if json.loads(sys.argv[1])["ok"] else 1)' "$line"; then
      sleep 5
    else
      echo "  ↳ $sc stops here"
      sleep 10
      break
    fi
  done
done

python3 -I "$here/report.py" report "$dir" > "$dir/report.md"
echo "report: $dir/report.md"
