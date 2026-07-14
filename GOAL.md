# GOAL — where zipgo is going

## North star

**Self-hosting static sites on any number of domains should need nothing but a
folder tree: one binary, `mkdir`, `cp`, done — no config file, no dashboard, no
YAML, ever.**

The bet is that the filesystem is already a perfectly good routing table, and
that every static host that asks you to *also* write a config (Caddyfile, nginx
vhosts, Netlify `_redirects`, a control panel) is charging you a tax for
something the directory layout already said. zipgo is "Caddy, but the tree is
the config": a folder whose name has a dot is a domain, a folder ending in a dot
is a subdomain, and HTTPS just happens. If a feature can't be expressed by
moving a file, it probably doesn't belong here.

Falsifiable version of "done": someone who has never read the docs can put a
site on a fresh VPS at `https://their.domain` in under two minutes, with one
`curl | sh` and one `cp -r`, and never open an editor.

## Target

Who this is for, in priority order:

- **Me (Gabriel)** — zipgo is the homelab's public edge. It serves every
  `*.gabvdl.xyz` site from raspy2, and every project's `npm run deploy` ends in
  `zipgo deploy`. It has to be boring, hot-reloading, and never the reason a
  site is down.
- **Homelab self-hosters with one box and several domains** — people who want
  Vercel-shaped ergonomics (push a build, get HTTPS) without a SaaS, an account,
  or a reverse-proxy config to maintain.
- **Anyone shipping a static build from CI** — `zipgo deploy dist/ -d host` over
  SSH should be all the "deploy step" a small project ever needs.

## Horizons

### Short term — v1.4 (now)

Sharpen the parts that are already used daily but still have rough edges:

- Deploy/inspection ergonomics: `zipgo ls`/`info` cover "what's live", but
  there's no way to see *why* a site isn't serving.
- Operability: when a site is broken (no `index.html`, malformed
  `.zipgoconfig.json`, cert failure), that must be visible from the CLI without
  reading logs.
- Documentation of the trailing-dot model as *the* mental model — one page,
  one diagram, no alternatives.

### Middle term — v2.0

The "two minutes on a fresh VPS" promise, end to end:

- Installer + systemd path is the blessed install (`curl | sh` → `zipgo enable`)
  and is tested on a clean machine, not just on raspy2.
- Rollback/versioned deploys — a deploy that breaks a site should be undoable
  without an rsync from the developer's laptop.
- Health and metrics good enough to run unattended: per-site up/down, cert
  expiry, and the Prometheus endpoint documented as a first-class feature.

### Long term — v3 / someday

zipgo is what people reach for when they say "I just want to host some static
files on my own box." A single binary that is genuinely trivial to explain, with
a healthy set of `.zipgoconfig.json` escape hatches (proxy, redirect, headers,
auth) for the 10% of sites that need one knob — but never a config file that
duplicates what the folder tree already encodes. Well-known enough that "put it
in a folder named `docs.`" is an obvious thing to say to a stranger.

## Wishlist

Ordered roughly by value. Each item is one session of work.

- [ ] `zipgo doctor` — check the domains folder and report per-site problems
      (missing `index.html`, malformed `.zipgoconfig.json`, folder name that
      isn't a valid domain, subdomain folder without a trailing dot that looks
      like it wanted one) with exit code 1 when anything is wrong.
- [ ] `zipgo ls --json` — machine-readable site listing (host, path, SPA/static,
      proxy target, size, mtime) so scripts and the homelab dashboards can
      consume it without scraping the human output.
- [ ] Redirect support in `.zipgoconfig.json` (`"redirect": "https://elsewhere"`,
      optional `"redirectStatus": 301|302`) — the one thing people currently have
      to fake with an `index.html` meta refresh.
- [ ] Custom headers in `.zipgoconfig.json` (`"headers": {"Cache-Control": "..."}`)
      merged into the security-headers handler, so a site can set caching or CORS
      without a proxy in front.
- [ ] Basic-auth in `.zipgoconfig.json` (`"basicAuth": {"user": "<bcrypt hash>"}`)
      — put a staging subdomain behind a password with one file.
- [ ] Serve a custom `404.html` when a non-SPA site has one, instead of Caddy's
      default plain-text 404.
- [ ] Structured access logs (JSON to stdout, one line per request with host,
      path, status, duration) behind a `ZIPGO_LOG_FORMAT=json` env var, so the
      homelab's Loki can ingest them.
- [ ] Unit tests for the discovery + builder core (`internal/sites`,
      `internal/builder`): the trailing-dot recursion, SPA detection,
      `enable:false`, and `rewrite` each get a table test. This is the safety net
      every later feature depends on.
- [ ] `zipgo deploy --prune` / dry-run summary that prints exactly which remote
      files would be added, replaced and deleted before touching anything.
- [ ] Per-site deploy history: keep the last N deploys under a hidden
      `.zipgo-versions/` folder and add `zipgo rollback <host>` to swap the
      previous one back in.
- [ ] Landing-page refresh — the generated index at the apex when no site is
      deployed should use the same `sub-domains-meta` metadata the API already
      computes, so it stops being a separate code path.
- [ ] Document the whole model in one page of `docs/` with the architecture SVG:
      folder tree → hosts table → what Caddy gets. Replace the scattered README
      sections with a link to it.

## Non-goals (for now)

- **Dynamic backends.** zipgo serves files and proxies to an upstream. It will
  never grow serverless functions, a database, or a build step.
- **A web UI / control panel.** The backoffice was removed on purpose. The CLI
  and the filesystem are the interface; a dashboard would be a second source of
  truth about what's deployed.
- **A config file at the root.** `.zipgo.json` holds *deploy targets* (client
  side), never routing. Routing lives in the tree, per-site tweaks live in a
  per-site `.zipgoconfig.json`. Anything that would reintroduce a global
  `sites:` list is out.
- **Being a general reverse proxy.** `rewrite` exists for the odd site backed by
  a container; zipgo is not competing with Traefik.
- **Multi-tenant / multi-user hosting.** One operator, one box, their domains.

## Guard rails (for the goal-keeper)

- One wishlist item per run, finished end-to-end: implement, `make build`,
  `make format`, and actually exercise it (`make run-local` against a scratch
  domains folder) before checking the box.
- **Never cut a release or push a `v*` tag** — the GitHub Actions release
  workflow builds binaries that the installer serves to real machines. Leave
  tagging to a human.
- **Never deploy to raspy2** and never restart the live zipgo container. Public
  `*.gabvdl.xyz` sites depend on it; deployment is a human step.
- Keep backward compatibility with the existing folder convention and with the
  fully-explicit `zipgo deploy dist/ -d host --ssh …` form — homelab projects'
  deploy scripts call it.
- Don't edit `domains/zipgo.xyz/install/{linux,macos,windows}.sh` — they are
  generated; edit `scripts/parts/` and run `make build-install-scripts`.
- Prefer adding to `.zipgoconfig.json` over adding new env vars or CLI flags:
  per-site behaviour belongs next to the site.
