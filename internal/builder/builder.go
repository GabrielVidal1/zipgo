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

// VinceHost returns the hostname used for the Vince analytics UI in domain mode.
func VinceHost(rootDomain string) string { return "analytics." + rootDomain }

// LocalhostStartPort is the base port for sites in localhost mode.
// Port 9000 is always the root/landing site; real sites start at 9001.
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
		allowJSON, _ := json.Marshal(allowedRanges)

		// Block non-allowed IPs for the backoffice host.
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

		// Proxy allowed IPs to the backoffice.
		parts = append(parts, fmt.Sprintf(`{
			"match": [{"host": [%s]}],
			"handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": %s}]}],
			"terminal": true
		}`, boHost, boAddr))
	} else {
		parts = append(parts, fmt.Sprintf(`{
			"match": [{"host": [%s]}],
			"handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": %s}]}],
			"terminal": true
		}`, boHost, boAddr))
	}

	// Vince analytics route — proxy analytics.<rootDomain> to the sidecar.
	vinceHost, _ := json.Marshal(VinceHost(rootDomain))
	vinceAddr, _ := json.Marshal(vinceInternalAddr)
	parts = append(parts, fmt.Sprintf(`{
		"match": [{"host": [%s]}],
		"handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": %s}]}],
		"terminal": true
	}`, vinceHost, vinceAddr))

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
// Port 8999 is the backoffice.
// Port 8898 proxies to the Vince sidecar on 8899.
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

	// Backoffice server.
	boAddr, _ := json.Marshal(backofficeAddr)
	serverEntries = append(serverEntries, fmt.Sprintf(`"backoffice": {
		"listen": ["127.0.0.1:%d"],
		"routes": [{"handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": %s}]}]}]
	}`, BackofficeLocalhostPort, boAddr))

	// Vince analytics proxy server.
	vinceAddr, _ := json.Marshal(vinceInternalAddr)
	serverEntries = append(serverEntries, fmt.Sprintf(`"vince": {
		"listen": ["127.0.0.1:%d"],
		"routes": [{"handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": %s}]}]}]
	}`, vinceLocalhostProxyPort, vinceAddr))

	// discovered[0] is always root (port 9000), rest are 9001+.
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