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

// Config holds the optional per-site settings read from .zipgoconfig.json.
// Pointer fields distinguish "unset" (nil → use the default) from an explicit
// true/false in the file.
type Config struct {
	// Enable, when explicitly false, switches the site off: every request gets
	// a 410 Gone page instead of its content, it is excluded from any parent's
	// sub-domains-meta listing, and the same goes for its whole subtree of nested
	// subdomains (see Site.Disabled). Unset/true means the site is served as
	// normal — an explicit true cannot revive a site under a disabled parent.
	Enable *bool `json:"enable,omitempty"`
	// Rewrite, when non-empty, reverse-proxies the site to another upstream
	// instead of serving files from its folder. The value is an upstream
	// address: a bare host:port (e.g. "localhost:8080") or a URL with scheme
	// (e.g. "https://api.example.com").
	Rewrite string `json:"rewrite,omitempty"`
	// RewritePath, when non-empty, prefixes every proxied request path with this
	// value before it reaches the Rewrite upstream, so a site can expose a
	// sub-path of an upstream at its own root:
	//
	//	{"rewrite": "app-pocketbase:8090", "rewritePath": "/_/"}
	//
	// puts the PocketBase admin UI (served at /_/ upstream) at the site's root,
	// with the browser's URL staying on this host instead of redirecting. It is
	// only meaningful together with Rewrite; on a static or redirect site it is
	// ignored (doctor warns). A value without a leading slash gets one.
	RewritePath string `json:"rewritePath,omitempty"`
	// RewritePathPassthrough lists path prefixes that must reach the upstream
	// unprefixed even though RewritePath is set, because the upstream already
	// serves them at its root. The app under the prefix usually still calls
	// those (the PocketBase admin UI at /_/ fetches /api/... absolutely), so
	// without this they would be rewritten to /_/api/... and 404:
	//
	//	{"rewritePath": "/_/", "rewritePathPassthrough": ["/api"]}
	//
	// Unset means DefaultRewritePathPassthrough, which covers that PocketBase
	// case; set it explicitly (or to []) for a different upstream.
	RewritePathPassthrough []string `json:"rewritePathPassthrough,omitempty"`
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
	// Password, when present, puts the whole site behind zipgo's own login
	// page: one shared password, a signed session cookie, nothing served
	// before it. Unlike BasicAuth it has a real page (the browser's basic-auth
	// dialog can't be styled, saved by every password manager, or logged out
	// of), and it is what `zipgo password set` manages. The block alone locks
	// the site: a missing or unusable hash means nobody gets in, never that
	// the site is public.
	Password *Password `json:"password,omitempty"`
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
	// Admin, when true, turns the folder into the back-office of the site it
	// is nested in: a page listing that site and every subdomain under it,
	// with an on/off switch each (the same "enable" key `zipgo off` writes).
	// Put it in a folder of its own at the domain root —
	//
	//	gabvdl.xyz/zipgo./.zipgoconfig.json  {"admin": true, "password": {…}}
	//
	// — and https://zipgo.gabvdl.xyz manages the whole domain. It needs a
	// "password" (`zipgo admin set` writes both): without one the page is
	// never served. An admin site is not switched off by a parent, so turning
	// the domain off doesn't take its switchboard with it.
	Admin bool `json:"admin,omitempty"`
	// Flags are the site's feature flags (see Flag): switches its own code
	// reads from /__zipgo/flags.json to show or hide unfinished features, and
	// that close the feature's own paths while they are off. Like "enable"
	// and "password" they are server state — flipped from the back-office or
	// `zipgo flag`, never reset by a deploy (a build can declare new ones).
	Flags map[string]Flag `json:"flags,omitempty"`
	// Browse, when true, gives a folder of this static site that has no
	// index.html a directory listing. Off by default: a listing shows every
	// file a build left behind, and a static host should say nothing about
	// a path it has no page for. Only a static site lists (an SPA answers
	// index.html for every path).
	Browse *bool `json:"browse,omitempty"`
}

// DefaultRedirectStatus is the status code used for a "redirect" site that does
// not set "redirectStatus". 302 (temporary) is the safe default: browsers cache
// a 301 aggressively, and in a folder-tree config a redirect is as easy to undo
// as deleting a file — the routing shouldn't outlive the file.
const DefaultRedirectStatus = 302

// Enabled reports whether the site's own config leaves it on (default true).
// Whether it is actually served also depends on its parents: see Site.Disabled.
func (c Config) Enabled() bool { return c.Enable == nil || *c.Enable }

// RedirectCode returns the status code to use for the site's redirect,
// defaulting to DefaultRedirectStatus when unset.
func (c Config) RedirectCode() int {
	if c.RedirectStatus == 0 {
		return DefaultRedirectStatus
	}
	return c.RedirectStatus
}

// Password is the "password" block of a .zipgoconfig.json:
//
//	{"password": {"hash": "$2a$12$…", "title": "Ballance", "sessionDays": 7}}
type Password struct {
	// Hash is the bcrypt hash of the shared password, never the password.
	Hash string `json:"hash"`
	// Title is the heading of the login page (the only thing that differs
	// from one protected site to the next). Empty means the site's host.
	Title string `json:"title,omitempty"`
	// SessionDays is how long a login lasts without a visit. A session is
	// renewed while in use, so a visitor who comes back within this many days
	// stays logged in. Zero means DefaultSessionDays.
	SessionDays int `json:"sessionDays,omitempty"`
}

// DefaultSessionDays is how long a login lasts when "sessionDays" is unset. The
// session is renewed while it is in use, so this is really "how long a visitor
// can stay away before having to log in again".
const DefaultSessionDays = 7

// MaxSessionDays is the longest session a site can ask for. Browsers cap a
// cookie's lifetime at 400 days (Chrome since 2022), so anything longer would
// silently be cut there.
const MaxSessionDays = 400

// Days returns the session lifetime in days, defaulting and clamping
// "sessionDays" to [1, MaxSessionDays].
func (p Password) Days() int {
	switch {
	case p.SessionDays <= 0:
		return DefaultSessionDays
	case p.SessionDays > MaxSessionDays:
		return MaxSessionDays
	}
	return p.SessionDays
}

// HasPassword reports whether the site is behind zipgo's login page.
func (c Config) HasPassword() bool { return c.Password != nil }

// BrowseEnabled reports whether the site opted into directory listings
// (default false).
func (c Config) BrowseEnabled() bool { return c.Browse != nil && *c.Browse }

// HasBasicAuth reports whether the site is behind HTTP basic auth.
func (c Config) HasBasicAuth() bool { return len(c.BasicAuth) > 0 }

// Protected reports whether the site is locked at all — a login page, HTTP
// basic auth, or both. A protected site is never listed in its parent's
// sub-domains-meta, and a deploy never removes its lock.
func (c Config) Protected() bool { return c.HasPassword() || c.HasBasicAuth() }

// LockKeys are the .zipgoconfig.json keys that lock a site. They are server
// state, set with `zipgo password` (or by hand on the server), not part of a
// build: `zipgo deploy` carries them over instead of letting a pushed build —
// with no config, or with a config of its own — unlock the site.
var LockKeys = []string{"password", "basicAuth"}

// Locks returns the lock keys present in a raw .zipgoconfig.json, in LockKeys
// order. An empty or absent file has none; a malformed one is an error, since
// the caller can't tell whether it locks the site.
func Locks(raw []byte) (map[string]json.RawMessage, []string, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, nil, fmt.Errorf("parsing %s: %w", ConfigFileName, err)
	}
	locks := map[string]json.RawMessage{}
	var names []string
	for _, k := range LockKeys {
		if v, ok := m[k]; ok && string(v) != "null" {
			locks[k] = v
			names = append(names, k)
		}
	}
	return locks, names, nil
}

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
	// Disabled is true when the site is switched off: its own config sets
	// "enable": false, or a parent site's does. Disabling a site takes every
	// nested subdomain under it down too, so `pb.app.example.com` goes off with
	// `app.example.com` — a project and its backends are one switch.
	Disabled bool
}

// DefaultRewritePathPassthrough are the path prefixes left unprefixed when a
// site sets rewritePath without saying which paths the upstream owns at its
// root. "/api" covers the overwhelmingly common case this key exists for — a
// PocketBase admin UI mounted at the site root, whose JS calls /api/... — so the
// key does the useful thing with one line of config.
var DefaultRewritePathPassthrough = []string{"/api"}

// NormalizeRewritePath cleans a rewritePath value into the form the builder
// emits: empty stays empty, and anything else gets a leading slash and no
// trailing one ("_/" → "/_"), so it concatenates with a request path exactly
// once.
func NormalizeRewritePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	for len(p) > 1 && strings.HasSuffix(p, "/") {
		p = strings.TrimSuffix(p, "/")
	}
	if p == "/" {
		return ""
	}
	return p
}

// RewritePathPassthroughs returns the path prefixes that must bypass the
// rewritePath prepend, normalised the same way, falling back to
// DefaultRewritePathPassthrough when the key is unset. An explicit empty list in
// the file means "prefix everything" and is honoured.
func (c Config) RewritePathPassthroughs() []string {
	list := c.RewritePathPassthrough
	if list == nil {
		list = DefaultRewritePathPassthrough
	}
	out := make([]string, 0, len(list))
	for _, p := range list {
		if n := NormalizeRewritePath(p); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// HasNotFoundPage reports whether the site serves a custom 404 page: it has one
// on disk, and it is a site that can 404 at all. An SPA is excluded — every
// unknown path there is a client-side route and falls back to index.html — as
// are proxy and redirect sites, which never reach the file server.
func (s Site) HasNotFoundPage() bool {
	return s.NotFoundPage != "" && !s.IsSPA &&
		s.Config.Rewrite == "" && s.Config.Redirect == ""
}

// Off reports whether the site is switched off — by its own "enable": false or,
// through Disabled, by a parent's. Callers should use this rather than either
// field alone.
func (s Site) Off() bool { return s.Disabled || !s.Config.Enabled() }

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
	var walk func(dir string, labels []string, parentOff bool) error
	walk = func(dir string, labels []string, parentOff bool) error {
		cfg, err := readConfig(dir)
		if err != nil {
			return err
		}
		// An admin site ignores its parents' switch: it is how they get
		// switched back on.
		off := (parentOff && !cfg.Admin) || !cfg.Enabled()
		result = append(result, Site{
			Labels:       append([]string{}, labels...),
			Path:         dir,
			IsSPA:        DetectSPA(dir),
			NotFoundPage: DetectNotFound(dir),
			Config:       cfg,
			Disabled:     off,
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
			if err := walk(child, append([]string{label}, labels...), off); err != nil {
				return err
			}
		}
		return nil
	}

	if err := walk(domainDir, nil, false); err != nil {
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
	c, err := ParseConfig(data)
	if err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", filepath.Join(dir, ConfigFileName), err)
	}
	return c, nil
}

// ParseConfig decodes a .zipgoconfig.json body exactly as the server reads it,
// so a tool that edits the file remotely (`zipgo off`/`on`) can refuse one the
// server would reject instead of rewriting it.
func ParseConfig(data []byte) (Config, error) {
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, err
	}
	if err := ValidateHeaders(c.Headers); err != nil {
		return Config{}, err
	}
	if err := ValidateFlags(c.Flags); err != nil {
		return Config{}, err
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
