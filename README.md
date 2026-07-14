# zipgo

![GitHub release](https://img.shields.io/github/v/release/GabrielVidal1/zipgo)
![Go version](https://img.shields.io/github/go-mod/go-version/GabrielVidal1/zipgo)
![Build](https://img.shields.io/github/actions/workflow/status/GabrielVidal1/zipgo/release.yml)
![License](https://img.shields.io/github/license/GabrielVidal1/zipgo)
![Platforms](https://img.shields.io/badge/platforms-linux%20%7C%20macOS-blue)

A minimal static site host powered by [Caddy](https://caddyserver.com/) and Go. Drop a domain folder into `.zipgo/` and your sites are live — with automatic HTTPS via Let's Encrypt, or on localhost with zero config.

---

## Architecture

![zipgo architecture](./docs/architecture.svg)

## Features

- **Drop-in deploy** — create a domain folder under `.zipgo/`; changes are picked up automatically
- **Automatic HTTPS** — Let's Encrypt certificates managed by Caddy, no configuration needed
- **Filesystem-as-config** — the folder tree _is_ the routing table; no config files to maintain
- **Recursive subdomains** — a folder ending in a dot (`docs.`) becomes a subdomain; nest them for sub-subdomains
- **Multiple domains** — host several domains at once, each in its own folder
- **SPA support** — auto-detected; all unknown paths fall back to `index.html`
- **Password-protected sites** — `"basicAuth"` in a site's `.zipgoconfig.json` puts a staging subdomain behind HTTP basic auth
- **Localhost mode** — no domain needed; all sites served on a single port (`9000`) with path routing
- **Systemd integration** — `zipgo enable` sets up a user service that starts on boot

---

## Requirements

- Go 1.21+
- A server with ports 80/443 open (for domain mode), or just a local machine (for localhost mode)
- DNS records pointing each domain (and its subdomains) to your server (domain mode only)

---

## Quick Start

### Localhost (no domain, no sudo)

```bash
git clone https://github.com/GabrielVidal1/zipgo
cd zipgo
make run-local
```

In localhost mode every site is reachable on a single port under a path that mirrors its host:

| URL                                        | What                          |
| ------------------------------------------ | ----------------------------- |
| `http://localhost:9000/yourdomain.com`     | The apex site                 |
| `http://localhost:9000/yourdomain.com/docs`| The `docs.` subdomain         |

### Domain mode

```bash
# 1. Create a domain folder and point its DNS records at your server
mkdir -p .zipgo/yourdomain.com
echo "<h1>hello</h1>" > .zipgo/yourdomain.com/index.html

# 2. Run (needs ports 80/443)
make run
```

| URL                              | What                  |
| -------------------------------- | --------------------- |
| `https://yourdomain.com`         | The apex site         |
| `https://docs.yourdomain.com`    | The `docs.` subdomain |

---

## Directory Layout

The domains folder (default `.zipgo/`, override with `ZIPGO_DOMAINS_FOLDER`) holds one folder per domain. **A domain folder name must contain a dot** — folders without one are ignored.

```
.zipgo/
└── yourdomain.com/            # one folder per domain (name must contain a dot)
    ├── index.html             # the apex site — served at yourdomain.com
    ├── assets/                # regular content, served as a path under the apex
    ├── docs./                 # → docs.yourdomain.com
    │   ├── index.html
    │   └── api./              # → api.docs.yourdomain.com  (recursive)
    └── blog./                 # → blog.yourdomain.com
```

### Subdomain rule

Inside a domain folder, **any subdirectory whose name ends in a dot is a subdomain**. The label is the name with the trailing dot removed, and the rule applies recursively:

| Folder                              | Host                          |
| ----------------------------------- | ----------------------------- |
| `yourdomain.com/` (the root itself) | `yourdomain.com`              |
| `yourdomain.com/docs./`             | `docs.yourdomain.com`         |
| `yourdomain.com/docs./api./`        | `api.docs.yourdomain.com`     |
| `yourdomain.com/foo.bar./`          | `foo.bar.yourdomain.com`      |

Folders that do **not** end in a dot are treated as ordinary content of their parent site (served as paths). Subdomain folders are hidden from the apex's directory listing, so they don't leak into `https://yourdomain.com/`.

### Site structure

The domain folder root is the apex site; each subdomain folder is its own site.

```
yourdomain.com/
├── index.html         # entry point
├── assets/            # presence of this (or static/, _next/, dist/) marks the site as an SPA
└── ...
```

**SPA detection** — a site is treated as a single-page app when it contains `index.html` **and** one of the bundler output directories: `assets/`, `static/`, `_next/`, `dist/`. All unmatched paths are rewritten to `/index.html`.

### Per-site config (`.zipgoconfig.json`)

Drop a `.zipgoconfig.json` file in any domain/subdomain folder to tweak how that
single site is served. The file is never served to clients (the file server
hides it). All keys are optional:

```jsonc
{
  "enable": true,                  // false → don't serve this site, and omit it
                                   //         from any parent's sub-domains-meta
  "rewrite": "localhost:8080",     // reverse-proxy to this upstream instead of
                                   //   serving files (host:port, or a URL with
                                   //   scheme: "https://api.example.com")
  "redirect": "https://elsewhere.example", // redirect every request to this URL
                                   //   instead of serving files
  "redirectStatus": 302,           // status used for "redirect" (default 302;
                                   //   301/308 permanent, 302/307 temporary)
  "allowHttp": false,              // true → also serve over plain HTTP (:80)
                                   //        instead of redirecting to HTTPS
  "basicAuth": {                   // put the whole site behind a password
    "alice": "$2a$14$Zkx19XLiW…"   //   username → *bcrypt hash*, never a
  }                                //   plaintext password
}
```

- **`enable: false`** removes the site entirely — no route, no TLS cert, and it
  disappears from its parent's `/sub-domains-meta` listing.
- **`rewrite`** makes the site a reverse proxy: requests are forwarded to the
  given upstream rather than served from the folder (no `index.html` needed). A
  bare `host:port` dials plain HTTP; a `https://` URL proxies over TLS.
- **`redirect`** turns the host into a redirect (no `index.html` needed — this is
  the replacement for the `<meta http-equiv="refresh">` trick). The target must
  be an **absolute** `http(s)` URL:
  - a bare origin — `"https://elsewhere.example"` — **keeps the path and query**,
    so deep links survive a domain move: `/blog/post?x=1` →
    `https://elsewhere.example/blog/post?x=1`;
  - a target with a path — `"https://elsewhere.example/moved"` — is used
    verbatim: every request lands on that one URL.

  A bare path (`"/new"`) is rejected: a redirect replaces the site's file server,
  so it would redirect to itself forever. `redirect` also wins over `rewrite` —
  `doctor` errors when both are set.
- **`redirectStatus`** picks the status code — `301`/`308` (permanent) or
  `302`/`307` (temporary). It defaults to **302**: browsers cache a 301 for a
  long time, and in a folder-tree config a redirect should be as easy to undo as
  the file that declared it. Set `301` once the move is final.
- **`allowHttp: true`** serves the site on port 80 as well as 443 (by default
  every host is 301-redirected to HTTPS). No effect in localhost mode, which is
  already HTTP-only.
- **`basicAuth`** puts the site behind HTTP basic auth — one file turns a
  staging subdomain into a password-protected one. Keys are usernames, values
  are **bcrypt hashes** (`caddy hash-password`, or `htpasswd -nbB alice s3cret`
  and keep the `$2y$…` part). Plaintext passwords are never accepted: Caddy
  compares against a hash, so a plaintext value locks *everyone* out — `doctor`
  reports it as an error.

  The check runs before anything is served, so it protects static files, SPA
  routes, a `rewrite` upstream and the site's own `/sub-domains-meta` listing
  alike. Child subdomains are **separate sites** and are not covered — give each
  one its own `basicAuth` (they can share the same hash).

```bash
# retire old.example.com, keeping every deep link working
mkdir -p .zipgo/example.com/old.
echo '{"redirect": "https://new.example.com", "redirectStatus": 301}' \
    > .zipgo/example.com/old./.zipgoconfig.json

# password-protect staging.example.com
mkdir -p .zipgo/example.com/staging.
echo "{\"basicAuth\": {\"alice\": \"$(htpasswd -nbB alice s3cret | cut -d: -f2)\"}}" \
    > .zipgo/example.com/staging./.zipgoconfig.json
```

Edits are picked up by the file watcher and hot-reloaded like any other change.

---

## Deploying a Site

Copy your build output into the right folder. The file watcher picks up changes automatically and reloads Caddy:

```bash
# apex
cp -r dist/* .zipgo/yourdomain.com/

# a subdomain (note the trailing dot)
cp -r dist/ .zipgo/yourdomain.com/app./
```

---

## CLI

```bash
zipgo serve     # start the server (default when no command is given)
zipgo deploy    # rsync a local dir to a remote zipgo host over SSH
zipgo ls        # list sites deployed on the remote target
zipgo info      # show a deployed site's remote path, size and mtime
zipgo doctor    # check the local domains folder for broken sites
zipgo enable    # install and start the systemd user service
zipgo disable   # stop and remove the systemd user service
zipgo status    # show service status
zipgo help      # usage
```

### `zipgo deploy`

Push a local build to a remote zipgo host. It creates the domain/subdomain
folder tree (the trailing-dot convention above) on the remote and rsyncs the
directory's contents into it:

```bash
zipgo deploy dist/ -d love-letters.game.gabvdl.xyz \
    --ssh gabrielvidal@100.74.118.12:/home/gabrielvidal/services/domains
# -> creates  .../domains/gabvdl.xyz/game./love-letters.
#    serving   https://love-letters.game.gabvdl.xyz  (zipgo hot-reloads)
```

`-d/--domain` is repeatable (deploy the same build to several hosts). Flags:
`--ssh/--target user@host:/base/path`, `--exclude <pat>` (repeatable),
`--no-delete` (don't mirror), `-n/--dry-run`.

#### Config-driven deploy

The target and the per-host source folders can be read from config, so a
configured project deploys with a bare `zipgo deploy`:

- **Root config** — a `.zipgo.json` file, found by ascending the directory tree
  from the current dir (like `git`/`.npmrc`), supplies the default target:

  ```json
  { "target": "gabrielvidal@100.74.118.12:/home/gabrielvidal/services/domains" }
  ```

  Optional `"targets": { "name": "user@host:/base" }` defines named targets a
  project can reference.

- **Project config** — a `"zipgo"` key in the project's `package.json` maps each
  host to the local folder deployed to it (paths relative to `package.json`):

  ```json
  "zipgo": {
    "deploy": {
      "www.gabvdl.xyz":      "dist/apex",
      "www.dev.gabvdl.xyz":  "dist/dev",
      "www.game.gabvdl.xyz": "dist/game"
    }
  }
  ```

Resolution precedence (each field independently): `--ssh` flag → project
`target` → root `target`; `-d` hosts → every key of the deploy map; positional
`dir` → the map entry for the host. With both files present:

```bash
zipgo deploy                       # deploy every host in the map
zipgo deploy -d www.dev.gabvdl.xyz # one host, its mapped folder, default target
```

The fully-explicit `dist/ -d host --ssh …` form keeps working unchanged.

### `zipgo ls` / `zipgo info`

Read-only inspection of what's deployed under the remote target (resolved from
`.zipgo.json`, or `--ssh`/`--target`):

```bash
zipgo ls                            # every deployed site, grouped by domain
zipgo ls love-letters.game.gabvdl.xyz   # one site's remote folder contents
zipgo info love-letters.game.gabvdl.xyz # remote path, kind, size, files, mtime
```

#### `--json`

Both commands take `--json` for scripts and dashboards, so nothing has to scrape
the human output:

```bash
zipgo ls --json          # array of sites
zipgo ls <host> --json   # array of files in that site's folder
zipgo info <host> --json # that one site, as an object
```

Each site is reported the way the server routes it — `type` is `static`, `spa`
(an `index.html` next to an `assets/`, `static/`, `_next/` or `dist/` folder),
`proxy` (a `.zipgoconfig.json` `rewrite`) or `redirect` (a `.zipgoconfig.json`
`redirect`, reported with its `redirect` target and `redirectStatus`), `enabled`
is `false` for a site turned off with `"enable": false`, `protected` is `true`
for a site behind `basicAuth` (the hashes themselves are never exposed), and an
unreadable
`.zipgoconfig.json` shows up as `configError` instead of being silently ignored. `sizeBytes`/`files` count a
site's **own** content: nested subdomain folders are sites of their own and are
excluded, so an apex isn't reported as the sum of its whole domain.

```console
$ zipgo ls --json | jq -r '.[] | select(.enabled) | "\(.host)\t\(.type)"'
gabvdl.xyz                      spa
love-letters.game.gabvdl.xyz    static
```

```json
{
  "host": "docs.example.com",
  "url": "https://docs.example.com",
  "path": "/home/me/domains/example.com/docs.",
  "type": "proxy",
  "proxy": "localhost:8080",
  "enabled": true,
  "protected": false,
  "deployed": true,
  "sizeBytes": 18,
  "files": 1,
  "modified": "2026-07-14T14:13:31Z"
}
```

### `zipgo doctor`

Answers "why isn't my site serving?" without reading Caddy's logs. It checks the
**local** domains folder (the one the server reads — `$ZIPGO_DOMAINS_FOLDER`, or
a folder passed as an argument) and reports, per host:

| Check | Level |
|---|---|
| No `index.html` (and no `rewrite`) — the host has nothing to serve | error |
| …unless the folder only holds subdomains, which is a legitimate shape | warning |
| `.zipgoconfig.json` is not valid JSON — **this blocks every reload** | error |
| Unknown key in `.zipgoconfig.json` (`"enabled"` instead of `"enable"`) | warning |
| Folder name isn't a valid domain / hostname | error |
| Folder in the domains root with no dot — silently ignored by the server | warning |
| Subdomain folder that forgot its trailing dot (`docs.example.com`) | warning |
| A `rewrite` upstream zipgo cannot turn into a dial address | error |
| A `redirect` target that isn't an absolute `http(s)` URL (a path would loop) | error |
| A `redirectStatus` that isn't 301/302/307/308 | error |
| `redirect` and `rewrite` both set — the proxy upstream is never used | error |
| A `basicAuth` password that isn't a bcrypt hash (a plaintext locks everyone out) | error |
| A `basicAuth` entry with an empty username | error |
| An empty `basicAuth` block — the site is served with no password at all | warning |
| `redirect` on a folder that still has an `index.html` (never served) | warning |
| `redirectStatus` without a `redirect` target | warning |
| Two folders claiming the same host (`a.b.` and `b./a.`) | error |

```bash
zipgo doctor                # check $ZIPGO_DOMAINS_FOLDER (default .zipgo)
zipgo doctor /srv/domains   # check a specific folder
zipgo doctor --strict       # exit 1 on warnings too, not just errors
```

It exits **1** when a site is broken, so it can gate a deploy:

```bash
zipgo doctor && zipgo deploy
```

A malformed `.zipgoconfig.json` is worth calling out: the server refuses to
rebuild its config while one exists, so *every* site silently stays on the last
good config. `doctor` is the fastest way to find which file it is.

---

## Makefile Reference

| Command          | Description                                           |
| ---------------- | ----------------------------------------------------- |
| `make build`     | Compile the binary (also regenerates install scripts) |
| `make run`       | Run in the foreground with a real domain (needs sudo) |
| `make run-local` | Run on localhost — no domain, no sudo                 |
| `make run-prod`  | Grant port-binding capability and run in domain mode  |
| `make format`    | Run gofmt                                             |
| `make clean`     | Remove compiled binary                                |

---

## Environment Variables

| Variable                | Default   | Description                                        |
| ----------------------- | --------- | -------------------------------------------------- |
| `ZIPGO_DOMAINS_FOLDER`  | `.zipgo`  | Folder scanned for domain subfolders               |
| `ZIPGO_LOCALHOST`       | _(off)_   | Set to `1` to force localhost mode (single port)   |
| `ZIPGO_METRICS`         | _(off)_   | Set to any value to expose Prometheus metrics      |
| `ZIPGO_METRICS_ADDR`    | `127.0.0.1:2019` | Address for the metrics endpoint (loopback by default) |

If the domains folder contains no valid domain folders, zipgo automatically falls back to localhost mode.

### Metrics

Set `ZIPGO_METRICS=1` to enable per-request HTTP metrics and serve a Prometheus
endpoint at `http://127.0.0.1:2019/metrics` (override the bind address with
`ZIPGO_METRICS_ADDR`). The endpoint is a plain metrics handler — Caddy's admin
API stays disabled — and binds to loopback by default so metrics are never
accidentally public. Point Prometheus at it:

```yaml
scrape_configs:
  - job_name: zipgo
    static_configs:
      - targets: ["127.0.0.1:2019"]
```

Useful series: `caddy_http_requests_total`, `caddy_http_request_duration_seconds`,
`caddy_http_response_size_bytes` (labeled by handler and status).

---

## How It Works

1. **Startup** — `main.go` reads `ZIPGO_DOMAINS_FOLDER` (default `.zipgo`) and lists its domain subfolders. No domains (or `ZIPGO_LOCALHOST=1`) → localhost mode.
2. **Discovery** — `sites.Discover()` walks each domain folder: the root is the apex, and every dot-suffixed subfolder is a subdomain, recursively. SPAs are auto-detected.
3. **Config build** — `builder` constructs a Caddy JSON config in memory:
   - Domain mode: one HTTPS server, one route per host, HTTP→HTTPS redirect, Let's Encrypt TLS (exact host subjects).
   - Localhost mode: a single listener on port `9000` with path routing, no TLS.
4. **Caddy** is started (or reloaded) with the generated config — no Caddyfile on disk.
5. **File watcher** — changes under the domains folder trigger a debounced `reload()`, re-running steps 2–4 with the updated site list.

---

## License

MIT
