package sites

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ConfigFileName is the per-site settings file. When present in a site folder
// it tweaks how that site is served. It is never served to clients (the
// file_server hides it).
const ConfigFileName = ".zipgoconfig.json"

// NotFoundFileName is the page a static site can drop in its folder to replace
// the default plain-text 404 body. It is served, with a 404 status, for any
// request that matches no file. An SPA never 404s (unknown paths fall back to
// index.html), so the file only means something for a static site.
const NotFoundFileName = "404.html"

// VersionsDirName is the hidden folder, at a site's own root, where `zipgo
// deploy` snapshots the previous content of that site before overwriting it, so
// `zipgo rollback` can swap an earlier deploy back in. It is never served (the
// file_server hides it) and is excluded from a site's size, sub-domains-meta and
// the deploy mirror. A leading dot keeps discovery from mistaking it for content
// (see Discover, which skips dot-prefixed entries).
const VersionsDirName = ".zipgo-versions"

// Config holds the optional per-site settings read from .zipgoconfig.json.
// Pointer fields distinguish "unset" (nil → use the default) from an explicit
// true/false in the file.
type Config struct {
	// Enable, when explicitly false, removes the site entirely: it is not
	// served and is excluded from any parent's sub-domains-meta listing.
	// Unset/true means the site is served as normal.
	Enable *bool `json:"enable,omitempty"`
	// Rewrite, when non-empty, reverse-proxies the site to another upstream
	// instead of serving files from its folder. The value is an upstream
	// address: a bare host:port (e.g. "localhost:8080") or a URL with scheme
	// (e.g. "https://api.example.com").
	Rewrite string `json:"rewrite,omitempty"`
	// Redirect, when non-empty, redirects every request for the site to another
	// absolute URL instead of serving files from its folder. When the value is a
	// bare origin ("https://elsewhere.com") the request's path and query are
	// preserved (/docs → https://elsewhere.com/docs); when it carries a path
	// ("https://elsewhere.com/moved") every request lands on that exact URL.
	Redirect string `json:"redirect,omitempty"`
	// RedirectStatus is the status code used for Redirect (301, 302, 307 or
	// 308). Unset means DefaultRedirectStatus (302) — a temporary redirect, so a
	// mistake isn't cached in browsers forever.
	RedirectStatus int `json:"redirectStatus,omitempty"`
	// AllowHTTP, when true, also serves the site over plain HTTP (port 80)
	// instead of redirecting to HTTPS. Has no effect in localhost mode.
	AllowHTTP *bool `json:"allowHttp,omitempty"`
	// BasicAuth, when non-empty, puts the whole site behind HTTP basic auth.
	// It maps a username to that user's *bcrypt hash* — never a plaintext
	// password (generate one with `caddy hash-password` or `htpasswd -nbB`).
	// It applies to static, SPA and rewrite (proxy) sites alike, and to the
	// site's own sub-domains-meta endpoint.
	BasicAuth map[string]string `json:"basicAuth,omitempty"`
	// Headers are extra response headers merged into the security-headers
	// handler zipgo already applies to every route (file and proxy alike), so a
	// site can set caching or CORS without a proxy in front:
	//
	//	{"headers": {"Cache-Control": "public, max-age=31536000, immutable"}}
	//
	// Names are canonicalised, so "cache-control" and "Cache-Control" are the
	// same header. An entry whose name matches one zipgo sends by default
	// overrides it; every other default is left alone. An empty value removes
	// the header instead of sending it blank.
	Headers map[string]string `json:"headers,omitempty"`
}

// DefaultRedirectStatus is the status code used for a "redirect" site that does
// not set "redirectStatus". 302 (temporary) is the safe default: browsers cache
// a 301 aggressively, and in a folder-tree config a redirect is as easy to undo
// as deleting a file — the routing shouldn't outlive the file.
const DefaultRedirectStatus = 302

// Enabled reports whether the site should be served (default true).
func (c Config) Enabled() bool { return c.Enable == nil || *c.Enable }

// RedirectCode returns the status code to use for the site's redirect,
// defaulting to DefaultRedirectStatus when unset.
func (c Config) RedirectCode() int {
	if c.RedirectStatus == 0 {
		return DefaultRedirectStatus
	}
	return c.RedirectStatus
}

// Protected reports whether the site is behind HTTP basic auth.
func (c Config) Protected() bool { return len(c.BasicAuth) > 0 }

// HTTPAllowed reports whether the site opts into being served over plain HTTP
// (default false).
func (c Config) HTTPAllowed() bool { return c.AllowHTTP != nil && *c.AllowHTTP }

// InvalidHeaderName returns a human reason when name cannot be used as an HTTP
// response header name, or "" when it is fine. A header name is a token
// (RFC 9110 §5.1): no spaces, no colon, no control or separator characters.
// Both the parser (a hard error, so a typo cannot reach Caddy) and doctor use
// this, so they always agree on what is valid.
func InvalidHeaderName(name string) string {
	if name == "" {
		return "empty header name"
	}
	for _, r := range name {
		if r <= ' ' || r >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={}", r) {
			return fmt.Sprintf("contains %q, which is not allowed in a header name", r)
		}
	}
	return ""
}

// InvalidHeaderValue returns a human reason when value cannot be sent as a
// header value, or "" when it is fine. The only hard rule is no CR/LF: a
// newline in a header value is a response-splitting injection.
func InvalidHeaderValue(value string) string {
	if strings.ContainsAny(value, "\r\n") {
		return "contains a newline (header injection)"
	}
	return ""
}

// ValidateHeaders checks every entry of a Headers map, returning the first
// problem found (names are checked in sorted order so the error is stable).
func ValidateHeaders(h map[string]string) error {
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	sort.Strings(names)
	// seen keys the names case-insensitively: header names are case-insensitive
	// on the wire, so two entries differing only in case are one header with two
	// values and which one wins would depend on Go's map iteration order.
	seen := map[string]string{}
	for _, name := range names {
		if bad := InvalidHeaderName(name); bad != "" {
			return fmt.Errorf("header %q: %s", name, bad)
		}
		if bad := InvalidHeaderValue(h[name]); bad != "" {
			return fmt.Errorf("header %q: %s", name, bad)
		}
		lower := strings.ToLower(name)
		if first, dup := seen[lower]; dup {
			return fmt.Errorf("header %q is also set as %q — header names are case-insensitive", name, first)
		}
		seen[lower] = name
	}
	return nil
}

type Site struct {
	// Labels is the subdomain label chain, leaf-first. Empty means the apex
	// domain. e.g. ["api", "docs"] → api.docs.<rootDomain>. A single label may
	// itself contain dots (folder "foo.bar." → label "foo.bar").
	Labels []string
	Path   string
	IsSPA  bool
	// NotFoundPage is the site's custom 404 page as it is named on disk (see
	// NotFoundFileName), or "" when the folder has none. The name is kept
	// verbatim rather than assumed, so the rewrite the builder emits points at
	// the file that actually exists.
	NotFoundPage string
	// Config is the per-site settings from .zipgoconfig.json (zero value when
	// the file is absent).
	Config Config
}

// HasNotFoundPage reports whether the site serves a custom 404 page: it has one
// on disk, and it is a site that can 404 at all. An SPA is excluded — every
// unknown path there is a client-side route and falls back to index.html — as
// are proxy and redirect sites, which never reach the file server.
func (s Site) HasNotFoundPage() bool {
	return s.NotFoundPage != "" && !s.IsSPA &&
		s.Config.Rewrite == "" && s.Config.Redirect == ""
}

// IsApex reports whether this site is served at the bare domain.
func (s Site) IsApex() bool { return len(s.Labels) == 0 }

// Host returns the fully-qualified host for this site under rootDomain.
func (s Site) Host(rootDomain string) string {
	if len(s.Labels) == 0 {
		return rootDomain
	}
	return strings.Join(s.Labels, ".") + "." + rootDomain
}

// LocalhostPath returns the path prefix used in localhost mode, parent-first:
// /<domain>/<outer>/.../<leaf>. The apex maps to /<domain>.
func (s Site) LocalhostPath(domain string) string {
	p := "/" + domain
	for i := len(s.Labels) - 1; i >= 0; i-- {
		p += "/" + s.Labels[i]
	}
	return p
}

// Discover walks a single domain directory. The directory root itself is the
// apex site; any subdirectory whose name ends in a dot is a subdomain whose
// label is the name with the trailing dot trimmed. This applies recursively, so
// docs./api./ becomes api.docs.<rootDomain>.
func Discover(domainDir string) ([]Site, error) {
	if _, err := os.Stat(domainDir); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("domain directory %q not found", domainDir)
		}
		return nil, fmt.Errorf("reading %s: %w", domainDir, err)
	}

	var result []Site
	var walk func(dir string, labels []string) error
	walk = func(dir string, labels []string) error {
		cfg, err := readConfig(dir)
		if err != nil {
			return err
		}
		result = append(result, Site{
			Labels:       append([]string{}, labels...),
			Path:         dir,
			IsSPA:        DetectSPA(dir),
			NotFoundPage: DetectNotFound(dir),
			Config:       cfg,
		})

		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("reading %s: %w", dir, err)
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if !strings.HasSuffix(e.Name(), ".") {
				continue
			}
			label := strings.TrimSuffix(e.Name(), ".")
			if label == "" {
				continue
			}
			child := filepath.Join(dir, e.Name())
			if err := walk(child, append([]string{label}, labels...)); err != nil {
				return err
			}
		}
		return nil
	}

	if err := walk(domainDir, nil); err != nil {
		return nil, err
	}
	return result, nil
}

// readConfig reads dir/.zipgoconfig.json. A missing file is not an error (the
// zero-value Config applies); a present-but-malformed file is, so a typo is
// surfaced rather than silently ignored.
func readConfig(dir string) (Config, error) {
	data, err := os.ReadFile(filepath.Join(dir, ConfigFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("reading %s: %w", filepath.Join(dir, ConfigFileName), err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", filepath.Join(dir, ConfigFileName), err)
	}
	if err := ValidateHeaders(c.Headers); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", filepath.Join(dir, ConfigFileName), err)
	}
	return c, nil
}

// DetectSPA returns true when the dir has index.html + a bundler output dir.
// Covers Vite (assets/), CRA (static/), Next.js (_next/), generic (dist/).
func DetectSPA(dir string) bool {
	bundleDirs := map[string]bool{"static": true, "assets": true, "_next": true, "dist": true}
	hasIndex, hasBundleDir := false, false
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if !e.IsDir() && name == "index.html" {
			hasIndex = true
		}
		if e.IsDir() && bundleDirs[name] {
			hasBundleDir = true
		}
	}
	return hasIndex && hasBundleDir
}

// DetectNotFound returns the name of the site's custom 404 page, or "" when the
// folder has none. The lookup is case-insensitive (a build tool may emit
// 404.HTML) but the name is returned as it is spelled on disk, because that is
// the path the file server will have to open.
func DetectNotFound(dir string) string {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.EqualFold(e.Name(), NotFoundFileName) {
			return e.Name()
		}
	}
	return ""
}
