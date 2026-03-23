# Multi-Domain Support

## Goal

Restructure the site storage layout from a single-domain `apps/` directory to a multi-domain `domains/` directory, where each subdirectory is a domain name and each of its subdirectories is a subdomain site.

## New Directory Structure

```
domains/
  zipgo.xyz/
    root/        → https://zipgo.xyz
    install/     → https://install.zipgo.xyz
    docs/        → https://docs.zipgo.xyz
    demo/        → https://demo.zipgo.xyz
  otherdomain.com/
    root/        → https://otherdomain.com
    blog/        → https://blog.otherdomain.com
```

- **No more `root.txt`** — the domain is derived from the folder name.
- **Multiple domains** can be served simultaneously from one zipgo instance.
- **Localhost mode** activates when `domains/` is empty (no domain folders).

---

## File Changes

### `internal/config/config.go`

**Before:** `ReadRootDomain(appsDir string) (string, error)` — reads `apps/root.txt`.

**After:** `ReadDomains(domainsDir string) ([]string, error)` — scans `domains/` for subdirectory names. Each subdirectory name must contain a dot (basic domain validation). Returns an empty slice when the directory is missing or empty (= localhost mode).

---

### `internal/builder/builder.go`

**New type:**
```go
type DomainSites struct {
    Domain string
    Sites  []sites.Site
}
```

**Changed signatures:**

| Before | After |
|--------|-------|
| `IsLocalhost(rootDomain string) bool` | `IsLocalhost(domains []string) bool` |
| `BuildConfig(rootDomain string, discovered []sites.Site, backofficeAddr string)` | `BuildConfig(domainSites []DomainSites, backofficeAddr string)` |
| `BuildLocalhostConfig(discovered []sites.Site, backofficeAddr string)` | `BuildLocalhostConfig(domainSites []DomainSites, backofficeAddr string)` |

**`BuildConfig` changes:**
- TLS subjects now include all domains and their wildcards: `["domain1.com", "*.domain1.com", "domain2.com", "*.domain2.com", ...]`
- Backoffice responds at `backoffice.<domain>` for **every** configured domain.
- Analytics (Vince) responds at `analytics.<domain>` for every domain.
- Site routes are generated per domain.
- Landing page injection happens per domain independently.

**`BuildLocalhostConfig` changes:**
- Uses a **single port** (9000) instead of one port per site.
- Sites are routed by path prefix: `localhost:9000/<domain>/<subdomain>`.
- The `root` subdomain maps to `localhost:9000/<domain>` (no extra path segment).
- Backoffice stays on port 8999, Vince on 8898 (unchanged).
- SPA fallback rewrite is scoped to the path prefix (strips prefix before serving files).

**Example localhost URLs:**
```
http://localhost:9000/zipgo.xyz          → domains/zipgo.xyz/root/
http://localhost:9000/zipgo.xyz/install  → domains/zipgo.xyz/install/
http://localhost:9000/zipgo.xyz/docs     → domains/zipgo.xyz/docs/
http://localhost:9000/otherdomain.com    → domains/otherdomain.com/root/
http://localhost:8999                    → backoffice
```

**Caddy config approach:** A single `localhost:9000` server with one route per site, each matching on a path prefix matcher, followed by a `strip_prefix` rewrite before the file server handler.

---

### `internal/backoffice/backoffice.go`

**`Handler` signature:**

| Before | After |
|--------|-------|
| `Handler(appsDir, username, password string, onReload func() error, urlFor func(string) string, vinceURL, rootDomain string)` | `Handler(domainsDir, username, password string, onReload func() error, urlFor func(string, string) string, vinceURL string)` |

**Struct changes:**
- `appsDir string` → `domainsDir string`
- `rootDomain string` removed (domain is now per-site, not global)
- `urlFor func(string) string` → `urlFor func(domain, name string) string`

**`handleIndex`:** Iterates all domain folders in `domainsDir`, discovers sites per domain, and populates a flat `[]siteInfo` list with a `Domain` field on each entry.

**`handleUpload`:** Reads a `domain` field from the form. Deploys to `domains/<domain>/<siteName>/`.

**`handleDelete`:** Reads `domain` + `name` from form. Removes `domains/<domain>/<siteName>/`.

**`siteDataDomain`:** Now takes `domain` parameter instead of using `bo.rootDomain`.

**New `pageData` fields:**
```go
type pageData struct {
    Sites   []siteInfo
    Domains []string  // available domain names for the upload selector
    Flash   string
    IsError bool
}

type siteInfo struct {
    Domain  string    // new
    Name    string
    IsSPA   bool
    Files   int
    ModTime string
    URL     string
}
```

---

### `internal/backoffice/template.html`

- **Site list:** New `Domain` column added before `Name`. Grid changes from `2fr 76px 72px 1fr 110px` to `1fr 2fr 76px 72px 1fr 110px`.
- **Delete form:** Hidden `<input name="domain">` field added.
- **Deploy modal:** New `<select name="domain">` field populated from `{{.Domains}}`, above the site name input.
- **JS search:** `applyFilters` also matches against `row.dataset.domain`.

---

### `main.go`

- `appsDir` → `domainsDir` (default `"domains"`, overridable via `os.Args[1]`).
- `config.ReadRootDomain` → `config.ReadDomains` returning `[]string`.
- New `discoverAll()` helper: loops over domain names, calls `sites.Discover(domainsDir/<domain>)` for each, builds `[]builder.DomainSites`.
- `reload` calls `discoverAll()` then `BuildConfig` or `BuildLocalhostConfig`.
- `urlFor` signature changes to `func(domain, name string) string`.
  - In localhost mode returns `http://localhost:9000/<domain>/<name>` (or `http://localhost:9000/<domain>` for `root`).
- Vince URL uses the first configured domain (or localhost fallback).
- Startup summary prints per-domain site listings with path-based localhost URLs.

---

### `Makefile`

```makefile
# Before
APPS_DIR := $(abspath apps)
build-install-scripts:
    bash scripts/populate_script.sh apps/install

# After
DOMAINS_DIR := $(abspath domains)
build-install-scripts:
    bash scripts/populate_script.sh domains/zipgo.xyz/install
```

---

### `scripts/parts/03_download_and_setup.sh`

**Before:** Creates `apps/` and an empty `apps/root.txt`.

**After:** Creates `domains/` only. No `root.txt`. To configure a domain the user creates a folder, e.g. `domains/example.com/`.

---

## Physical File Migration

```bash
mkdir -p domains/zipgo.xyz
mv apps/root apps/install apps/docs apps/demo apps/example domains/zipgo.xyz/
rm apps/root.txt
rmdir apps
```

---

## Localhost Mode

Localhost mode is activated when `domains/` is **empty** (no subdirectories). In this mode:
- No sites are served (backoffice accessible on port 8999).
- Use it for initial setup before configuring a domain.

When domain folders exist, localhost mode uses a **single port with path routing**:

| URL | Serves |
|-----|--------|
| `http://localhost:9000/<domain>` | `domains/<domain>/root/` |
| `http://localhost:9000/<domain>/<sub>` | `domains/<domain>/<sub>/` |
| `http://localhost:8999` | backoffice |
| `http://localhost:8898` | Vince analytics |

This replaces the old per-site sequential port scheme (`9000`, `9001`, `9002`, ...).

To start serving sites, create a domain folder and restart:
```bash
mkdir -p domains/mysite.com/root
# then restart zipgo
```
