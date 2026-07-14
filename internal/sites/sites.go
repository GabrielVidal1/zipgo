package sites

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ConfigFileName is the per-site settings file. When present in a site folder
// it tweaks how that site is served. It is never served to clients (the
// file_server hides it).
const ConfigFileName = ".zipgoconfig.json"

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

// HTTPAllowed reports whether the site opts into being served over plain HTTP
// (default false).
func (c Config) HTTPAllowed() bool { return c.AllowHTTP != nil && *c.AllowHTTP }

type Site struct {
	// Labels is the subdomain label chain, leaf-first. Empty means the apex
	// domain. e.g. ["api", "docs"] → api.docs.<rootDomain>. A single label may
	// itself contain dots (folder "foo.bar." → label "foo.bar").
	Labels []string
	Path   string
	IsSPA  bool
	// Config is the per-site settings from .zipgoconfig.json (zero value when
	// the file is absent).
	Config Config
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
			Labels: append([]string{}, labels...),
			Path:   dir,
			IsSPA:  detectSPA(dir),
			Config: cfg,
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
	return c, nil
}

// detectSPA returns true when the dir has index.html + a bundler output dir.
// Covers Vite (assets/), CRA (static/), Next.js (_next/), generic (dist/).
func detectSPA(dir string) bool {
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
