# zipgo source security audit — 2026-11-20

**Scope.** Source-level review of zipgo's custom request-handling and
config-building logic, plus a dynamic black-box probe of the auth/isolation
surface. Done as the high-yield half of the "ARTEX pentest platform" work
(`~/homelab/data/plans/2026-11-20-artex-pentest-platform.md`): zipgo is a static
file host embedding Caddy, so a black-box scanner finds little — its real
exposure is in the custom modules, which are best reviewed at the source.

**Modules reviewed**

| area | files |
| ---- | ----- |
| request pipeline | `main.go` |
| password gate (login page, sessions, throttle) | `internal/gate/{gate,page}.go` |
| Caddy config builder (routes, auth layering, rewrite proxy, file serving) | `internal/builder/{builder,admin}.go` |
| admin back-office (runtime JSON API: switch/flag/password/rollback) | `internal/admin/handler.go` |
| site config + rewrite/redirect model | `internal/sites/sites.go` |

**Method.** Code read, plus a reproducible dynamic probe
(`bench/security_probe.sh`, Docker-only) that builds the current release image,
runs it with a seeded locked site + a disallowed-upstream rewrite, and asserts
14 security properties with curl. **All 14 pass.**

---

## Verdict

**No exploitable vulnerability found in the reviewed modules.** The security
design is deliberate and holds up: unforgeable per-site sessions, a fail-closed
admin surface, layered CSRF defence, and a static file server hardened against
dotfile/symlink/path escapes. The items below are **hardening recommendations
and informational notes**, not breaks.

The one finding worth acting on is operational, not a code bug: the SSRF
rewrite allow-list is **off by default**, and the homelab's own `zipgo-test`
instance runs without it.

---

## Findings

### 1. Rewrite→reverse-proxy allow-list defaults to allow-all — `zipgo-test` runs unset *(Low / hardening)*

`internal/builder/builder.go` `UpstreamAllowed()` returns `true` for every dial
address when `ZIPGO_REWRITE_ALLOW` is unset (documented back-compat). A site's
`.zipgoconfig.json` `"rewrite"` then becomes an SSRF primitive for anyone who
can write that file: `{"rewrite":"http://169.254.169.254/"}`,
`{"rewrite":"http://<internal-host>:<port>"}`, loopback, tailnet, etc. —
whatever the server process can reach — published under a host it serves.
`proxyDial()` only parses `host:port`/scheme (no `file://` escape), so the
exposure is strictly network reachability, but on a **multi-tenant** deploy
(guests deploying their own site folders) that is the whole internal network.

- **Production (raspy2) is correctly configured**: `hosts/raspy2/zipgo-canary.sh`
  sets `ZIPGO_REWRITE_ALLOW=*-pocketbase:8090,3d-gallery:80,…` — the probe
  confirms a disallowed upstream returns **403** (`refusedUpstreamHandler`) when
  the list is set.
- **The homelab `services/zipgo` (`zipgo-test`) sets no allow-list.** It only
  serves branch-preview static builds and is Tailscale-only, so the practical
  risk is low (previews rarely use `rewrite`, and a preview author is trusted),
  but a preview's `.zipgoconfig.json` could still proxy to any address raspy2's
  network reaches.

**Recommendation.** Set `ZIPGO_REWRITE_ALLOW` on `zipgo-test` too (even an empty
allow-all-nothing list, or the handful of upstreams previews actually need).
Longer term, consider making an *unset* allow-list on a non-localhost server a
startup **warning** (or fail-closed behind a `ZIPGO_REWRITE_ALLOW=*` opt-in), so
a public deploy can't silently proxy internal services.

### 2. `__zipgo_pwd` unlock-link discloses the cleartext password to logs *(Low / informational)*

The gate accepts a site password in a GET query parameter
(`?__zipgo_pwd=<pw>`, `internal/gate/gate.go`) for kiosk/shortcut links. The
redirect strips it from the address bar, but the **initial request line still
carries the cleartext password**, and zipgo runs with
`ZIPGO_LOG_FORMAT=json` → Caddy writes the full request URI (query included) to
stdout → Loki. The password therefore lands in the access logs (and in any
upstream TCP/HTTP proxy's logs, and the browser history of the person who
clicked). It is an opt-in convenience with a documented trade-off, and the
throttle/limiter still applies.

**Recommendation.** Note the trade-off in the `zipgo password` docs (don't put
a sensitive password in a shareable link), and/or scrub `__zipgo_pwd` from the
access-log URI in the builder's log encoder config.

### 3. Per-site login throttle resets on config reload *(Informational)*

The wrong-password limiter (`internal/gate/gate.go`, `limiters sync.Map`) lives
in process memory and is rebuilt when Caddy re-provisions handlers — which zipgo
does on **every deploy** (the file watcher reload). A reload clears the
sliding-window fail counts. This is not remotely triggerable (deploys aren't an
attacker action), so it is informational: a guesser cannot force a reset. No
change recommended.

---

## What was checked and holds (positive confirmations)

- **Session cookies are unforgeable** without the server key.
  `value = "<expiry>.<HMAC>"`, HMAC keyed by
  `HMAC(serverKey, "zipgo-password-v1\0"+host+"\0"+hash)`. Per-site key (a cookie
  from site A fails on B), password rotation changes the hash → key → logs
  everyone out, and the server key lives in Caddy's data dir (not the repo).
  Comparison is constant-time (`hmac.Equal`). *Probe: forged cookie → 401.*
- **Open-redirect is blocked.** `safeNext()` rejects `//`, `/\`, non-`/` prefixes,
  control chars and the gate's own endpoints. *Probe: `next=//evil.com` →
  Location is the site home, not the external origin.*
- **The admin back-office fails closed.** `internal/builder/admin.go`
  `adminHandler()` returns a 403 refusal when a site marks `"admin"` but has no
  `"password"`, and `siteHandler()` always layers the gate in front when a
  password is present — there is no code path that serves the switch/flag/
  password/rollback JSON API ungated.
- **Admin mutations are CSRF-defended** (`sameOrigin()`): `Sec-Fetch-Site`,
  `Origin`-vs-`Host`, and a required `application/json` content type, on top of a
  `SameSite=Lax` session cookie.
- **Static serving is hardened.** Dotfiles are refused on the cleaned request
  path (`dotfileRoute`), the per-site config file and `.zipgo-versions/` are
  hidden, and symlinks whose target escapes the site are hidden
  (`escapingSymlinks`). *Probe: the site config file returns 404 even with a
  valid session.*
- **Password sites are HTTPS-only** regardless of `allowHttp` (`httpAllowed()`),
  and get `Cache-Control: private, no-cache` + `X-Robots-Tag: noindex`.

## Dynamic probe

`bench/security_probe.sh` (committed) — Docker-only black-box regression test
covering all of the above against the current release image. Result on
2026-11-20: **14 passed, 0 failed** (zipgo v1.14.1).

```
locked apex unauthenticated → 401 · no secret body leaked · login page served
forged session cookie rejected → 401 · wrong password → 401
correct password → 303 + cookie · valid session → 200 + content
open redirect blocked · unlock-link wrong → 401 / right → 303
config file hidden (404) · disallowed rewrite upstream → 403
```

## Actions

- [ ] Set `ZIPGO_REWRITE_ALLOW` on homelab `services/zipgo` (`zipgo-test`) — finding 1.
- [ ] (upstream) Consider a startup warning when the allow-list is unset on a non-localhost server — finding 1.
- [ ] (upstream, optional) Scrub `__zipgo_pwd` from access-log URIs; document the link trade-off — finding 2.
- [x] Dynamic security regression probe added (`bench/security_probe.sh`).
