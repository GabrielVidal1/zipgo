# The zipgo model

Self-hosting static sites on any number of domains should need nothing but a
folder tree: one binary, `mkdir`, `cp`, done — no config file, no dashboard, no
YAML, ever. The filesystem is already a perfectly good routing table: a folder
whose name has a dot is a domain, a folder ending in a dot is a subdomain, and
HTTPS just happens.

This page is that model, end to end — the folder tree, what host it resolves
to, and exactly what Caddy config zipgo builds from it. It's the one place
this is explained; if you only read one doc, read this one.

![zipgo architecture](./architecture.svg)

---

## Folder tree → hosts

A worked example. This is the whole domains folder (default `.zipgo/`,
override with `ZIPGO_DOMAINS_FOLDER`) for one domain, `example.com`:

```
.zipgo/
└── example.com/                   # domain folder — name must contain a dot
    ├── index.html                 # apex site: example.com
    ├── 404.html                   # apex's custom 404 body
    ├── docs./                     # trailing dot → subdomain
    │   ├── index.html             # docs.example.com
    │   └── api./                  # trailing dot again → recurses
    │       └── .zipgoconfig.json  # {"rewrite": "localhost:8080"}
    └── old./                      # another subdomain, no index.html needed
        └── .zipgoconfig.json      # {"redirect": "https://example.com",
                                    #  "redirectStatus": 301}
```

Three rules produced every row of the table below, and they're the entire
discovery algorithm (`internal/sites`, `Discover`):

1. **A domain folder's name must contain a dot.** One without a dot (or
   starting with `.`) is ignored — `config.ReadDomains` skips it.
2. **Inside a domain folder, any subdirectory whose name ends in a dot is a
   subdomain.** The label is the name with the trailing dot trimmed, and the
   rule applies recursively — `docs./api./` is two subdomain hops, not one
   folder named `docs./api.`. A folder that doesn't end in a dot is ordinary
   content of its parent site instead (served as a path under it), and is
   invisible to the recursion.
3. **The domain folder's own root is the apex site.** It has no label of its
   own; everything else is `<labels…>.<domain>`.

Resolved:

| Folder                              | Host                     | Type      | Notes                                                              |
| ------------------------------------ | ------------------------ | --------- | ------------------------------------------------------------------- |
| `example.com/`                       | `example.com`            | static    | apex; serves `index.html`; `404.html` is this site's 404 body       |
| `example.com/docs./`                 | `docs.example.com`       | static    | ordinary site one hop down                                          |
| `example.com/docs./api./`            | `api.docs.example.com`   | proxy     | `rewrite` in its `.zipgoconfig.json` → no `index.html` needed       |
| `example.com/old./`                  | `old.example.com`        | redirect  | `redirect` in its `.zipgoconfig.json` → no `index.html` needed      |

Two more rules that don't show up in this particular tree, but are worth
knowing before you hit them:

- **A site with an `assets/`, `static/`, `_next/` or `dist/` directory next to
  its `index.html` is auto-detected as an SPA** (`DetectSPA`) — no config key.
  All unmatched paths fall back to `index.html` instead of 404ing.
- **A `www.` subdomain folder makes the bare apex redirect to it.** If
  `example.com/www./` exists (and is enabled, with an `index.html`), a request
  to `example.com` gets a 308 to `https://www.example.com{path}` instead of
  being served directly — the one piece of routing that depends on a
  *sibling's* presence rather than a site's own folder or config.

The same tree in **localhost mode** (no valid domain folders present, or
`ZIPGO_LOCALHOST=1`) serves every site on one port with path routing instead of
host routing — `http://localhost:9000/example.com`,
`.../example.com/docs`, `.../example.com/docs/api` — parent segment before
child, deepest folder first so nested paths win. No TLS; this is the
`git clone && make run-local` path with zero setup.

---

## `.zipgoconfig.json` reference

Drop a `.zipgoconfig.json` file in any domain or subdomain folder to tweak how
that **one** site is served — it's read at discovery time
(`internal/sites`, `readConfig`) and applies to that folder alone, never to
its children. It is the one escape hatch in the model: everything else is the
folder tree. The file is never served to clients (the file server hides it,
same as any dot-suffixed subdomain folder), and a missing file is not an error
— the zero value is every default below. A malformed one (bad JSON, an unusable
value) is a **hard error**: it blocks that whole reload, so every site — not
just this one — silently keeps serving its last-good config until it's fixed.
`zipgo doctor` is the fast way to find which file it is.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `enable` | bool | `true` | `false` removes the site entirely — no route, no TLS subject, and it disappears from its parent's `/sub-domains-meta` listing. |
| `rewrite` | string | — | Reverse-proxies the site to this upstream instead of serving files — no `index.html` needed. A bare `host:port` dials plain HTTP; a `https://` URL proxies over TLS. |
| `rewritePath` | string | — | Prefixes every proxied request path, so a sub-path of the upstream (e.g. PocketBase's `/_/` admin UI) is served at this site's own root while the browser's URL stays put. Needs `rewrite`. |
| `rewritePathPassthrough` | string[] | `["/api"]` | Path prefixes that reach the upstream **unprefixed** even though `rewritePath` is set, because the upstream already serves them at its own root (the admin UI mounted under the prefix still calls those absolutely). Set `[]` to prefix everything. |
| `redirect` | string | — | Turns the host into a redirect instead of a file server — no `index.html` needed. A bare origin (`"https://elsewhere.example"`) keeps the request's path and query; a target with a path is used verbatim for every request. A bare path (`"/new"`) is rejected — it would redirect to itself. Wins over `rewrite` if both are set (`doctor` errors on that). |
| `redirectStatus` | int | `302` | Status for `redirect`: `301`/`308` permanent, `302`/`307` temporary. 302 by default so a redirect declared by a file is as easy to undo as deleting it — nothing gets cached forever in a browser. |
| `allowHttp` | bool | `false` | Also serve the site on plain HTTP (`:80`) instead of 301-redirecting it to HTTPS. No effect in localhost mode (already HTTP-only). |
| `headers` | map[string]string | — | Extra response headers, **merged into** the security headers zipgo already sends (`X-Content-Type-Options`, `Referrer-Policy`, `X-XSS-Protection`, `Permissions-Policy`, `X-Frame-Options`), not a replacement. An entry matching a default overrides just that one; names are case-insensitive; an **empty value deletes** a header instead of sending it blank. Applies to file, `rewrite` and `redirect` routes alike. |
| `basicAuth` | map[string]string | — | Puts the whole site behind HTTP basic auth — username → **bcrypt hash**, never plaintext (`caddy hash-password` or `htpasswd -nbB`). Covers static files, SPA fallback, a `rewrite` upstream, and the site's own `/sub-domains-meta` alike; the check runs before any of them. Per-site — a child subdomain needs its own entry (they can share a hash). |

Two conventions that aren't `.zipgoconfig.json` keys because the file's
presence already is the config:

- **`404.html`** in a static site's folder becomes the body of that site's
  404s (status stays 404 — only the body changes). Meaningless on an SPA
  (unknown paths get `index.html` + 200) or a `rewrite`/`redirect` site (they
  never reach the file server) — `doctor` warns when one sits there unused.
- **SPA detection** — `index.html` next to `assets/`, `static/`, `_next/` or
  `dist/` — as above.

A malformed `headers` value, an unknown key (a typo'd `"enabled"`), an
unusable `rewrite`/`redirect` target, a plaintext `basicAuth` password, and
every other way a `.zipgoconfig.json` can be wrong are caught by
`zipgo doctor` — see its check table in the [README](../README.md#zipgo-doctor).

---

## What Caddy gets

`zipgo` never writes a Caddyfile — `internal/builder` assembles a Caddy JSON
config in memory (`BuildConfig` for domain mode, `BuildLocalhostConfig` for
localhost) and hands it to the embedded `caddy.Run`. This is what the worked
example above turns into.

**TLS.** One `tls.automation` policy per run, with an exact-host `subjects`
list — every domain plus every non-apex site actually served (a disabled or
index-less site contributes no subject, so it never triggers an ACME request).
For the example tree: `example.com`, `docs.example.com`,
`api.docs.example.com`, `old.example.com`. In localhost mode the issuer is
`"internal"` instead (no ACME, no real TLS — plain HTTP on `127.0.0.1:9000`).

**Two HTTP servers.** An `https` server on `:443` carries every site's route,
matched on `host`. An `http_redirect` server on `:80` is a catch-all 301 to
the HTTPS URL for everything — *except* a route copied onto it for any site
that set `allowHttp: true`, which is served on both ports instead of bounced.

**One route per served site**, matched on `s.Host(domain)`, `terminal: true`.
A site is only routed at all when it's enabled *and* has something to serve —
an `index.html`, a `rewrite` upstream, or a `redirect` target
(`builder.served`); a folder with none of those produces no route and no TLS
subject. Inside the route, the handler is picked in this order:

1. **`redirect` set** → a `static_response` with the resolved `Location`
   header and status code. Wins if `rewrite` is also (wrongly) set.
2. **`rewrite` set** → a `reverse_proxy` to the dial address `rewrite`
   resolves to (host:port from a bare address, or from a URL's host with the
   scheme's default port filled in). TLS transport is added automatically for
   an `https://` upstream. `rewritePath`, when set, inserts one more `rewrite`
   handler ahead of the proxy that prepends the prefix to the request URI —
   skipped for a request that already starts with the prefix or one of
   `rewritePathPassthrough`, so the upstream's own root-absolute calls aren't
   double-prefixed.
3. **Neither** → a `file_server` rooted at the site's folder. `index_names`
   covers `index.html`/`index.htm`; the folder's own dot-suffixed subdomain
   folders and its `.zipgoconfig.json` are always in `hide`, so neither leaks
   into a directory listing or a request. An SPA gets a `try_files` match that
   rewrites any path with no matching file to `/index.html` instead (still a
   200). A static site with a `404.html` gets an `errors` route on the
   subroute: a `file_server` 404 is caught (`{http.error.status_code} == 404`,
   so only a 404 — a 403/500 is untouched), rewritten to `/404.html`, and
   re-served with `status_code: 404` forced — the body changes, the status
   doesn't, so `curl -f` still sees a miss.

Two things wrap *any* of the three, in this order:

- **Security headers**, always first: `X-Content-Type-Options: nosniff`,
  `Referrer-Policy: strict-origin-when-cross-origin`, `X-XSS-Protection: 0`,
  `Permissions-Policy: …`, and `X-Frame-Options: SAMEORIGIN` (replaced by a
  `Content-Security-Policy: frame-ancestors` directive when the site's
  `index.html` declares `<meta property="zipgo:authorized-origins">`). A
  site's `headers` map merges into this same handler — case-insensitively, an
  empty value deleting rather than blanking a header.
- **`basicAuth` guard**, outermost, only when set: an `authentication` handler
  with bcrypt accounts, checked before the redirect/proxy/file handler below
  it ever runs.

**`/sub-domains-meta`.** Any site with at least one direct child subdomain
(here: the apex, whose child is `docs.`, and `docs.`, whose child is `api.`)
gets one more route ahead of its own, matching `<host>/sub-domains-meta`. It's
a `static_response` serving a JSON object — one entry per direct child's
folder name (`"docs."`) mapping to that child's title/description/OpenGraph/
`zipgo:*` metadata, extracted from its `index.html` at config-build time
(`internal/meta`). It gets the same `basicAuth` guard as the parent site, so a
protected parent's child listing doesn't leak past the password.

**Everything rebuilds on a filesystem change.** A watcher on the domains
folder debounces edits and re-runs discovery + config build + `caddy.Run` with
the new config — nothing here is a one-time boot step. Copying a build into a
folder, editing a `.zipgoconfig.json`, or deleting a subdomain folder all take
effect without a restart.

Optional, orthogonal to routing: `ZIPGO_METRICS=1` adds a `metrics` server
serving Prometheus `/metrics` (loopback by default); `ZIPGO_LOG_FORMAT=json`
routes every content server's access log to one JSON line per request on
stdout. Neither changes what a site serves.

---

Deploying a build into this tree, the CLI (`deploy`/`ls`/`info`/`doctor`), and
`.zipgo.json`/`package.json` config-driven deploys are covered in the
[README](../README.md) — this page is the routing model, not the tooling
around it.
