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
- **Custom 404 page** — drop a `404.html` in a static site's folder and it becomes the body of its 404s
- **Password-protected sites** — `zipgo password set <host>` puts a whole site behind a login page (one shared password, a session cookie); deploys keep the lock. `"basicAuth"` is there too for HTTP basic auth
- **Switch a site off** — `zipgo off <host>` takes a deployed site down (410 Gone, nested subdomains included) without deleting it; `zipgo on <host>` brings it back
- **Back-office** — `zipgo admin set admin.yourdomain.com` gives a domain a password-protected page, made for a phone, to switch any of its sites (sub and sub-sub domains included) off and on and to put a password on one. It edits the same `.zipgoconfig.json` files the CLI does — no state of its own
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
├── 404.html           # optional — the body of this site's 404s
├── assets/            # presence of this (or static/, _next/, dist/) marks the site as an SPA
└── ...
```

**SPA detection** — a site is treated as a single-page app when it contains `index.html` **and** one of the bundler output directories: `assets/`, `static/`, `_next/`, `dist/`. All unmatched paths are rewritten to `/index.html`.

**Custom 404 page** — a static site with a `404.html` in its folder serves that
file, instead of Caddy's plain-text 404, for any request that matches no file.
Nothing to configure: the file being there *is* the config. The status stays
**404** — only the body changes, so crawlers and `curl -f` still see a miss
rather than a soft 404. It applies per site (a subdomain does not inherit its
parent's page) and only to a static one: an SPA answers unknown paths with
`index.html` and a 200, and `rewrite`/`redirect` sites never reach the file
server — `zipgo doctor` warns when a `404.html` sits in one of those folders,
where it would never be served.

### Per-site config (`.zipgoconfig.json`)

Drop a `.zipgoconfig.json` file in any domain/subdomain folder to tweak how that
single site is served. The file is never served to clients (the file server
hides it). All keys are optional:

```jsonc
{
  "enable": true,                  // false → switch the site off: 410 Gone for
                                   //   it and every subdomain nested under it
                                   //   (`zipgo off <host>` writes this for you)
  "rewrite": "localhost:8080",     // reverse-proxy to this upstream instead of
                                   //   serving files (host:port, or a URL with
                                   //   scheme: "https://api.example.com")
  "rewritePath": "/_/",            // prefix the proxied path, so a sub-path of
                                   //   the upstream is served at this site's
                                   //   root (needs "rewrite")
  "rewritePathPassthrough": ["/api"], // paths the upstream owns at its root, so
                                   //   they are NOT moved under "rewritePath"
                                   //   (default ["/api"])
  "redirect": "https://elsewhere.example", // redirect every request to this URL
                                   //   instead of serving files
  "redirectStatus": 302,           // status used for "redirect" (default 302;
                                   //   301/308 permanent, 302/307 temporary)
  "allowHttp": false,              // true → also serve over plain HTTP (:80)
                                   //        instead of redirecting to HTTPS
  "headers": {                     // extra response headers, merged into the
    "Cache-Control": "public, max-age=31536000, immutable"
  },                               //   security headers zipgo already sends
  "admin": false,                  // true → this folder is the back-office of
                                   //   the site it is nested in (needs a
                                   //   "password"; `zipgo admin set` writes both)
  "password": {                    // a login page in front of the whole site
    "hash": "$2a$12$Zkx19XLiW…",   //   *bcrypt hash* of the shared password
    "title": "Ballance",           //   the login page's heading (default: host)
    "sessionDays": 7               //   how long a login lasts without a visit
  },                               //   (`zipgo password set` writes all this)
  "basicAuth": {                   // or HTTP basic auth
    "alice": "$2a$14$Zkx19XLiW…"   //   username → *bcrypt hash*, never a
  }                                //   plaintext password
}
```

- **`enable: false`** switches the site off without deleting it: every request
  gets a small **410 Gone** page (with `Cache-Control: no-store`, so nothing
  lingers once it's back on), it keeps its TLS certificate so visitors see that
  page rather than a certificate error, and it disappears from its parent's
  `/sub-domains-meta` listing. It **cascades**: every subdomain nested under the
  folder goes off with it (`app./pb./` is off when `app./` is), and an
  `"enable": true` further down cannot override that. You rarely write it by
  hand — [`zipgo off`](#zipgo-off--zipgo-on) does it over SSH and keeps the
  file's other keys.
- **`rewrite`** makes the site a reverse proxy: requests are forwarded to the
  given upstream rather than served from the folder (no `index.html` needed). A
  bare `host:port` dials plain HTTP; a `https://` URL proxies over TLS.
- **`rewritePath`** prepends a path to every proxied request, so a sub-path of an
  upstream is served at this site's root while the browser's URL stays on this
  host (unlike a `redirect`, which bounces the address bar). The motivating case
  is an admin UI that lives under a prefix — PocketBase serves its dashboard at
  `/_/`, so:

  ```jsonc
  // domains/example.xyz/admin./.zipgoconfig.json
  { "rewrite": "app-pocketbase:8090", "rewritePath": "/_/" }
  ```

  puts the dashboard on `https://admin.example.xyz/` instead of
  `https://pb.example.xyz/_/`.

  The prefix is **not** applied to a request that already starts with it, nor to
  the paths in `rewritePathPassthrough` — the app under the prefix usually still
  calls the upstream's *root* paths (PocketBase's dashboard fetches `/api/…`
  absolutely), and prefixing those to `/_/api/…` would 404, so the dashboard
  would load but never log in. The default passthrough is `["/api"]`, which
  covers that case; set it explicitly for a different upstream, or to `[]` to
  prefix everything. `rewritePath` needs `rewrite` — `doctor` warns when it is
  set on a site that isn't a proxy.
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
- **`headers`** sets extra response headers on every request to the site —
  caching, CORS, anything — without putting a proxy in front of it. They are
  **merged into** the security headers zipgo already sends (`X-Frame-Options`,
  `X-Content-Type-Options`, `Referrer-Policy`, `X-XSS-Protection`,
  `Permissions-Policy`), not a replacement for them: an entry whose name matches
  one of those defaults overrides that one header and leaves the rest alone.
  Header names are case-insensitive (`cache-control` and `Cache-Control` are the
  same header), and an **empty value removes** a header instead of sending it
  blank:

  ```jsonc
  {
    "headers": {
      "Cache-Control": "public, max-age=31536000, immutable",  // add
      "Access-Control-Allow-Origin": "https://app.example.com",// CORS, no proxy
      "X-XSS-Protection": "1; mode=block",                     // override a default
      "Referrer-Policy": ""                                    // remove a default
    }
  }
  ```

  A `headers` value that isn't an object of strings, a name that isn't a valid
  header token, or a value containing a newline is a hard error — the same
  contract as any malformed `.zipgoconfig.json` (see below). `zipgo doctor`
  points at the file.

  Headers apply to every kind of site: on a `rewrite` proxy they override
  whatever the upstream sent, and on a `redirect` they ride along with the 301/302.
- **`password`** puts the whole site behind zipgo's login page — see
  [Password-protected sites](#password-protected-sites). You set it with
  `zipgo password set <host>` rather than by hand.
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

### Password-protected sites

One shared password in front of a whole site — a game in progress, a share
with a few people — with a real login page rather than the browser's
basic-auth dialog:

```bash
zipgo password set ballance.game.example.com --title "Ballance"   # prompts twice
zipgo deploy dist -d ballance.game.example.com                     # still locked
```

- **Every path** answers `401` with the same small login page until the
  visitor has a session: the index, every asset, `/sub-domains-meta`, a 404.
  Nothing of the site is served before the password. The page is generic —
  only its title changes — `noindex`, `no-store`, with a strict CSP.
- The right password sets a **session cookie** (`__Host-`, `Secure`,
  `HttpOnly`, `SameSite=Lax`) and sends the visitor back to the page they
  asked for. It lasts `sessionDays` (default **7**) and is **renewed while in
  use**, so someone who keeps coming back never sees the page again.
  `/__zipgo/logout` ends it.
- A **link can skip the page**: any URL with `?__zipgo_pwd=<password>`
  (e.g. `https://ballance.game.example.com/?__zipgo_pwd=…` in a kiosk or a
  Steam shortcut) is checked like the form, with the same throttle; the right
  password sets the session and `303`s to the same URL **without** the
  parameter, so it doesn't linger in the address bar or a `Referer`. GET/HEAD
  only; the site never sees the parameter. Mind that a password in a link can
  end up in browser history and proxy logs.
- **Changing the password logs everyone out.** Sessions are signed with a key
  derived from a server secret (kept next to the certificates, in Caddy's data
  dir: it survives restarts), the host and the password hash — and every new
  hash has a new salt. A cookie never opens another site.
- Wrong passwords are **throttled**: 10 a minute per visitor IP, 60 a minute
  per site, then `429` for the rest of the minute. Behind a TCP proxy, set
  [`ZIPGO_PROXY_PROTOCOL_ALLOW`](#behind-a-tcp-proxy-proxy-protocol) so zipgo
  sees real visitor IPs. Logins and throttling are logged.
- Only a **bcrypt hash** is stored, never the password. A `password` block whose
  hash is missing or unusable **fails closed**: the site stays locked and nobody
  gets in (`zipgo doctor` says so).
- A locked site is **HTTPS-only** (`allowHttp` is ignored), its responses get
  `Cache-Control: private, no-cache` and `X-Robots-Tag: noindex, nofollow`
  (override them with `headers`), and it is **left out of its parent's
  `/sub-domains-meta`**, so its title and description don't leak through a
  public feed. A `basicAuth` site is left out too.
- zipgo picks the site by `Host` header whatever the TLS SNI, and the check
  runs inside the host's own route, so a domain-fronted request (SNI of a
  public site, `Host` of a locked one) gets the login page as well.
- **Deploys keep the lock.** `zipgo deploy` never removes or overwrites a
  site's `password` (or `basicAuth`): the mirror leaves the server's
  `.zipgoconfig.json` alone, and a build that ships its own has the server's
  lock merged back in. A build can't unlock a site or change its password —
  `zipgo password` is the one way.
- Child subdomains are separate sites: each needs its own password.

See [`zipgo password`](#zipgo-password) for the commands.

### Back-office

A page to switch sites off and on from a phone, one per domain, behind the same
login page:

```bash
zipgo admin set admin.yourdomain.com --generate   # prints the password once
# → https://admin.yourdomain.com
```

<img src="./docs/admin.png" alt="zipgo back-office: the gabvdl.xyz tree" width="320">

- It manages **the site its folder is nested in and every subdomain under it**:
  `admin.yourdomain.com` (folder `yourdomain.com/admin./`) lists the apex and
  every sub and sub-sub domain. Put one at `dev./admin./` and it manages
  `*.dev.yourdomain.com` only.
- Each site gets a **switch**. Off writes `"enable": false` into the site's
  `.zipgoconfig.json` — exactly what [`zipgo off`](#zipgo-off--zipgo-on)
  writes — so the site answers 410 and its nested subdomains go with it (they
  show *off through …*). Every switch has an **Undo** for five seconds.
- A **tree** view (collapsible, the nesting at a glance) and a **list** view
  (one row per host, grouped by top-level label), a search box, and *On / Off
  / 🔒* filters. Each row shows the type (static, SPA, proxy, redirect), the
  lock, when the site was last deployed and its size, and opens the site.
- Tapping a site opens its details and its **password**: set one, generate
  one (shown once), change it or take it off — the `password` block that
  [`zipgo password`](#zipgo-password) writes, so deploys keep it too.
- It is **not a second source of truth**: it has no database, reads the folder
  tree on every request and writes `.zipgoconfig.json` files the watcher
  hot-reloads, so the page, the CLI and `zipgo ls` always agree. No upload and
  no delete: deploys stay `zipgo deploy`'s job.
- It **fails closed**: an `"admin": true` folder without a `password` answers
  `403` and serves nothing. It can't switch itself off, and it is **not**
  taken down when its parent is switched off — it is how the domain gets
  switched back on. It is never listed in `/sub-domains-meta`.
- Writes are same-origin JSON `POST`s only (the session cookie is
  `SameSite=Lax`, cross-site requests are refused), the page is `no-store`,
  `noindex`, framed by nobody, and sends no `Referer` to the sites it links to.
- When zipgo runs as root (the Docker image), a config it writes is handed to
  the folder's owner, so `zipgo off` and deploys over SSH can still replace it.

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
zipgo off       # switch a deployed site off (410 Gone, nested sites too)
zipgo on        # switch it back on
zipgo password  # put a site behind a login page, rotate or remove its password
zipgo admin     # set up a domain's back-office (a page to switch its sites off/on)
zipgo doctor    # check the local domains folder for broken sites
zipgo enable    # install and start the systemd user service
zipgo disable   # stop and remove the systemd user service
zipgo status    # show service status
zipgo version   # print the version
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

A deploy ships **files, never server state**: a site that is
[switched off](#zipgo-off--zipgo-on) stays off and a
[password-protected](#password-protected-sites) one stays locked. The mirror
leaves such a site's `.zipgoconfig.json` alone, and a build that ships its own
gets the switch and the lock merged back in (`🔒 password kept: the site stays
locked` in the output).

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

A deploy never flips a site's [on/off switch](#zipgo-off--zipgo-on): deploying
to a site that is off ships the files but leaves it answering 410, and says so
(`⏸ stays off … — zipgo on <host> to publish it`). Its remote
`.zipgoconfig.json` is kept out of the mirror, and if the build ships one of its
own, that one is merged in with `"enable": false` kept.

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
is `false` for a site that is [switched off](#zipgo-off--zipgo-on) — by its own
`"enable": false`, or through a parent's, in which case `offBy` names that
parent — `protected` is `true`
for a locked site — `password` is `true` when the lock is the login page, with
its `passwordTitle` and `sessionDays`, otherwise it is `basicAuth` (the hashes
themselves are never exposed) — and an
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

### `zipgo off` / `zipgo on`

Take a deployed site down without deleting it, and bring it back:

```console
$ zipgo off deck-tinder.dev.gabvdl.xyz
⏸  deck-tinder.dev.gabvdl.xyz is now off — it answers 410 Gone
   and 1 nested site with it: pb.deck-tinder.dev.gabvdl.xyz

$ zipgo on deck-tinder.dev.gabvdl.xyz
▶  deck-tinder.dev.gabvdl.xyz is back on
```

`off` writes `"enable": false` into the site's remote `.zipgoconfig.json`
(creating the file if needed, keeping every other key and their order); `on`
removes it again — and deletes the file if that was all it held, so a site
that had no config gets none back. The new file is renamed into place, so the
server's watcher never reloads a half-written one. Nothing restarts: the server
hot-reloads within a second.

While a site is off:

- every request to it answers a **410 Gone** page; it keeps its certificate (a
  folder that only holds subdomains had nothing to serve and stays that way);
- **its nested subdomains are off too** — `off` lists the ones it takes down;
- `zipgo ls` marks it `⏸ off` (or `⏸ off through <parent>`), `zipgo info`
  shows it in `Kind`, and `ls --json` reports `"enabled": false`;
- a `zipgo deploy` to it ships the files but **keeps it off** — `zipgo on` is
  the one way back.

```bash
zipgo off a.example.com b.example.com   # several at once
zipgo off a.example.com -n              # dry run: say what would change
zipgo on a.example.com --ssh me@box:/srv/domains
```

The target comes from `.zipgo.json`, like `ls`/`info`. `on` for a site whose
**parent** is off says so (`off through <parent>`) instead of pretending it is
live, and both refuse to rewrite a `.zipgoconfig.json` the server itself would
reject — fix that one by hand first. (`zipgo enable`/`disable` are something
else: they install and remove the systemd service.)

### `zipgo password`

Puts a site behind the [login page](#password-protected-sites), changes its
password, takes it off. It edits the `password` block of the site's
`.zipgoconfig.json` on the deploy target, over SSH — the target resolves like
`zipgo deploy`'s (`--ssh`, then the project's `package.json` `zipgo.target`,
then `.zipgo.json`) — or in a local domains folder with `--dir`. Every other key
of the file is kept. The password is hashed on your machine; only the bcrypt
hash travels.

```bash
zipgo password set ballance.game.example.com --title "Ballance"
#   New password for ballance.game.example.com: ••••••••
#   Again: ••••••••
# 🔒  ballance.game.example.com is now password-protected
#    login page: "Ballance" · sessions: 7 days, renewed while in use
#    deploy into it as usual — the lock is kept:  zipgo deploy <dir> -d ballance.game.example.com

zipgo password set <host>                    # change it (alias: rotate) — logs everyone out
echo -n "$PW" | zipgo password set <host> --stdin   # from a script or a secret store
zipgo password set <host> --generate         # make one up, print it once
zipgo password set <host> --keep-password --title "Ballance v2" --days 30
                                             # page/session settings only, sessions survive
zipgo password status                        # every locked site on the target
zipgo password status <host> [--json]        # one site (never the hash)
zipgo password remove <host> [--yes]         # make it public again
zipgo password hash [--stdin]                # just print a bcrypt hash
```

`set` creates the site folder when it doesn't exist yet, so a site can be locked
**before its first deploy** — it is never public, not even for a second. The
server hot-reloads: a new password takes effect, and old sessions die, within a
second.

### `zipgo admin`

Sets up a domain's [back-office](#back-office): writes `{"admin": true,
"password": {…}}` into the host's `.zipgoconfig.json`, on the deploy target or
in a local folder (`--dir`), with the same password flags as `zipgo password`.

```bash
zipgo admin set admin.yourdomain.com             # prompts twice
zipgo admin set admin.yourdomain.com --generate  # make one up, print it once
zipgo admin set admin.yourdomain.com --keep-password --days 30
zipgo admin remove admin.yourdomain.com --yes    # take the back-office down
```

Changing the password logs every open session out. `zipgo doctor` errors on an
admin folder without a password, or on the apex.

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
| A `password` hash that is missing or not a bcrypt hash — nobody can log in | error |
| A `password` that isn't an object, or an unknown key inside it | error / warning |
| `sessionDays` outside 1–400 (browsers cap cookies at 400 days) | warning |
| `password` and `basicAuth` both set — two prompts | warning |
| `allowHttp` on a password site — ignored, it is HTTPS-only | warning |
| `redirect` on a folder that still has an `index.html` (never served) | warning |
| A `404.html` in an SPA, `rewrite` or `redirect` site — it is never served | warning |
| `redirectStatus` without a `redirect` target | warning |
| `"enable": true` under a parent that is off — the cascade wins, it does nothing | warning |
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
| `ZIPGO_PROXY_PROTOCOL_ALLOW` | _(off)_ | Comma-separated CIDRs allowed to send a PROXY header (see below) |

If the domains folder contains no valid domain folders, zipgo automatically falls back to localhost mode.

### Behind a TCP proxy (PROXY protocol)

When zipgo sits behind a proxy that forwards TCP without terminating TLS
(Traefik with `tls.passthrough`, HAProxy in `mode tcp`), every request appears
to come from the proxy — access logs show one address for the whole internet.

Set `ZIPGO_PROXY_PROTOCOL_ALLOW` to the CIDRs the proxy dials from, and zipgo
reads the real client address from the PROXY header it prepends:

```bash
ZIPGO_PROXY_PROTOCOL_ALLOW=172.18.0.0/16
```

```yaml
# the Traefik side
tcp:
  services:
    zipgo-tcp:
      loadBalancer:
        proxyProtocol:
          version: 2
        servers:
          - address: "zipgo:443"
```

Unset (the default) the header is never honoured — accepting it from an
arbitrary client would let that client choose its own source address, which
defeats anything built on the client IP. Listed sources use go-proxyproto's
`USE` policy, which reads the header when present and is otherwise a no-op, so
you can enable this side first and switch the proxy over afterwards.

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
