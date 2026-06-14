# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

**zipgo** — a minimal static site host written in Go. It embeds Caddy as a library, discovers domains and subdomains from a local domains folder (default `.zipgo/`), and serves them over HTTPS (domain mode) or HTTP on a single localhost port (localhost mode).

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

CLI subcommands: `zipgo serve` (default), `zipgo enable|disable|status` (systemd user service), `zipgo help`.

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

### Subdomain metadata endpoint
Any site that has at least one direct child subdomain (the apex, or a subdomain with nested subdomains) serves a JSON listing at `<host>/sub-domains-meta` (localhost: `/<domain>/<path>/sub-domains-meta`). It maps each direct child's folder name (e.g. `"docs."`) to metadata extracted from that child's `index.html` — `title`, `description`, OpenGraph (`og:*`) tags, favicon `icon`, and custom `zipgo:*` meta properties (exposed under `zipgo`, keyed by the part after the prefix). The JSON is computed at config-build time in `internal/builder` (`childMeta`/`metaRoute`) using `internal/meta` and embedded as a Caddy `static_response`, so it refreshes on every watcher reload. Children one level down only; leaf sites get no endpoint (404).

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
