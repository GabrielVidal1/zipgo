package builder

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"zipgo/internal/landing"
	"zipgo/internal/sites"

	"github.com/caddyserver/caddy/v2"
)

// obj and arr are shorthand for the untyped JSON shapes we assemble before
// marshalling the whole config in one pass.
type (
	obj = map[string]any
	arr = []any
)

// ---- public helpers --------------------------------------------------------

// DomainSites pairs a domain name with its discovered sites.
type DomainSites struct {
	Domain string
	Sites  []sites.Site
}

// IsLocalhost reports whether we are in localhost mode (no domains configured).
func IsLocalhost(domains []string) bool { return len(domains) == 0 }

// LocalhostStartPort is the single port used for all sites in localhost mode.
const LocalhostStartPort = 9000

// landingDir is where the auto-generated landing page is written.
const landingDir = "/tmp/zipgo-landing"

// ---- domain mode -----------------------------------------------------------

// BuildConfig serves every site on its subdomain over HTTPS (Let's Encrypt).
// It supports multiple domains simultaneously.
func BuildConfig(domainSites []DomainSites) (*caddy.Config, error) {
	for i := range domainSites {
		ds := &domainSites[i]
		domainLandingDir := landingDir + "-" + ds.Domain
		ds.Sites = injectLanding(ds.Sites, func(name string) string {
			for _, s := range ds.Sites {
				if s.Name == name {
					return "https://" + s.Host(ds.Domain)
				}
			}
			return ""
		}, domainLandingDir)
	}

	routes := arr{}
	for _, ds := range domainSites {
		for _, s := range ds.Sites {
			r, err := domainRoute(s, ds.Domain)
			if err != nil {
				return nil, fmt.Errorf("domain %s site %s: %w", ds.Domain, s.Name, err)
			}
			routes = append(routes, r)
		}
	}

	// TLS subjects for all configured domains and their wildcards.
	// Also add *.parent.domain for each top-level subdomain that has sub-subdomains.
	var subjects []string
	for _, ds := range domainSites {
		subjects = append(subjects, ds.Domain, "*."+ds.Domain)
		seen := map[string]bool{}
		for _, s := range ds.Sites {
			if s.Parent != "" && !seen[s.Parent] {
				seen[s.Parent] = true
				subjects = append(subjects, "*."+s.Parent+"."+ds.Domain)
			}
		}
	}

	cfg := obj{
		"logging": obj{"logs": obj{"default": obj{"level": "ERROR"}}},
		"admin":   obj{"disabled": true},
		"apps": obj{
			"http": obj{"servers": obj{
				"https": obj{
					"listen": arr{":443"},
					"routes": routes,
				},
				"http_redirect": obj{
					"listen": arr{":80"},
					"routes": arr{obj{"handle": arr{obj{
						"handler":     "static_response",
						"status_code": "301",
						"headers":     obj{"Location": arr{"https://{http.request.host}{http.request.uri}"}},
					}}}},
				},
			}},
			"tls": obj{"automation": obj{"policies": arr{obj{"subjects": subjects}}}},
		},
	}

	return finalize(cfg)
}

func domainRoute(s sites.Site, rootDomain string) (obj, error) {
	absPath, err := filepath.Abs(s.Path)
	if err != nil {
		return nil, err
	}

	var match obj
	if s.Name == "root" {
		match = obj{"host": arr{rootDomain}, "path": arr{"/"}}
	} else {
		match = obj{"host": arr{s.Host(rootDomain)}}
	}

	return obj{
		"match":    arr{match},
		"handle":   arr{fileHandler(absPath, s.IsSPA, "")},
		"terminal": true,
	}, nil
}

// ---- localhost mode --------------------------------------------------------

// BuildLocalhostConfig serves all sites on a single port (9000) using path
// routing: localhost:9000/<domain>/<subdomain>.
// The "root" subdomain maps to localhost:9000/<domain> (no extra segment).
func BuildLocalhostConfig(domainSites []DomainSites) (*caddy.Config, error) {
	// Inject landing per domain with path-based URLs.
	for i := range domainSites {
		ds := &domainSites[i]
		domainLandingDir := landingDir + "-" + ds.Domain
		ds.Sites = injectLanding(ds.Sites, func(name string) string {
			prefix := "/" + ds.Domain
			if name != "root" {
				prefix += "/" + name
			}
			return fmt.Sprintf("http://localhost:%d%s", LocalhostStartPort, prefix)
		}, domainLandingDir)
	}

	routes := arr{}

	// Build one route per site. Non-root sites come first (more specific paths)
	// so they are matched before the root catch-all for each domain.
	for _, ds := range domainSites {
		var nonRoot, rootSites []sites.Site
		for _, s := range ds.Sites {
			if s.Name == "root" {
				rootSites = append(rootSites, s)
			} else {
				nonRoot = append(nonRoot, s)
			}
		}
		for _, s := range append(nonRoot, rootSites...) {
			pathPrefix := "/" + ds.Domain
			if s.Parent != "" {
				pathPrefix += "/" + s.Parent + "/" + s.Name
			} else if s.Name != "root" {
				pathPrefix += "/" + s.Name
			}

			absPath, err := filepath.Abs(s.Path)
			if err != nil {
				return nil, fmt.Errorf("domain %s site %s: %w", ds.Domain, s.Name, err)
			}

			routes = append(routes, obj{
				"match":    arr{obj{"path": arr{pathPrefix, pathPrefix + "/*"}}},
				"handle":   arr{fileHandler(absPath, s.IsSPA, pathPrefix)},
				"terminal": true,
			})
		}
	}

	cfg := obj{
		"logging": obj{"logs": obj{"default": obj{"level": "ERROR"}}},
		"admin":   obj{"disabled": true},
		"apps": obj{
			"http": obj{"servers": obj{
				"sites": obj{
					"listen": arr{fmt.Sprintf("127.0.0.1:%d", LocalhostStartPort)},
					"routes": routes,
				},
			}},
			"tls": obj{"automation": obj{"policies": arr{obj{"issuers": arr{obj{"module": "internal"}}}}}},
		},
	}

	return finalize(cfg)
}

// ---- landing injection -----------------------------------------------------

func injectLanding(discovered []sites.Site, urlFor func(string) string, destDir string) []sites.Site {
	if HasRootSite(discovered) {
		return discovered
	}
	if _, err := landing.Generate(discovered, urlFor, destDir); err != nil {
		return discovered
	}
	return append([]sites.Site{{
		Name:  "root",
		Path:  destDir,
		IsSPA: false,
	}}, discovered...)
}

func HasRootSite(discovered []sites.Site) bool {
	for _, s := range discovered {
		if s.Name == "root" {
			return true
		}
	}
	return false
}

// ---- shared file-serving handler -------------------------------------------

// fileHandler builds the subroute that serves a single site from root.
// When stripPrefix is non-empty (localhost mode) a strip_path_prefix rewrite is
// inserted so requests under /domain/name are served as if they were at root.
// SPA sites fall back to index.html for unmatched paths.
func fileHandler(root string, isSPA bool, stripPrefix string) obj {
	routes := arr{obj{"handle": arr{securityHeaders()}}}

	if stripPrefix != "" {
		routes = append(routes, obj{"handle": arr{obj{
			"handler":           "rewrite",
			"strip_path_prefix": stripPrefix,
		}}})
	}

	if isSPA {
		routes = append(routes,
			obj{
				"match": arr{obj{"file": obj{
					"root":      root,
					"try_files": arr{"{http.request.uri.path}", "{http.request.uri.path}/index.html"},
				}}},
				"handle": arr{
					obj{"handler": "rewrite", "uri": "{http.matchers.file.relative}"},
					obj{"handler": "file_server", "root": root, "index_names": arr{"index.html"}},
				},
			},
			obj{"handle": arr{
				obj{"handler": "rewrite", "uri": "/index.html"},
				obj{"handler": "file_server", "root": root},
			}},
		)
	} else {
		routes = append(routes, obj{"handle": arr{obj{
			"handler":     "file_server",
			"root":        root,
			"index_names": arr{"index.html", "index.htm"},
			"browse":      obj{},
		}}})
	}

	return obj{"handler": "subroute", "routes": routes}
}

func securityHeaders() obj {
	return obj{
		"handler": "headers",
		"response": obj{"set": obj{
			"X-Content-Type-Options": arr{"nosniff"},
			"X-Frame-Options":        arr{"SAMEORIGIN"},
			"Referrer-Policy":        arr{"strict-origin-when-cross-origin"},
			"X-XSS-Protection":       arr{"0"},
			"Permissions-Policy":     arr{"camera=(), microphone=(), geolocation=(), payment=()"},
		}},
	}
}

// ---- helpers ---------------------------------------------------------------

// finalize marshals the assembled config and unmarshals it into caddy.Config.
func finalize(cfg any) (*caddy.Config, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal caddy config: %w", err)
	}
	var c caddy.Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("caddy config error: %w\n\nJSON was:\n%s", err, raw)
	}
	return &c, nil
}
