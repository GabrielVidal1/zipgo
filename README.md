# zipgo

![GitHub release](https://img.shields.io/github/v/release/GabrielVidal1/zipgo)
![Go version](https://img.shields.io/github/go-mod/go-version/GabrielVidal1/zipgo)
![Build](https://img.shields.io/github/actions/workflow/status/GabrielVidal1/zipgo/release.yml)
![License](https://img.shields.io/github/license/GabrielVidal1/zipgo)
![Platforms](https://img.shields.io/badge/platforms-linux%20%7C%20macOS-blue)

A minimal static site host powered by [Caddy](https://caddyserver.com/) and Go. Drop a folder into `apps/` and your site is live — with automatic HTTPS via Let's Encrypt, or on localhost with zero config.

---

## Architecture

![zipgo architecture](./docs/architecture.svg)

## Features

- **Drop-in deploy** — copy a folder into `apps/`; changes are picked up automatically
- **Automatic HTTPS** — Let's Encrypt certificates managed by Caddy, no configuration needed
- **SPA support** — auto-detected; all unknown paths fall back to `index.html`
- **Subdomain routing** — each site lives at `<name>.<your-domain>.com`
- **Localhost mode** — no domain needed; all sites served on a single port (`9000`) with path routing
- **Auto landing page** — when no root site exists, a generated index links to all hosted sites
- **Systemd integration** — `make install` sets up a service that starts on boot

---

## Requirements

- Go 1.21+
- A server with ports 80/443 open (for domain mode), or just a local machine (for localhost mode)
- A domain with a wildcard DNS record pointing to your server (domain mode only)

---

## Quick Start

### Localhost (no domain, no sudo)

```bash
git clone https://github.com/GabrielVidal1/zipgo
cd zipgo
make run-local
```

| URL                            | What                |
| ------------------------------ | ------------------- |
| `http://localhost:9000`        | Root / landing page |
| `http://localhost:9000/<name>` | A deployed site     |

### Domain mode

```bash
# 1. Point *.yourdomain.com → your server IP (DNS)
echo "yourdomain.com" > apps/root.txt

# 2. Run (needs ports 80/443)
make run
```

| URL                                 | What                |
| ----------------------------------- | ------------------- |
| `https://yourdomain.com`            | Root / landing page |
| `https://mysite.yourdomain.com`     | Site named "mysite" |

---

## Directory Layout

```
zipgo/
├── apps/                  # Your sites live here
│   ├── root.txt           # (optional) your domain — omit for localhost mode
│   ├── root/              # Served at the apex domain / port 9000
│   ├── blog/              # → blog.yourdomain.com / port 9001
│   └── docs/              # → docs.yourdomain.com / port 9002
├── internal/
│   ├── builder/           # Caddy config construction
│   ├── config/            # root.txt reader
│   ├── landing/           # Auto-generated landing page
│   └── sites/             # Site discovery + SPA detection
└── main.go
```

### Site structure

Each subdirectory of `apps/` is a site. The directory name becomes the subdomain (or `root` for the apex domain).

```
apps/
└── myblog/
    ├── index.html         # Required
    ├── assets/            # Presence of this (or static/, _next/, dist/) marks site as SPA
    └── ...
```

**SPA detection** — a site is treated as a single-page app when it contains `index.html` **and** one of the bundler output directories: `assets/`, `static/`, `_next/`, `dist/`. All unmatched paths are rewritten to `/index.html`.

---

## Deploying a Site

Copy your build output into `apps/<name>/`. The file watcher picks up changes automatically and reloads Caddy:

```bash
cp -r dist/ apps/mysite/
```

The site name (the directory name) becomes the subdomain, or `root` for the apex domain.

---

## Makefile Reference

| Command          | Description                                           |
| ---------------- | ----------------------------------------------------- |
| `make build`     | Compile the binary                                    |
| `make run`       | Run in the foreground with a real domain (needs sudo) |
| `make run-local` | Run on localhost — no domain, no sudo                 |
| `make install`   | Build, install binary, create systemd service         |
| `make uninstall` | Stop and remove the service and binary                |
| `make up`        | Start the systemd service                             |
| `make down`      | Stop the systemd service                              |
| `make restart`   | Restart (picks up new sites automatically)            |
| `make status`    | Show service status                                   |
| `make logs`      | Follow live logs                                      |
| `make clean`     | Remove compiled binary                                |

---

## Production Install (systemd)

```bash
# 1. Install binary + service
make install

# 2. Start
make up

# 3. Follow logs
make logs
```

The service runs with `CAP_NET_BIND_SERVICE` so it can bind to ports 80/443 without running as root.

---

## Environment Variables

| Variable          | Default | Description                                      |
| ----------------- | ------- | ------------------------------------------------ |
| `ZIPGO_LOCALHOST` | _(off)_ | Set to `1` to force localhost mode (single port) |

---

## How It Works

1. **Startup** — `main.go` reads `apps/root.txt` to determine the mode (domain vs. localhost).
2. **Discovery** — `sites.Discover()` scans `apps/` for subdirectories and detects SPAs.
3. **Config build** — `builder` constructs a Caddy JSON config in memory:
   - Domain mode: one HTTPS server, one route per subdomain, HTTP→HTTPS redirect, Let's Encrypt TLS.
   - Localhost mode: a single listener on port `9000` with path routing, no TLS.
4. **Caddy** is started (or reloaded) with the generated config — no Caddyfile on disk.
5. **File watcher** — changes under the domains dir trigger a debounced `reload()`, re-running steps 2–4 with the updated site list.

---

## Documentation

| Page                              | Description                                           |
| --------------------------------- | ----------------------------------------------------- |
| [Landing page](./docs/landing.md) | The auto-generated site index and how to customise it |

---

## License

MIT
