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

// IsLocalhost reports whether we are in localhost mode (no root domain).
func IsLocalhost(rootDomain string) bool { return rootDomain == "" }

// BackofficeHost returns the hostname used for the backoffice in domain mode.
func BackofficeHost(rootDomain string) string { return "backoffice." + rootDomain }

// LocalhostStartPort is the base port for sites in localhost mode.
// Port 9000 is always the root/landing site; real sites start at 9001.
const LocalhostStartPort = 9000

// BackofficeLocalhostPort is the backoffice port in localhost mode.
const BackofficeLocalhostPort = LocalhostStartPort - 1

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
func BuildConfig(rootDomain string, discovered []sites.Site, backofficeAddr string) (*caddy.Config, error) {
	discovered = injectLanding(discovered, func(name string) string {
		for _, s := range discovered {
			if s.Name == name {
				return "https://" + s.Host(rootDomain)
			}
		}
		return ""
	})

	routesJSON, err := domainRoutes(rootDomain, discovered, backofficeAddr)
	if err != nil {
		return nil, err
	}
	subjects, _ := json.Marshal([]string{rootDomain, "*." + rootDomain})

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

func domainRoutes(rootDomain string, discovered []sites.Site, backofficeAddr string) (string, error) {
	var parts []string

	boHost, _ := json.Marshal(BackofficeHost(rootDomain))
	boAddr, _ := json.Marshal(backofficeAddr)

	allowedRanges := allowedBackofficeRanges()

	if len(allowedRanges) > 0 {
		// Build two routes for the backoffice host:
		//   1. Deny (403) if the remote IP is NOT in the allow-list.
		//   2. Proxy through to the internal server for allowed IPs.
		//
		// Caddy's remote_ip matcher matches the listed ranges, so we negate it
		// with "not" to catch everything outside those ranges.
		allowJSON, _ := json.Marshal(allowedRanges)

		// Route 1 — block non-allowed IPs
		parts = append(parts, fmt.Sprintf(`{
							"match": [
								{
									"host": [%s],
					"not": [{"remote_ip": {"ranges": %s}}]
				}
			],
			"handle": [{"handler": "static_response", "status_code": "403", "body": "Forbidden"}],
			"terminal": true
		}`, boHost, allowJSON))

		// Route 2 — proxy allowed IPs
		parts = append(parts, fmt.Sprintf(`{
			"match": [{"host": [%s]}],
			"handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": %s}]}],
			"terminal": true
		}`, boHost, boAddr))
	} else {
		// No IP restriction — proxy all traffic to the backoffice.
		parts = append(parts, fmt.Sprintf(`{
			"match": [{"host": [%s]}],
			"handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": %s}]}],
			"terminal": true
		}`, boHost, boAddr))
	}

	for _, s := range discovered {
		r, err := domainRouteJSON(s, rootDomain)
		if err != nil {
			return "", fmt.Errorf("site %s: %w", s.Name, err)
		}
		parts = append(parts, r)
	}
	return "[" + strings.Join(parts, ",") + "]", nil
}

func domainRouteJSON(s sites.Site, rootDomain string) (string, error) {
	absPath, err := filepath.Abs(s.Path)
	if err != nil {
		return "", err
	}
	host, _ := json.Marshal(s.Host(rootDomain))
	root, _ := json.Marshal(absPath)

	return fmt.Sprintf(`{
		"match": [{"host": [%s]}],
		"handle": [%s],
		"terminal": true
	}`, host, fileHandler(root, s.IsSPA)), nil
}

// ---- localhost mode --------------------------------------------------------

// BuildLocalhostConfig serves each site on its own port.
// Port 9000 is always root (real or generated landing page).
// Real sites start at 9001.
func BuildLocalhostConfig(discovered []sites.Site, backofficeAddr string) (*caddy.Config, error) {
	discovered = injectLanding(discovered, func(name string) string {
		for i, s := range discovered {
			if s.Name == name {
				return fmt.Sprintf("http://localhost:%d", LocalhostStartPort+1+i)
			}
		}
		return ""
	})

	var serverEntries []string

	// Backoffice server
	boAddr, _ := json.Marshal(backofficeAddr)
	serverEntries = append(serverEntries, fmt.Sprintf(`"backoffice": {
		"listen": ["127.0.0.1:%d"],
		"routes": [{"handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": %s}]}]}]
	}`, BackofficeLocalhostPort, boAddr))

	// discovered[0] is always root (port 9000), rest are 9001+
	for i, s := range discovered {
		port := LocalhostStartPort + i
		absPath, err := filepath.Abs(s.Path)
		if err != nil {
			return nil, fmt.Errorf("site %s: %w", s.Name, err)
		}
		root, _ := json.Marshal(absPath)
		key, _ := json.Marshal(s.Name)

		serverEntries = append(serverEntries, fmt.Sprintf(`%s: {
			"listen": ["127.0.0.1:%d"],
			"routes": [{"handle": [%s]}]
		}`, key, port, fileHandler(root, s.IsSPA)))
	}

	raw := fmt.Sprintf(`{
		"logging": {
			"logs": {
				"default": {"level": "ERROR"}
			}
		},
		"admin": {"disabled": true},
		"apps": {
			"http": {"servers": {%s}},
			"tls": {"automation": {"policies": [{"issuers": [{"module": "internal"}]}]}}
		}
	}`, strings.Join(serverEntries, ","))

	return unmarshal(raw)
}

// ---- landing injection -----------------------------------------------------

// injectLanding prepends a generated landing site at index 0 (port 9000) when
// no "root" site exists. urlFor must resolve names from the original slice
// (before injection) since the landing page links to the other sites.
func injectLanding(discovered []sites.Site, urlFor func(string) string) []sites.Site {
	if HasRootSite(discovered) {
		return discovered
	}
	if _, err := landing.Generate(discovered, urlFor, landingDir); err != nil {
		return discovered
	}
	return append([]sites.Site{{
		Name:  "root",
		Path:  landingDir,
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

// securityHeadersHandler returns a Caddy headers handler JSON snippet that
// sets defensive HTTP response headers for all static file responses:
//
//   - X-Content-Type-Options: nosniff       — prevents MIME-type sniffing
//   - X-Frame-Options: SAMEORIGIN           — blocks clickjacking via iframes
//   - Referrer-Policy: strict-origin-when-cross-origin — limits referrer leakage
//   - X-XSS-Protection: 0                  — disables legacy IE XSS filter
//     (modern browsers use CSP; the old filter can introduce vulnerabilities)
//   - Permissions-Policy                    — opts out of sensitive browser APIs
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
