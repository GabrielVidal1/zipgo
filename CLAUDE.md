# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

**zipgo** — a minimal static site host written in Go. It embeds Caddy as a library, discovers domains and subdomains from a local domains folder (default `.zipgo/`), and serves them over HTTPS (domain mode) or HTTP on a single localhost port (localhost mode).

Where this is going is defined in [GOAL.md](GOAL.md) — read it before proposing features.

## Commands

```bash
make build          # Compile binary (also regenerates install scripts)
make run-local      # Build + run on localhost (no sudo, no domain needed)
make run            # Build + run with real domain (needs sudo, ports 80/443)
make run-prod       # setcap + run in domain mode
make format         # Run gofmt
make clean          # Remove binary
make build-install-scripts  # Regenerate install/{linux,macos,windows}.sh from parts
```

CLI subcommands: `zipgo serve` (default), `zipgo deploy` (rsync a local dir to a remote zipgo host over SSH, creating the trailing-dot subdomain folder tree — see `internal/deploy`), `zipgo ls`/`zipgo info` (read-only SSH inspection of what's deployed on the remote target, `--json` for machine-readable output — see `internal/remote`), `zipgo doctor` (lint the local domains folder — see `internal/doctor`), `zipgo enable|disable|status` (systemd user service), `zipgo help`.

### Remote inspection (`internal/remote`)
`ls`/`info` shell out to `ssh` (GNU coreutils assumed on the remote, as the existing `find -printf`/`date -r` calls already did). `siteDirs` finds every folder under the base and keeps the ones `deploy.HostFromRemote` maps back to a host; `stat` then runs **one** generated script (`statScript`) over exactly those folders, printing a tab-separated record per site that `parseSites` (pure, table-tested) turns into a `Site`. `Site` is the single source of truth for both the human output and `--json`: `Type` (`static`/`spa`/`proxy`/`redirect`), `Enabled` and `Protected` are recomputed the way `internal/builder` routes the site — SPA = `index.html` + a bundle dir, proxy = a `.zipgoconfig.json` `rewrite`, redirect = a `redirect`, protected = a `basicAuth` (reported as a bool; the hashes are never emitted) — so keep `bundleDirs` in sync with `internal/sites`, and an unreadable config surfaces as `ConfigError` rather than being dropped. `Size`/`Files` **prune nested trailing-dot folders** (they are sites of their own), so an apex is not the sum of its whole domain. `--json` shapes: `ls` → `[]Site`, `ls <host>` → `[]Entry` (that folder's children), `info <host>` → one `Site`.

### Config-driven deploy (`internal/zipconfig`)
`zipgo deploy`, `ls` and `info` resolve their target and source folders from two config files so they don't have to be repeated (`internal/zipconfig`, both found by **ascending** the dir tree from the cwd):
- **Root** `.zipgo.json` → `{ "target": "user@host:/base", "targets": {"name": "..."} }` — the default `--ssh` destination.
- **Project** `package.json` `"zipgo"` field → `{ "deploy": {"<host>": "<srcDir>"}, "target": "<override>" }` — maps each host to its local build folder (paths relative to package.json).

`deploy.ParseArgs` is **lenient** (src/-d/--ssh all optional); `deploy.Resolve(&opts, proj, root)` is the pure function that fills `opts.SSH` + `opts.Jobs []Job{Host,Src}` from flags+config and validates, then `deploy.Run` iterates the jobs. Precedence: `--ssh` > project `target` > root `target`; `-d` hosts > all map keys; positional dir > map entry. The fully-explicit `dir -d host --ssh …` form is unchanged (back-compat). `deploy.HostFromRemote` is the inverse of `RemoteDir` (folder path → host), used by `ls`.

## Architecture

### Discovery model
The folder tree *is* the routing table. Inside the domains folder, each subfolder whose name contains a dot is a **domain** (others are ignored). Within a domain folder:
- the folder root itself is the **apex** site (`yourdomain.com`)
- any subfolder whose name **ends in a dot** is a **subdomain** — `docs.` → `docs.yourdomain.com` — applied **recursively** (`docs./api.` → `api.docs.yourdomain.com`). Multi-dot names map directly (`foo.bar.` → `foo.bar.yourdomain.com`).
- folders that don't end in a dot are ordinary content of their parent site (served as paths); subdomain folders are hidden from the apex's directory listing via a `*.` file_server hide.

### Startup flow (`main.go`)
1. Resolves the domains folder from `ZIPGO_DOMAINS_FOLDER` (default `.zipgo`); `config.ReadDomains` lists valid domain folders
2. No domains, or `ZIPGO_LOCALHOST=1` → localhost mode; otherwise domain mode
3. Calls `sites.Discover()` per domain + `builder.Build*Config()` to generate a Caddy JSON config in memory
4. Calls `caddy.Run(cfg)` — no Caddyfile on disk, config is entirely in-memory
5. Watches the domains folder; on filesystem changes it calls `reload()` which re-runs steps 3–4

### Internal packages
- **`internal/sites`** — recursively walks a domain folder; `Site` holds a leaf-first `Labels` chain (empty = apex). Helpers: `Host(root)`, `IsApex()`, `LocalhostPath(domain)`. Detects SPAs when `index.html` + one of `assets/`, `static/`, `_next/`, `dist/` is present
- **`internal/builder`** — constructs the Caddy JSON config; `BuildLocalhostConfig` for HTTP-only path routing, `BuildConfig` for HTTPS host routing. TLS subjects are exact hosts (correct for nested/multi-dot subdomains)
- **`internal/config`** — `ReadDomains` scans the domains folder for valid domain subfolders (must contain a dot; malformed are skipped)
- **`internal/service`** — systemd user-service install/remove/status
- **`internal/doctor`** — backs `zipgo doctor`: lints the domains folder and returns a `Report` of `Finding{Level, Host, Path, Msg, Hint}` (`Warn`/`Error`; `Report.OK(strict)` drives the exit code). It deliberately **re-walks the tree instead of calling `sites.Discover`**: Discover is a hard-fail parser (one malformed `.zipgoconfig.json` aborts the whole reload), whereas doctor must keep going and report *every* problem in one pass. Checks: missing `index.html` (Error, downgraded to Warn when the folder only holds subdomains — that's the legitimate `gabvdl.xyz/game./` shape), malformed `.zipgoconfig.json`, unknown config keys (a typo'd `"enabled"` is silently ignored by the real parser), invalid domain/subdomain folder names, a dot-containing folder that forgot its trailing dot, an unusable `rewrite` upstream, an unusable `redirect` target / `redirectStatus` (including `redirect` combined with `rewrite`, or a redirect over a folder that still has an `index.html`), a `basicAuth` password that isn't a bcrypt hash (`invalidBcrypt` — a plaintext value is an Error: Caddy only ever compares hashes, so it locks everyone out *and* commits a secret), and two folders resolving to the same host. Keep `knownConfigKeys` **and** `knownKeyList` in sync when adding a `.zipgoconfig.json` key.

### Subdomain metadata endpoint
Any site that has at least one direct child subdomain (the apex, or a subdomain with nested subdomains) serves a JSON listing at `<host>/sub-domains-meta` (localhost: `/<domain>/<path>/sub-domains-meta`). It maps each direct child's folder name (e.g. `"docs."`) to metadata extracted from that child's `index.html` — `title`, `description`, OpenGraph (`og:*`) tags, favicon `icon`, and custom `zipgo:*` meta properties (exposed under `zipgo`, keyed by the part after the prefix). The JSON is computed at config-build time in `internal/builder` (`childMeta`/`metaRoute`) using `internal/meta` and embedded as a Caddy `static_response`, so it refreshes on every watcher reload. Children one level down only; leaf sites get no endpoint (404).

### Per-site config (`.zipgoconfig.json`)
Any site folder may contain a `.zipgoconfig.json` read at discovery time into `sites.Config` (`internal/sites`, `readConfig`) and attached to each `Site`. A missing file is the zero value (defaults); a malformed file is a hard error so typos surface. Pointer `*bool` fields distinguish unset from explicit false. Keys: `enable` (false → site is dropped from routing/TLS and from any parent's `sub-domains-meta`; gated by `builder.served`/`childMeta`), `rewrite` (non-empty → the site is a `reverse_proxy` to that upstream instead of a `file_server`; `builder.proxyHandler`/`proxyDial` turn a `host:port` or scheme URL into a Caddy dial address + TLS transport), `redirect` + `redirectStatus` (non-empty → the site is a `static_response` redirect instead of a `file_server`; `builder.redirectHandler`/`redirectLocation` append `{http.request.uri}` when the target is a bare origin so paths and queries survive, and use the target verbatim when it carries a path; the status defaults to `sites.DefaultRedirectStatus` = 302), `allowHttp` (true → the host is also served on `:80` via `httpRoutes` instead of the catch-all HTTPS redirect; no effect in localhost mode), `headers` (a name → value map merged into the security-headers handler — see below), and `basicAuth` (a `username → bcrypt hash` map → the site is served behind HTTP basic auth).

`builder.unguardedSiteHandler` chooses redirect vs proxy vs file (in that precedence) and hides `.zipgoconfig.json` from the file_server; `builder.siteHandler` then wraps *whatever that produced* in `basicAuthHandler`'s Caddy `authentication` handler, so the credential check runs before files, SPA fallback, a proxied upstream or a redirect are reached, and `builder.guard` prepends the same handler to a protected site's `sub-domains-meta` route so it can't leak the child listing of a password-protected site. Basic auth is per-site: a child subdomain is a separate site and needs its own `basicAuth`. The `authentication` handler is only registered because `main.go` imports `caddyhttp/caddyauth` — Caddy rejects the whole config at load time if that import is dropped. The file watcher hot-reloads config edits like any other change.

**When adding a key**, three places must move together: the `sites.Config` struct, `doctor.knownConfigKeys` (+ its `knownKeyList` hint — an unlisted key is reported as an ignored typo), and the README's per-site-config section.

### Framing / clickjacking policy (`zipgo:authorized-origins`)
Every static route gets a security-headers handler (`securityHeaders` in `internal/builder`). By default it sends `X-Frame-Options: SAMEORIGIN`, so a page cannot be embedded in a frame from another origin. A site can opt into cross-origin embedding by declaring `<meta property="zipgo:authorized-origins" content="...">` in its `index.html`: the builder (`authorizedOrigins`) reads it and, when present, drops `X-Frame-Options` and instead emits `Content-Security-Policy: frame-ancestors 'self' <content>`. The content is a space-separated list of CSP `frame-ancestors` source expressions (host patterns supporting `*` wildcards), e.g. `https://*.gabvdl.xyz` (only that subdomain tree) or `https:` (any HTTPS origin). Note: CSP `frame-ancestors` uses host-pattern source expressions, not arbitrary regex — Caddy emits the header statically and the browser matches the embedding origin against it.

### Custom response headers (`.zipgoconfig.json` `headers`)
The same `securityHeaders(frameAncestors, custom)` handler carries the site's `headers` map, so custom headers apply to **file and proxy routes alike** (`fileHandler`/`proxyHandler` both pass `s.Config.Headers`). The map is *merged into* the defaults, never a replacement: each entry adds a header, or overrides the one default of that name and leaves the others in place. Two rules matter and both are load-bearing:
- **Case-insensitive merge** (`headerKey`): a custom name is matched against the existing keys with `EqualFold` and reuses that key's spelling, falling back to `http.CanonicalHeaderKey`. Canonicalising blindly would be a bug — Go's canonical form of `X-XSS-Protection` is `X-Xss-Protection`, so a site overriding it would land on a *second* map key and Caddy would send the header twice.
- **Empty value = delete**: the header is dropped from the `set` map *and* added to the handler's `delete` list, so it also removes one the file_server or upstream produced.

`sites.ValidateHeaders` (used by both `readConfig` — a hard error, like any malformed config — and `doctor.checkHeaders`) rejects non-token names, values with CR/LF (response-splitting), and two entries differing only in case (which header wins would otherwise depend on Go's map iteration order, making the emitted config unstable across reloads).

### Site routing
- **Domain mode**: `Host()` (e.g. `api.docs.rootDomain`) → that folder
- **Localhost mode**: single port `9000` with path routing `LocalhostPath()` = `/<domain>/<outer>/.../<leaf>` (trailing dots stripped). Routes sorted deepest-first so nested paths win

### Install scripts
The install scripts (`domains/zipgo.xyz/install/{linux,macos,windows}.sh`) are **generated files** — do not edit them directly. Edit the parts in `scripts/parts/` and regenerate with `make build-install-scripts` (or `make build`).

## Environment Variables

| Variable               | Default  | Description                                        |
|------------------------|----------|----------------------------------------------------|
| `ZIPGO_DOMAINS_FOLDER` | `.zipgo` | Folder scanned for domain subfolders               |
| `ZIPGO_LOCALHOST`      | _(off)_  | Set to `1` to force localhost mode (single port)   |
| `ZIPGO_METRICS`        | _(off)_  | Set to expose a Prometheus `/metrics` endpoint     |
| `ZIPGO_METRICS_ADDR`   | `127.0.0.1:2019` | Bind address for the metrics endpoint (loopback) |
