package builder

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"zipgo/internal/landing"
	"zipgo/internal/sites"
)

// ---- public helpers --------------------------------------------------------

// DomainSites pairs a domain name with its discovered sites.
type DomainSites struct {
	Domain string
	Sites  []sites.Site
}

// IsLocalhost reports whether we are in localhost mode (no domains configured).
func IsLocalhost(domains []string) bool { return len(domains) == 0 }

// BackofficeHost returns the hostname used for the backoffice in domain mode.
func BackofficeHost(rootDomain string) string { return "backoffice." + rootDomain }

// VinceHost returns the hostname used for the Vince analytics UI in domain mode.
func VinceHost(rootDomain string) string { return "analytics." + rootDomain }

// LocalhostStartPort is the single port used for all sites in localhost mode.
const LocalhostStartPort = 9000

// BackofficeLocalhostPort is the backoffice port in localhost mode.
const BackofficeLocalhostPort = LocalhostStartPort - 1

// vinceInternalAddr is the loopback address where the Vince sidecar listens.
// This is fixed and shared between main.go (subprocess) and builder (proxy config).
const vinceInternalAddr = "127.0.0.1:8899"

// vinceLocalhostProxyPort is the localhost port Caddy listens on in localhost
// mode and proxies to the internal Vince address.
const vinceLocalhostProxyPort = 8898

// landingDir is where the auto-generated landing page is written.
const landingDir = "/tmp/zipgo-landing"

// backofficeAllowedIPsEnv is the env var for comma-separated CIDRs allowed to
// reach the backoffice. When unset every IP is allowed (backwards-compatible).
// Example: ZIPGO_BO_ALLOW=203.0.113.42/32,192.168.1.0/24
const backofficeAllowedIPsEnv = "ZIPGO_BO_ALLOW"

// allowedBackofficeRanges reads ZIPGO_BO_ALLOW and returns a slice of CIDR
// strings. Returns nil when the variable is unset or empty (= allow all).
func allowedBackofficeRanges() []string {
	raw := strings.TrimSpace(os.Getenv(backofficeAllowedIPsEnv))
	if raw == "" {
		return nil
	}
	var ranges []string
	for _, r := range strings.Split(raw, ",") {
		r = strings.TrimSpace(r)
		if r != "" {
			ranges = append(ranges, r)
		}
	}
	return ranges
}

// ---- domain mode -----------------------------------------------------------

// BuildConfig serves every site on its subdomain over HTTPS (Let's Encrypt).
// It supports multiple domains simultaneously.
func BuildConfig(domainSites []DomainSites, backofficeAddr string) (*caddy.Config, error) {
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

	routesJSON, err := domainRoutes(domainSites, backofficeAddr)
	if err != nil {
		return nil, err
	}

	// TLS subjects for all configured domains and their wildcards.
	// Also add *.parent.domain for each top-level subdomain that has sub-subdomains.
	var allSubjects []string
	for _, ds := range domainSites {
		allSubjects = append(allSubjects, ds.Domain, "*."+ds.Domain)
		seen := map[string]bool{}
		for _, s := range ds.Sites {
			if s.Parent != "" && !seen[s.Parent] {
				seen[s.Parent] = true
				allSubjects = append(allSubjects, "*."+s.Parent+"."+ds.Domain)
			}
		}
	}
	subjects, _ := json.Marshal(allSubjects)

	raw := fmt.Sprintf(`{
		"logging": {
			"logs": {
				"default": {"level": "ERROR"}
			}
		},
		"admin": {"disabled": true},
		"apps": {
			"http": {
				"servers": {
					"https": {
						"listen": [":443"],
						"routes": %s
					},
					"http_redirect": {
						"listen": [":80"],
						"routes": [{"handle": [{"handler": "static_response", "status_code": "301",
							"headers": {"Location": ["https://{http.request.host}{http.request.uri}"]}}]}]
					}
				}
			},
			"tls": {"automation": {"policies": [{"subjects": %s}]}}
		}
	}`, routesJSON, subjects)

	return unmarshal(raw)
}

func domainRoutes(domainSites []DomainSites, backofficeAddr string) (string, error) {
	var parts []string

	// Collect backoffice and vince hosts across all domains.
	var boHosts, vinceHosts []string
	for _, ds := range domainSites {
		boHosts = append(boHosts, BackofficeHost(ds.Domain))
		vinceHosts = append(vinceHosts, VinceHost(ds.Domain))
	}
	boHostsJSON, _ := json.Marshal(boHosts)
	boAddrJSON, _ := json.Marshal(backofficeAddr)

	allowedRanges := allowedBackofficeRanges()
	if len(allowedRanges) > 0 {
		allowJSON, _ := json.Marshal(allowedRanges)
		parts = append(parts, fmt.Sprintf(`{
			"match": [{"host": %s, "not": [{"remote_ip": {"ranges": %s}}]}],
			"handle": [{"handler": "static_response", "status_code": "403", "body": "Forbidden"}],
			"terminal": true
		}`, boHostsJSON, allowJSON))
	}
	parts = append(parts, fmt.Sprintf(`{
		"match": [{"host": %s}],
		"handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": %s}]}],
		"terminal": true
	}`, boHostsJSON, boAddrJSON))

	vinceHostsJSON, _ := json.Marshal(vinceHosts)
	vinceAddrJSON, _ := json.Marshal(vinceInternalAddr)
	parts = append(parts, fmt.Sprintf(`{
		"match": [{"host": %s}],
		"handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": %s}]}],
		"terminal": true
	}`, vinceHostsJSON, vinceAddrJSON))

	for _, ds := range domainSites {
		for _, s := range ds.Sites {
			r, err := domainRouteJSON(s, ds.Domain)
			if err != nil {
				return "", fmt.Errorf("domain %s site %s: %w", ds.Domain, s.Name, err)
			}
			parts = append(parts, r)
		}
	}
	return "[" + strings.Join(parts, ",") + "]", nil
}

func domainRouteJSON(s sites.Site, rootDomain string) (string, error) {
	absPath, err := filepath.Abs(s.Path)
	if err != nil {
		return "", err
	}
	root, _ := json.Marshal(absPath)

	if s.Name == "root" {
		host, _ := json.Marshal(rootDomain)
		path, _ := json.Marshal("/index.html")
		return fmt.Sprintf(`{
		"match": [{"host": [%s], "path": [%s]}],
		"handle": [%s],
		"terminal": true
	}`, host, path, fileHandler(root, s.IsSPA)), nil
	}

	host, _ := json.Marshal(s.Host(rootDomain))

	return fmt.Sprintf(`{
		"match": [{"host": [%s]}],
		"handle": [%s],
		"terminal": true
	}`, host, fileHandler(root, s.IsSPA)), nil
}

// ---- localhost mode --------------------------------------------------------

// BuildLocalhostConfig serves all sites on a single port (9000) using path
// routing: localhost:9000/<domain>/<subdomain>.
// The "root" subdomain maps to localhost:9000/<domain> (no extra segment).
// Port 8999 is the backoffice; port 8898 proxies to Vince.
func BuildLocalhostConfig(domainSites []DomainSites, backofficeAddr string) (*caddy.Config, error) {
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

	var routes []string

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
			rootJSON, _ := json.Marshal(absPath)
			pathMatchJSON, _ := json.Marshal([]string{pathPrefix, pathPrefix + "/*"})
			prefixJSON, _ := json.Marshal(pathPrefix)

			routes = append(routes, fmt.Sprintf(`{
				"match": [{"path": %s}],
				"handle": [%s],
				"terminal": true
			}`, pathMatchJSON, localhostFileHandler(rootJSON, s.IsSPA, prefixJSON)))
		}
	}

	routesJSON := "[" + strings.Join(routes, ",") + "]"
	boAddr, _ := json.Marshal(backofficeAddr)
	vinceAddr, _ := json.Marshal(vinceInternalAddr)

	raw := fmt.Sprintf(`{
		"logging": {
			"logs": {
				"default": {"level": "ERROR"}
			}
		},
		"admin": {"disabled": true},
		"apps": {
			"http": {
				"servers": {
					"sites": {
						"listen": ["127.0.0.1:%d"],
						"routes": %s
					},
					"backoffice": {
						"listen": ["127.0.0.1:%d"],
						"routes": [{"handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": %s}]}]}]
					},
					"vince": {
						"listen": ["127.0.0.1:%d"],
						"routes": [{"handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": %s}]}]}]
					}
				}
			},
			"tls": {"automation": {"policies": [{"issuers": [{"module": "internal"}]}]}}
		}
	}`, LocalhostStartPort, routesJSON, BackofficeLocalhostPort, boAddr, vinceLocalhostProxyPort, vinceAddr)

	return unmarshal(raw)
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

// ---- shared file-serving handler JSON --------------------------------------

// localhostFileHandler wraps fileHandler with a strip_path_prefix rewrite so
// that sites served under a path prefix (e.g. /domain/name) receive requests
// as if they were at the root.
func localhostFileHandler(root json.RawMessage, isSPA bool, pathPrefix json.RawMessage) string {
	secHeaders := securityHeadersHandler()
	stripRewrite := fmt.Sprintf(`{"handler": "rewrite", "strip_path_prefix": %s}`, pathPrefix)

	if isSPA {
		return fmt.Sprintf(`{
			"handler": "subroute",
			"routes": [
				{"handle": [%s]},
				{"handle": [%s]},
				{
					"match": [{"file": {"root": %s, "try_files": ["{http.request.uri.path}", "{http.request.uri.path}/index.html"]}}],
					"handle": [
						{"handler": "rewrite", "uri": "{http.matchers.file.relative}"},
						{"handler": "file_server", "root": %s, "index_names": ["index.html"]}
					]
				},
				{
					"handle": [
						{"handler": "rewrite", "uri": "/index.html"},
						{"handler": "file_server", "root": %s}
					]
				}
			]
		}`, secHeaders, stripRewrite, root, root, root)
	}
	return fmt.Sprintf(`{
		"handler": "subroute",
		"routes": [
			{"handle": [%s]},
			{"handle": [%s]},
			{"handle": [{"handler": "file_server", "root": %s, "index_names": ["index.html", "index.htm"], "browse": {}}]}
		]
	}`, secHeaders, stripRewrite, root)
}

func securityHeadersHandler() string {
	return `{
		"handler": "headers",
		"response": {
			"set": {
				"X-Content-Type-Options":  ["nosniff"],
				"X-Frame-Options":         ["SAMEORIGIN"],
				"Referrer-Policy":         ["strict-origin-when-cross-origin"],
				"X-XSS-Protection":        ["0"],
				"Permissions-Policy":      ["camera=(), microphone=(), geolocation=(), payment=()"]
			}
		}
	}`
}

func fileHandler(root json.RawMessage, isSPA bool) string {
	secHeaders := securityHeadersHandler()

	if isSPA {
		return fmt.Sprintf(`{
			"handler": "subroute",
			"routes": [
				{
					"handle": [%s]
				},
				{
					"match": [{"file": {"root": %s, "try_files": ["{http.request.uri.path}", "{http.request.uri.path}/index.html"]}}],
					"handle": [
						{"handler": "rewrite", "uri": "{http.matchers.file.relative}"},
						{"handler": "file_server", "root": %s, "index_names": ["index.html"]}
					]
				},
				{
					"handle": [
						{"handler": "rewrite", "uri": "/index.html"},
						{"handler": "file_server", "root": %s}
					]
				}
			]
		}`, secHeaders, root, root, root)
	}
	return fmt.Sprintf(`{
		"handler": "subroute",
		"routes": [
			{
				"handle": [%s]
			},
			{
				"handle": [
					{
						"handler": "file_server",
						"root": %s,
						"index_names": ["index.html", "index.htm"],
						"browse": {}
					}
				]
			}
		]
	}`, secHeaders, root)
}

// ---- helpers ---------------------------------------------------------------

func unmarshal(raw string) (*caddy.Config, error) {
	var cfg caddy.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("caddy config error: %w\n\nJSON was:\n%s", err, raw)
	}
	return &cfg, nil
}

// Unused — satisfies go vet
var _ = http.StatusForbidden