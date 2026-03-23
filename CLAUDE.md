# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

**zipgo** — a minimal static site host written in Go. It embeds Caddy as a library, discovers sites from a local `apps/` directory, and serves them over HTTPS (domain mode) or HTTP on sequential localhost ports (localhost mode).

## Commands

```bash
make build          # Compile binary (also regenerates install scripts)
make run-local      # Build + run on localhost (no sudo, no domain needed)
make run            # Build + run with real domain (needs sudo, ports 80/443)
make format         # Run gofmt
make clean          # Remove binary
make build-install-scripts  # Regenerate apps/install/{linux,macos,windows}.sh from parts
```

Run with a custom password:
```bash
ZIPGO_PASS=mypass make run-local
```

## Architecture

### Startup flow (`main.go`)
1. Reads `apps/root.txt` — empty/missing = localhost mode, otherwise = domain mode
2. Starts the backoffice HTTP server on `127.0.0.1:9876` (loopback only)
3. Optionally starts Vince analytics as a subprocess (`./vince` binary next to the executable)
4. Calls `sites.Discover()` + `builder.Build*Config()` to generate a Caddy JSON config in memory
5. Calls `caddy.Run(cfg)` — no Caddyfile on disk, config is entirely in-memory
6. On deploy/delete, the backoffice calls `reload()` which re-runs steps 4–5

### Internal packages
- **`internal/sites`** — scans `apps/` subdirs; detects SPAs when `index.html` + one of `assets/`, `static/`, `_next/`, `dist/` is present
- **`internal/builder`** — constructs the Caddy JSON config; `BuildLocalhostConfig` for HTTP-only sequential ports, `BuildConfig` for HTTPS subdomain routing
- **`internal/backoffice`** — password-protected web UI; handles ZIP/HTML upload, site deletion, calls `onReload()`
- **`internal/config`** — reads `apps/root.txt`
- **`internal/landing`** — generates the auto-index page when no `root/` site exists

### Site routing
- **Domain mode**: `<name>.<rootDomain>` → `apps/<name>/`; `backoffice.<rootDomain>` → backoffice UI; `analytics.<rootDomain>` → Vince
- **Localhost mode**: port `9000` = root/landing, `9001+` = sites in discovery order, `8999` = backoffice, `8898` = Vince

### Install scripts
`apps/install/linux.sh`, `macos.sh`, and `windows.sh` are **generated files** — do not edit them directly. Edit the parts in `scripts/parts/` and regenerate with `make build-install-scripts` (or `make build`).

### `apps/` directory
Contains both the zipgo website itself (served at `zipgo.xyz`) and each subdirectory is a hosted site:
- `apps/root.txt` — domain name (e.g. `zipgo.xyz`); empty = localhost mode
- `apps/root/` — served at the apex domain
- `apps/install/`, `apps/docs/`, `apps/demo/`, `apps/example/` — subdomains
- The install scripts under `apps/install/` download the `zipgo` binary from GitHub releases

## Environment Variables

| Variable     | Default        | Description         |
|--------------|----------------|---------------------|
| `ZIPGO_USER` | `admin`        | Backoffice username |
| `ZIPGO_PASS` | _(auto-gen)_   | Backoffice password |
