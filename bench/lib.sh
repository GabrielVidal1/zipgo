# zipgo bench — helpers shared by run.sh and saturate.sh (sourced; they set $here).

# connload_bin: build bench/connload once and print the binary's path. Uses
# the local Go when there is one, else the golang image (the bench otherwise
# needs only Docker). Rebuilt when a source file is newer than the binary.
connload_bin() {
  local src="$here/connload" bin="$here/.bin/connload"
  if [ -x "$bin" ] && [ -z "$(find "$src" -name '*.go' -newer "$bin" 2>/dev/null)" ]; then
    echo "$bin"
    return
  fi
  mkdir -p "$here/.bin"
  if command -v go >/dev/null 2>&1; then
    (cd "$src" && CGO_ENABLED=0 go build -o "$bin" .) >&2 || return 1
  else
    echo "building connload in the golang image…" >&2
    docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -e CGO_ENABLED=0 -e GOTOOLCHAIN=local \
      -e GOFLAGS=-buildvcs=false -v "$(cd "$here/.." && pwd):/src" -w /src/bench/connload \
      golang:1.27-alpine go build -o /src/bench/.bin/connload . >&2 || return 1
  fi
  echo "$bin"
}

# client_stat: this machine's own CPU line, so a step can tell a busy load
# generator from a busy server (report.py flags a client past 80 %).
client_stat() { head -1 /proc/stat 2>/dev/null || true; }
