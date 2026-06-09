package builder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

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

// dotHide returns the file_server hide patterns that prevent dot-suffixed
// subdomain folders from being served or listed. Every route should hide
// these, not just the apex, because subdomain folders can appear at any level.
func dotHide() []string {
	return []string{"*."}
}

// ---- domain mode -----------------------------------------------------------

// hasIndex reports whether the site directory contains an index.html. Sites
// without one are skipped entirely (no route, no TLS subject) rather than
// served as a directory listing.
func hasIndex(s sites.Site) bool {
	if _, err := os.Stat(filepath.Join(s.Path, "index.html")); err == nil {
		return true
	}
	return false
}

// hasWww reports whether the domain has a www subdomain site.
func hasWww(all []sites.Site) bool {
	for _, s := range all {
		if len(s.Labels) == 1 && s.Labels[0] == "www" && hasIndex(s) {
			return true
		}
	}
	return false
}

// BuildConfig serves every site on its host over HTTPS (Let's Encrypt).
// It supports multiple domains simultaneously.
func BuildConfig(domainSites []DomainSites) (*caddy.Config, error) {
	routes := arr{}
	var subjects []string

	for _, ds := range domainSites {
		hide := dotHide()
		www := hasWww(ds.Sites)
		subjects = append(subjects, ds.Domain)

		// If a www. subdomain folder exists, redirect bare domain → www.
		if www {
			routes = append(routes, obj{
				"match":    arr{obj{"host": arr{ds.Domain}}},
				"handle":   arr{obj{"handler": "static_response", "status_code": "308", "headers": obj{"Location": arr{"https://www." + ds.Domain + "{http.request.uri}"}}}},
				"terminal": true,
			})
		}

		for _, s := range ds.Sites {
			if !hasIndex(s) {
				continue
			}
			r, err := domainRoute(s, ds.Domain, hide)
			if err != nil {
				return nil, fmt.Errorf("domain %s host %s: %w", ds.Domain, s.Host(ds.Domain), err)
			}
			routes = append(routes, r)
			if !s.IsApex() {
				subjects = append(subjects, s.Host(ds.Domain))
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
			"tls": obj{"automation": obj{"policies": arr{obj{
				"subjects": subjects,
				"issuers": arr{obj{
					"module": "acme",
					"challenges": obj{
						"http": 		obj{"disabled": false},
						"tls-alpn":	obj{"disabled": true},
					},
				}},
			}}}},
		},
	}

	return finalize(cfg)
}

func domainRoute(s sites.Site, rootDomain string, hidePaths []string) (obj, error) {
	absPath, err := filepath.Abs(s.Path)
	if err != nil {
		return nil, err
	}

	return obj{
		"match":    arr{obj{"host": arr{s.Host(rootDomain)}}},
		"handle":   arr{fileHandler(absPath, s.IsSPA, "", hidePaths)},
		"terminal": true,
	}, nil
}

// ---- localhost mode --------------------------------------------------------

// BuildLocalhostConfig serves all sites on a single port (9000) using path
// routing: localhost:9000/<domain>/<subdomain>. The apex maps to
// localhost:9000/<domain> (no extra segment).
func BuildLocalhostConfig(domainSites []DomainSites) (*caddy.Config, error) {
	routes := arr{}

	for _, ds := range domainSites {
		hide := dotHide()
		www := hasWww(ds.Sites)

		// Sort by descending path depth so more specific (deeper) paths match
		// before shallower catch-alls.
		sorted := append([]sites.Site{}, ds.Sites...)
		sort.SliceStable(sorted, func(i, j int) bool {
			return len(sorted[i].Labels) > len(sorted[j].Labels)
		})

		// If a www. subdomain folder exists, redirect bare domain path → www path.
		if www {
			apexPath := "/" + ds.Domain
			wwwPath := apexPath + "/www"
			routes = append(routes, obj{
				"match": arr{obj{"path": arr{apexPath, apexPath + "/*"}}},
				"handle": arr{
					obj{"handler": "rewrite", "strip_path_prefix": apexPath},
					obj{
						"handler":     "static_response",
						"status_code": "308",
						"headers":     obj{"Location": arr{wwwPath + "{http.request.uri}"}},
					},
				},
				"terminal": true,
			})
		}

		for _, s := range sorted {
			if !hasIndex(s) {
				continue
			}
			pathPrefix := s.LocalhostPath(ds.Domain)

			absPath, err := filepath.Abs(s.Path)
			if err != nil {
				return nil, fmt.Errorf("domain %s host %s: %w", ds.Domain, s.LocalhostPath(ds.Domain), err)
			}

			routes = append(routes, obj{
				"match":    arr{obj{"path": arr{pathPrefix, pathPrefix + "/*"}}},
				"handle":   arr{fileHandler(absPath, s.IsSPA, pathPrefix, hide)},
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

// ---- shared file-serving handler -------------------------------------------

// fileHandler builds the subroute that serves a single site from root.
// When stripPrefix is non-empty (localhost mode) a strip_path_prefix rewrite is
// inserted so requests under /domain/name are served as if they were at root.
// SPA sites fall back to index.html for unmatched paths. hide lists absolute
// paths the file_server should hide from listing and serving (used to keep the
// apex from exposing its dot-suffixed subdomain folders).
func fileHandler(root string, isSPA bool, stripPrefix string, hide []string) obj {
	routes := arr{obj{"handle": arr{securityHeaders()}}}

	if stripPrefix != "" {
		routes = append(routes, obj{"handle": arr{obj{
			"handler":           "rewrite",
			"strip_path_prefix": stripPrefix,
		}}})
	}

	if isSPA {
		spaServer := obj{"handler": "file_server", "root": root, "index_names": arr{"index.html"}}
		fallbackServer := obj{"handler": "file_server", "root": root}
		if len(hide) > 0 {
			spaServer["hide"] = toArr(hide)
			fallbackServer["hide"] = toArr(hide)
		}
		routes = append(routes,
			obj{
				"match": arr{obj{"file": obj{
					"root":      root,
					"try_files": arr{"{http.request.uri.path}", "{http.request.uri.path}/index.html"},
				}}},
				"handle": arr{
					obj{"handler": "rewrite", "uri": "{http.matchers.file.relative}"},
					spaServer,
				},
			},
			obj{"handle": arr{
				obj{"handler": "rewrite", "uri": "/index.html"},
				fallbackServer,
			}},
		)
	} else {
		fs := obj{
			"handler":     "file_server",
			"root":        root,
			"index_names": arr{"index.html", "index.htm"},
			"browse":      obj{},
		}
		if len(hide) > 0 {
			fs["hide"] = toArr(hide)
		}
		routes = append(routes, obj{"handle": arr{fs}})
	}

	return obj{"handler": "subroute", "routes": routes}
}

func toArr(s []string) arr {
	out := make(arr, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
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
