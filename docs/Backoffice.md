# Backoffice

The backoffice is a password-protected web UI for deploying and managing sites without touching the filesystem directly.

![zipgo backoffice](./backoffice.png)

## Accessing it

| Mode      | URL                                 |
| --------- | ----------------------------------- |
| Domain    | `https://backoffice.yourdomain.com` |
| Localhost | `http://localhost:8999`             |

Your browser will prompt for a username and password (HTTP Basic Auth). The credentials are set via the `ZIPGO_USER` and `ZIPGO_PASS` environment variables — or in `/etc/zipgo/env` if you used `make install`.

## Deploying a site

1. Enter a **site name** — this becomes the subdomain (e.g. `blog` → `blog.yourdomain.com`). Use `root` to serve the apex domain itself.
2. Choose a **ZIP file** of your build output, or a single `.html` file.
3. Click **Deploy**.

The site is extracted into `apps/<name>/`, and Caddy's routing config is reloaded immediately — no restart needed. If the ZIP has a single top-level folder (e.g. you zipped a `dist/` directory), that wrapper folder is stripped automatically.

Uploading to an existing site name **replaces** it entirely.

## Live sites table

The **Live sites** section lists every deployed site with its type (static or SPA), file count, and last-modified time. Each row has:

- **↗ Open** — opens the site in a new tab (only shown once the URL is known)
- **Delete** — removes the site directory and reloads Caddy

## Site types

| Badge    | Meaning                                                   |
| -------- | --------------------------------------------------------- |
| `STATIC` | Standard file server — each URL maps directly to a file   |
| `SPA`    | Single-page app — unknown paths fall back to `index.html` |

SPA mode is detected automatically: a site is treated as a SPA when it contains `index.html` **and** one of the standard bundler output directories (`assets/`, `static/`, `_next/`, `dist/`).
