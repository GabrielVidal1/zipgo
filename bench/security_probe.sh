#!/usr/bin/env bash
# zipgo security probe — black-box regression checks for the auth/isolation
# surface the source audit (docs/security-audit-2026-11-20.md) reviewed:
#   - the password gate: no content leak unauthenticated, forged cookies
#     rejected, correct password works, open-redirect blocked, the
#     __zipgo_pwd unlock-link path
#   - static hardening: the per-site config file / dotfiles are hidden
#   - the rewrite allow-list: an upstream outside ZIPGO_REWRITE_ALLOW → 403
#
# Docker-only (like the rest of bench/). Builds the zipgo image, runs it in
# localhost mode (one plain-HTTP port, so the gate is testable without TLS),
# and drives it with curl from a throwaway container sharing zipgo's network
# namespace (localhost mode binds the container loopback, so published ports
# don't reach it — a netns-shared client does). Exits non-zero on any failure.
#
#   bench/security_probe.sh
#   ZIPGO_IMAGE=zipgo:local bench/security_probe.sh   # reuse an existing image
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"
IMAGE="${ZIPGO_IMAGE:-zipgo:probe}"
CURL_IMAGE="${CURL_IMAGE:-curlimages/curl:8.11.1}"
NAME="zipgo-security-probe-$$"
PW="s3cret-probe-pw"

pass=0 fail=0
ok()   { printf '  \033[32m✓\033[0m %s\n' "$1"; pass=$((pass+1)); }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$1"; fail=$((fail+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1 ($3)"; else bad "$1 — expected [$2] got [$3]"; fi; }

seed=""
cleanup(){ docker rm -f "$NAME" >/dev/null 2>&1 || true; [ -n "$seed" ] && rm -rf "$seed"; }
trap cleanup EXIT

# curl inside zipgo's netns (reaches 127.0.0.1:9000, the localhost-mode bind).
cq(){ docker run --rm --network "container:$NAME" "$CURL_IMAGE" -s "$@"; }

# --- build image if missing ---------------------------------------------------
if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  echo "building $IMAGE from docker/ (fetches the release binary)…"
  docker build -t "$IMAGE" "$root/docker" >/dev/null || { echo "build failed"; exit 1; }
fi
docker pull -q "$CURL_IMAGE" >/dev/null 2>&1 || true

# --- a bcrypt hash of $PW -----------------------------------------------------
if command -v htpasswd >/dev/null 2>&1; then
  HASH="$(htpasswd -bnBC 10 "" "$PW" | tr -d ':\n')"
else
  echo "need htpasswd (apache2-utils) to hash the probe password" >&2; exit 1
fi

# --- seed a throwaway domains folder -----------------------------------------
seed="$(mktemp -d)"; chmod 755 "$seed"
mkdir -p "$seed/probe.test/open." "$seed/probe.test/ssrf."
printf '{ "password": { "hash": %s, "title": "Probe" } }\n' "\"$HASH\"" > "$seed/probe.test/.zipgoconfig.json"
printf '<!doctype html><title>secret</title>FLAG_SECRET_BODY\n' > "$seed/probe.test/index.html"
printf '<!doctype html><title>open</title>OPEN_BODY\n' > "$seed/probe.test/open./index.html"
printf '{ "allowHttp": true }\n' > "$seed/probe.test/open./.zipgoconfig.json"
printf '{ "rewrite": "http://169.254.169.254:80" }\n' > "$seed/probe.test/ssrf./.zipgoconfig.json"

# --- run zipgo in localhost mode ---------------------------------------------
docker run -d --name "$NAME" \
  -e ZIPGO_LOCALHOST=1 \
  -e ZIPGO_REWRITE_ALLOW='open.probe.test:80,pocketbase-target:8090' \
  -v "$seed:/domains:ro" \
  "$IMAGE" >/dev/null
B="http://127.0.0.1:9000/probe.test"
for _ in $(seq 1 40); do
  code="$(cq -o /dev/null -w '%{http_code}' "$B/open/" 2>/dev/null || echo 000)"
  [ "$code" != "000" ] && break; sleep 0.5
done
echo "zipgo security probe → $B (via netns curl)"

# 1. locked apex, no session → 401, login page, no secret body
body="$(cq -w '\n%{http_code}' "$B/")"
code="${body##*$'\n'}"; html="${body%$'\n'*}"
check "locked apex unauthenticated → 401" "401" "$code"
case "$html" in *FLAG_SECRET_BODY*) bad "secret body leaked unauthenticated";; *) ok "no secret body leaked";; esac
case "$html" in *password*|*Password*) ok "login page served";; *) bad "login page not served";; esac

# 2. forged cookie (valid shape, bogus MAC) → still 401
check "forged session cookie rejected → 401" "401" \
  "$(cq -o /dev/null -w '%{http_code}' -H 'Cookie: zipgo_session=99999999999.AAAABBBBCCCCDDDD' "$B/")"

# 3. wrong password → 401
check "wrong password → 401" "401" \
  "$(cq -o /dev/null -w '%{http_code}' -X POST --data "password=nope&next=/probe.test/" "$B/__zipgo/login")"

# 4. correct password → 303 + Set-Cookie
hdr="$(cq -D - -o /dev/null -X POST --data "password=$PW&next=/probe.test/" "$B/__zipgo/login")"
case "$hdr" in *' 303'*) ok "correct password → 303";; *) bad "correct password not 303";; esac
cookie="$(printf '%s' "$hdr" | grep -i '^set-cookie:' | sed -E 's/.*(zipgo_session=[^;]*).*/\1/' | tr -d '\r')"
[ -n "$cookie" ] && ok "session cookie minted" || bad "no session cookie minted"

# 5. reuse the session cookie → 200 + secret body
body="$(cq -w '\n%{http_code}' -H "Cookie: $cookie" "$B/")"
code="${body##*$'\n'}"; html="${body%$'\n'*}"
check "valid session → 200" "200" "$code"
case "$html" in *FLAG_SECRET_BODY*) ok "content served with session";; *) bad "content not served with session";; esac

# 6. open-redirect: next=//evil.com must NOT become the Location
loc="$(cq -D - -o /dev/null -X POST --data "password=$PW&next=//evil.com" "$B/__zipgo/login" | grep -i '^location:' | tr -d '\r' | awk '{print $2}')"
case "$loc" in //evil.com|http://evil.com|https://evil.com) bad "open redirect: Location=$loc";; *) ok "open redirect blocked (Location=$loc)";; esac

# 7. __zipgo_pwd unlock link: wrong → 401, right → 303
check "unlock-link wrong password → 401" "401" "$(cq -o /dev/null -w '%{http_code}' "$B/?__zipgo_pwd=nope")"
check "unlock-link correct password → 303" "303" "$(cq -o /dev/null -w '%{http_code}' "$B/?__zipgo_pwd=$PW")"

# 8. dotfile / config file hidden on the open static site
code="$(cq -o /dev/null -w '%{http_code}' "$B/open/.zipgoconfig.json")"
case "$code" in 403|404) ok "config file hidden ($code)";; *) bad "config file exposed ($code)";; esac

# 9. SSRF allow-list: ssrf. sub rewrites to a disallowed upstream → 403
body="$(cq -w '\n%{http_code}' "$B/ssrf/")"
code="${body##*$'\n'}"
check "disallowed rewrite upstream → 403" "403" "$code"

echo
echo "security probe: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
