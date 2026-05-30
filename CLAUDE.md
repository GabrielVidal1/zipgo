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

## Architecture

### Startup flow (`main.go`)
1. Reads `apps/root.txt` — empty/missing = localhost mode, otherwise = domain mode
2. Calls `sites.Discover()` + `builder.Build*Config()` to generate a Caddy JSON config in memory
3. Calls `caddy.Run(cfg)` — no Caddyfile on disk, config is entirely in-memory
4. Watches the domains dir; on filesystem changes it calls `reload()` which re-runs steps 2–3

### Internal packages
- **`internal/sites`** — scans `apps/` subdirs; detects SPAs when `index.html` + one of `assets/`, `static/`, `_next/`, `dist/` is present
- **`internal/builder`** — constructs the Caddy JSON config; `BuildLocalhostConfig` for HTTP-only path routing, `BuildConfig` for HTTPS subdomain routing
- **`internal/config`** — reads `apps/root.txt`
- **`internal/landing`** — generates the auto-index page when no `root/` site exists

### Site routing
- **Domain mode**: `<name>.<rootDomain>` → `apps/<name>/`
- **Localhost mode**: single port `9000` with path routing `/<domain>/<name>`

### Install scripts
`apps/install/linux.sh`, `macos.sh`, and `windows.sh` are **generated files** — do not edit them directly. Edit the parts in `scripts/parts/` and regenerate with `make build-install-scripts` (or `make build`).

### `apps/` directory
Contains both the zipgo website itself (served at `zipgo.xyz`) and each subdirectory is a hosted site:
- `apps/root.txt` — domain name (e.g. `zipgo.xyz`); empty = localhost mode
- `apps/root/` — served at the apex domain
- `apps/install/`, `apps/docs/`, `apps/demo/`, `apps/example/` — subdomains
- The install scripts under `apps/install/` download the `zipgo` binary from GitHub releases

## Environment Variables

| Variable          | Default | Description                                        |
|-------------------|---------|----------------------------------------------------|
| `ZIPGO_LOCALHOST` | _(off)_ | Set to `1` to force localhost mode (single port)   |
