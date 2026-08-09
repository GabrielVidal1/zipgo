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

The full model behind this diagram — folder tree, hosts table, and what Caddy
config each site produces — is [`docs/model.md`](./docs/model.md).

## Features

- **Drop-in deploy** — create a domain folder under `.zipgo/`; changes are picked up automatically
- **Automatic HTTPS** — Let's Encrypt certificates managed by Caddy, no configuration needed
- **Filesystem-as-config** — the folder tree _is_ the routing table; no config files to maintain
- **Recursive subdomains** — a folder ending in a dot (`docs.`) becomes a subdomain; nest them for sub-subdomains
- **Multiple domains** — host several domains at once, each in its own folder
- **SPA support** — auto-detected; all unknown paths fall back to `index.html`
- **Custom 404 page** — drop a `404.html` in a static site's folder and it becomes the body of its 404s
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

The domains folder (default `.zipgo/`, override with `ZIPGO_DOMAINS_FOLDER`) holds one folder per domain — a folder whose name has a dot is a domain, and inside it a folder ending in a dot is a subdomain, recursively:

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

A `.zipgoconfig.json` dropped in any of those folders tweaks how that one site
is served — reverse-proxy it (`rewrite`), redirect it, add response headers,
put it behind basic auth, or turn it off — and a `404.html` next to its
`index.html` becomes that site's 404 body. That's the entire per-site surface.

**The whole model — the folder tree worked through to a hosts table, the
complete `.zipgoconfig.json` reference, and exactly what Caddy config each
kind of site produces — is documented in one page with the architecture
diagram: [`docs/model.md`](./docs/model.md).** Read that before proposing a
change here; this README stays a landing page.

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
`--delete`/`--prune` (mirror — delete remote files missing from the source;
this is the default, `--prune` is just a clearer name for it), `--no-delete`
(keep them), `--include-subdomains` (let the mirror also delete nested
trailing-dot subdomain folders; by default they're protected), `-n/--dry-run`.

A `--dry-run` **previews exactly what would change and touches nothing** — it
runs the same rsync with `--itemize-changes` and prints a per-host summary of
which files would be added, replaced and deleted, so you can eyeball a mirror
before it prunes the remote:

```text
🌐  app.dev.gabvdl.xyz
   → user@host:/base/gabvdl.xyz/dev./app.
   dry run — 2 added, 1 replaced, 1 deleted:
     ＋ add      assets/app-9f3.js
     ＋ add      favicon.ico
     ~ replace  index.html
     － delete   assets/app-old.js
   (dry run — nothing was changed)
```

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
| `headers` isn't an object of strings, or a header name/value is unusable | error |
| A `headers` entry overriding a header the server computes (`Content-Length`) | warning |
| Folder name isn't a valid domain / hostname | error |
| Folder in the domains root with no dot — silently ignored by the server | warning |
| Subdomain folder that forgot its trailing dot (`docs.example.com`) | warning |
| A `rewrite` upstream zipgo cannot turn into a dial address | error |
| `rewritePath`/`rewritePathPassthrough` set without `rewrite` — dead config | warning |
| A `redirect` target that isn't an absolute `http(s)` URL (a path would loop) | error |
| A `redirectStatus` that isn't 301/302/307/308 | error |
| `redirect` and `rewrite` both set — the proxy upstream is never used | error |
| A `basicAuth` password that isn't a bcrypt hash (a plaintext locks everyone out) | error |
| A `basicAuth` entry with an empty username | error |
| An empty `basicAuth` block — the site is served with no password at all | warning |
| `redirect` on a folder that still has an `index.html` (never served) | warning |
| A `404.html` in an SPA, `rewrite` or `redirect` site — it is never served | warning |
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
| `ZIPGO_LOG_FORMAT`      | _(off)_   | Set to `json` for one JSON access-log line per request on stdout |

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

### Access logs

Set `ZIPGO_LOG_FORMAT=json` to emit one structured JSON access-log line per
request on **stdout** — host, path, method, status, duration, response size and
headers — ready to be scraped by a log shipper (e.g. Loki via the container's
stdout). Each line is tagged `"logger":"http.log.access.access"`, so a pipeline
can pick access logs out from zipgo's own error output:

```jsonc
{"level":"info","logger":"http.log.access.access","msg":"handled request",
 "request":{"host":"blog.example.com","method":"GET","uri":"/posts/hi"},
 "duration":0.0026,"size":1421,"status":200}
```

Off by default (no per-request logging, only Caddy errors). The loopback metrics
server is excluded, so Prometheus scrapes don't flood the request stream.

---

## How It Works

Startup reads the domains folder, discovers sites, builds an in-memory Caddy
config from them, and a file watcher rebuilds it on every change — the exact
mechanics (what each site's route looks like, TLS subjects, the file watcher's
reload loop) are the "what Caddy gets" section of
[`docs/model.md`](./docs/model.md).

---

## License

MIT
