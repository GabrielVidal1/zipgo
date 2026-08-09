package builder

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"zipgo/internal/meta"
	"zipgo/internal/sites"

	"github.com/caddyserver/caddy/v2"
)

// MetaPath is the path (relative to a site's host/prefix) at which the JSON
// listing of that site's direct child subdomains is served.
const MetaPath = "/sub-domains-meta"

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

// DefaultMetricsAddr is the loopback address used to serve Prometheus metrics
// when ZIPGO_METRICS is enabled but ZIPGO_METRICS_ADDR is unset.
const DefaultMetricsAddr = "127.0.0.1:2019"

// MetricsAddr returns the address on which to expose the Prometheus /metrics
// endpoint, or "" when metrics are disabled. Metrics are opt-in via
// ZIPGO_METRICS; the bind address can be overridden with ZIPGO_METRICS_ADDR
// (defaults to loopback so metrics are never accidentally public).
func MetricsAddr() string {
	if os.Getenv("ZIPGO_METRICS") == "" {
		return ""
	}
	if a := os.Getenv("ZIPGO_METRICS_ADDR"); a != "" {
		return a
	}
	return DefaultMetricsAddr
}

// LogFormat returns the access-log format selected via ZIPGO_LOG_FORMAT,
// lower-cased and trimmed. The only structured format is "json"; anything else
// (including unset) means no access logs — Caddy still logs errors at ERROR
// level, but nothing per request.
func LogFormat() string {
	return strings.ToLower(strings.TrimSpace(os.Getenv("ZIPGO_LOG_FORMAT")))
}

// accessLogging returns the top-level Caddy logging config and, when format is
// "json", turns on structured access logging: every content HTTP server routes
// its per-request logs to a dedicated "access" logger that this config encodes
// as one JSON line per request on stdout, ready for a log shipper (e.g. Loki)
// to ingest. The metrics server is left out so loopback Prometheus scrapes
// don't drown the request stream. With any other format the servers are left
// untouched and only the default (errors-only) logger is configured.
func accessLogging(servers obj, format string) obj {
	logging := obj{"logs": obj{"default": obj{"level": "ERROR"}}}
	if format != "json" {
		return logging
	}
	for name, s := range servers {
		if name == "metrics" {
			continue
		}
		s.(obj)["logs"] = obj{"default_logger_name": "access"}
	}
	logging["logs"].(obj)["access"] = obj{
		"encoder": obj{"format": "json"},
		"writer":  obj{"output": "stdout"},
		"include": arr{"http.log.access.access"},
	}
	return logging
}

// withMetrics enables per-server HTTP request metrics on each server in the
// given servers map and, when addr is non-empty, adds a dedicated server that
// serves the Prometheus /metrics endpoint on addr. It is a no-op when addr is
// empty. The metrics endpoint is a plain HTTP handler with no admin surface.
// ProxyProtocolAllow returns the CIDR ranges allowed to speak the PROXY
// protocol, from ZIPGO_PROXY_PROTOCOL_ALLOW (comma-separated). Unset means the
// feature is off, which is the default: the header must never be accepted from
// an arbitrary client, since it lets that client dictate its own source
// address.
//
// It exists because zipgo is commonly reached through a TCP proxy that
// terminates nothing (Traefik with tls.passthrough), which hides the visitor:
// every request then appears to come from the proxy. The proxy re-attaches the
// real address as a PROXY v2 header, and this lets Caddy read it.
func ProxyProtocolAllow() []string {
	raw := strings.TrimSpace(os.Getenv("ZIPGO_PROXY_PROTOCOL_ALLOW"))
	if raw == "" {
		return nil
	}
	var out []string
	for _, c := range strings.Split(raw, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// withProxyProtocol makes each server read a PROXY header from connections
// coming from `allow`, so the request's client IP is the real visitor's.
//
// The explicit "tls" wrapper is load-bearing on the HTTPS server: when no tls
// wrapper is named, Caddy *prepends* one, which would decrypt before the PROXY
// header is read and break every connection. Naming it puts proxy_protocol on
// the raw listener, where the header actually is.
//
// Sources in `allow` get go-proxyproto's USE policy, which reads the header
// when present and is otherwise a no-op — so turning this on does not require
// the proxy to be sending headers yet, and the two sides can be rolled out
// one at a time.
func withProxyProtocol(servers obj, allow []string) {
	if len(allow) == 0 {
		return
	}
	ranges := arr{}
	for _, c := range allow {
		ranges = append(ranges, c)
	}
	for name, s := range servers {
		if name == "metrics" {
			continue // loopback scrapes, never proxied
		}
		s.(obj)["listener_wrappers"] = arr{
			obj{"wrapper": "proxy_protocol", "allow": ranges},
			obj{"wrapper": "tls"},
		}
	}
}

func withMetrics(servers obj, addr string) {
	if addr == "" {
		return
	}
	for _, s := range servers {
		s.(obj)["metrics"] = obj{}
	}
	servers["metrics"] = obj{
		"listen": arr{addr},
		"routes": arr{obj{"handle": arr{obj{"handler": "metrics"}}}},
	}
}

// dotHide returns absolute paths to all dot-suffixed entries (subdomain
// directories) directly inside dir, so Caddy hides them via a prefix match on
// the absolute path rather than matching every path component. A bare "*."
// glob would match every served file, since each site's own root directory
// ends in a dot — hiding everything and returning 404 for all requests.
func dotHide(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var paths []string
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".") {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	return paths
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

// served reports whether a site should produce a route at all. A site is served
// when its .zipgoconfig.json doesn't disable it (enable:false) and it has
// something to serve — an index.html (file mode), a rewrite upstream
// (reverse-proxy mode) or a redirect target. Disabled sites are also excluded
// from sub-domains-meta.
func served(s sites.Site) bool {
	return s.Config.Enabled() &&
		(s.Config.Redirect != "" || s.Config.Rewrite != "" || hasIndex(s))
}

// authorizedOrigins reads the site's index.html and returns the value of its
// <meta property="zipgo:authorized-origins" content="..."> tag, or "" when
// absent. The value is a space-separated list of CSP frame-ancestors source
// expressions (host patterns, supporting "*" wildcards) — e.g.
// "https://*.gabvdl.xyz" — declaring which origins may embed the site in a
// frame/iframe. When set, the builder emits a Content-Security-Policy
// frame-ancestors directive (and drops the default X-Frame-Options) for that
// site; when absent the site keeps the safe X-Frame-Options: SAMEORIGIN default.
func authorizedOrigins(s sites.Site) string {
	m, err := meta.Extract(filepath.Join(s.Path, "index.html"))
	if err != nil {
		log.Printf("authorized-origins: extract %s: %v", s.Path, err)
		return ""
	}
	return strings.TrimSpace(m.Zipgo["authorized-origins"])
}

// hasWww reports whether the domain has a www subdomain site.
func hasWww(all []sites.Site) bool {
	for _, s := range all {
		if len(s.Labels) == 1 && s.Labels[0] == "www" && s.Config.Enabled() && hasIndex(s) {
			return true
		}
	}
	return false
}

// childMeta returns the metadata of parent's direct child subdomains, keyed by
// the child's folder name (its leaf label plus a trailing dot, e.g. "docs.").
// A site C is a direct child of parent when its label chain is parent's chain
// with exactly one extra leaf prepended (chains are leaf-first). Children
// without an index.html are skipped. Returns nil when parent has no children.
func childMeta(parent sites.Site, all []sites.Site) map[string]meta.Meta {
	var result map[string]meta.Meta
	for _, c := range all {
		if len(c.Labels) != len(parent.Labels)+1 {
			continue
		}
		if !labelsEqual(c.Labels[1:], parent.Labels) {
			continue
		}
		if !c.Config.Enabled() || !hasIndex(c) {
			continue
		}
		m, err := meta.Extract(filepath.Join(c.Path, "index.html"))
		if err != nil {
			log.Printf("sub-domains-meta: extract %s: %v", c.Path, err)
			m = meta.Meta{}
		}
		if result == nil {
			result = map[string]meta.Meta{}
		}
		result[c.Labels[0]+"."] = m
	}
	return result
}

func labelsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// metaRoute builds a terminal route that serves m as a JSON object under the
// given matcher. Callers must only invoke it with a non-empty m.
func metaRoute(matcher obj, m map[string]meta.Meta) (obj, error) {
	body, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshal sub-domains-meta: %w", err)
	}
	return obj{
		"match": arr{matcher},
		"handle": arr{obj{
			"handler":     "static_response",
			"status_code": 200,
			"body":        string(body),
			"headers": obj{
				"Content-Type":                arr{"application/json"},
				"Access-Control-Allow-Origin": arr{"*"},
			},
		}},
		"terminal": true,
	}, nil
}

// BuildConfig serves every site on its host over HTTPS (Let's Encrypt).
// It supports multiple domains simultaneously. When metricsAddr is non-empty,
// per-server HTTP metrics are enabled and a Prometheus /metrics endpoint is
// served on that address. When logFormat is "json", structured JSON access logs
// are written to stdout (one line per request).
func BuildConfig(domainSites []DomainSites, metricsAddr, logFormat string) (*caddy.Config, error) {
	routes := arr{}
	// httpRoutes are the per-site routes served on :80 for sites that opt into
	// plain HTTP via allowHttp. Everything else falls through to the catch-all
	// HTTPS redirect appended below.
	httpRoutes := arr{}
	var subjects []string

	for _, ds := range domainSites {
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
			if !served(s) {
				continue
			}
			httpAllowed := s.Config.HTTPAllowed()
			// Serve /sub-domains-meta for any site that has child subdomains,
			// before the site's own file_server so the path match wins.
			if cm := childMeta(s, ds.Sites); cm != nil {
				mr, err := metaRoute(obj{"host": arr{s.Host(ds.Domain)}, "path": arr{MetaPath}}, cm)
				if err != nil {
					return nil, fmt.Errorf("domain %s host %s: %w", ds.Domain, s.Host(ds.Domain), err)
				}
				mr = guard(mr, s)
				routes = append(routes, mr)
				if httpAllowed {
					httpRoutes = append(httpRoutes, mr)
				}
			}
			r, err := domainRoute(s, ds.Domain)
			if err != nil {
				return nil, fmt.Errorf("domain %s host %s: %w", ds.Domain, s.Host(ds.Domain), err)
			}
			routes = append(routes, r)
			if httpAllowed {
				httpRoutes = append(httpRoutes, r)
			}
			if !s.IsApex() {
				subjects = append(subjects, s.Host(ds.Domain))
			}
		}
	}

	// Catch-all: any host not explicitly allowed over HTTP is 301'd to HTTPS.
	httpRoutes = append(httpRoutes, obj{"handle": arr{obj{
		"handler":     "static_response",
		"status_code": "301",
		"headers":     obj{"Location": arr{"https://{http.request.host}{http.request.uri}"}},
	}}})

	servers := obj{
		"https": obj{
			"listen": arr{":443"},
			"routes": routes,
		},
		"http_redirect": obj{
			"listen": arr{":80"},
			"routes": httpRoutes,
		},
	}
	// Domain mode only: in localhost mode nothing sits in front of zipgo.
	withProxyProtocol(servers, ProxyProtocolAllow())
	withMetrics(servers, metricsAddr)

	cfg := obj{
		"logging": accessLogging(servers, logFormat),
		"admin":   obj{"disabled": true},
		"apps": obj{
			"http": obj{"servers": servers},
			"tls": obj{"automation": obj{"policies": arr{obj{
				"subjects": subjects,
				"issuers": arr{obj{
					"module": "acme",
					"challenges": obj{
						"http":     obj{"disabled": false},
						"tls-alpn": obj{"disabled": true},
					},
				}},
			}}}},
		},
	}

	return finalize(cfg)
}

func domainRoute(s sites.Site, rootDomain string) (obj, error) {
	h, err := siteHandler(s, "")
	if err != nil {
		return nil, err
	}
	return obj{
		"match":    arr{obj{"host": arr{s.Host(rootDomain)}}},
		"handle":   arr{h},
		"terminal": true,
	}, nil
}

// siteHandler builds the handler for a single site: a redirect subroute when the
// site declares a redirect target, a reverse_proxy subroute when it declares a
// rewrite upstream, otherwise the file_server subroute. A site that (wrongly)
// sets both redirect and rewrite redirects — doctor flags the combination.
// stripPrefix is the localhost path prefix to strip ("" in domain mode).
// A site with basicAuth is wrapped so the credential check runs before anything
// is served — files, SPA fallback and proxied upstreams alike.
func siteHandler(s sites.Site, stripPrefix string) (obj, error) {
	inner, err := unguardedSiteHandler(s, stripPrefix)
	if err != nil {
		return nil, err
	}
	if auth := basicAuthHandler(s.Config); auth != nil {
		return obj{"handler": "subroute", "routes": arr{
			obj{"handle": arr{auth}},
			obj{"handle": arr{inner}},
		}}, nil
	}
	return inner, nil
}

// unguardedSiteHandler picks what the site serves, before any auth check is
// layered on top: a redirect, a proxied upstream, or its own files.
func unguardedSiteHandler(s sites.Site, stripPrefix string) (obj, error) {
	if s.Config.Redirect != "" {
		return redirectHandler(s, stripPrefix), nil
	}
	if s.Config.Rewrite != "" {
		return proxyHandler(s, stripPrefix), nil
	}
	absPath, err := filepath.Abs(s.Path)
	if err != nil {
		return nil, err
	}
	// Hide subdomain folders and the per-site config file from the file_server.
	hide := append(dotHide(absPath), filepath.Join(absPath, sites.ConfigFileName))
	notFound := ""
	if s.HasNotFoundPage() {
		notFound = s.NotFoundPage
	}
	return fileHandler(absPath, s.IsSPA, stripPrefix, hide, authorizedOrigins(s), s.Config.Headers, notFound), nil
}

// redirectHandler builds a subroute that answers every request for the site with
// a redirect to the site's configured target, instead of serving files. In
// localhost mode the site's path prefix is stripped first, so the placeholder in
// the Location header expands to the path *within* the site.
func redirectHandler(s sites.Site, stripPrefix string) obj {
	routes := arr{obj{"handle": arr{securityHeaders(authorizedOrigins(s), s.Config.Headers)}}}
	if stripPrefix != "" {
		routes = append(routes, obj{"handle": arr{obj{
			"handler":           "rewrite",
			"strip_path_prefix": stripPrefix,
		}}})
	}
	routes = append(routes, obj{"handle": arr{obj{
		"handler":     "static_response",
		"status_code": strconv.Itoa(s.Config.RedirectCode()),
		"headers":     obj{"Location": arr{redirectLocation(s.Config.Redirect)}},
	}}})
	return obj{"handler": "subroute", "routes": routes}
}

// redirectLocation turns a "redirect" value into the Location header Caddy
// sends. A bare origin ("https://elsewhere.com", trailing slash allowed) keeps
// the request's path and query, so deep links survive a domain move:
// /docs?x=1 → https://elsewhere.com/docs?x=1. A target that carries a path or a
// query ("https://elsewhere.com/moved") is used verbatim — every request lands
// there.
func redirectLocation(redirect string) string {
	r := strings.TrimSpace(redirect)
	u, err := url.Parse(r)
	if err != nil || u.Host == "" {
		// doctor rejects these; be conservative and don't append anything.
		return r
	}
	if strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" {
		return r
	}
	return strings.TrimSuffix(r, "/") + "{http.request.uri}"
}

// basicAuthHandler builds the Caddy authentication handler for a site's
// basicAuth config, or nil when the site is public. Passwords are bcrypt
// hashes; the hash cache keeps a protected site from paying a full bcrypt
// comparison on every request.
func basicAuthHandler(c sites.Config) obj {
	if !c.Protected() {
		return nil
	}
	users := make([]string, 0, len(c.BasicAuth))
	for u := range c.BasicAuth {
		users = append(users, u)
	}
	sort.Strings(users) // stable config across reloads
	accounts := arr{}
	for _, u := range users {
		accounts = append(accounts, obj{"username": u, "password": c.BasicAuth[u]})
	}
	return obj{
		"handler": "authentication",
		"providers": obj{"http_basic": obj{
			"hash":       obj{"algorithm": "bcrypt"},
			"accounts":   accounts,
			"hash_cache": obj{},
		}},
	}
}

// guard prepends a site's basic-auth check to an already-built route's handler
// chain, so an endpoint served beside the site (sub-domains-meta) is protected
// with it rather than leaking the child listing of a password-protected site.
func guard(route obj, s sites.Site) obj {
	auth := basicAuthHandler(s.Config)
	if auth == nil {
		return route
	}
	if h, ok := route["handle"].(arr); ok {
		route["handle"] = append(arr{auth}, h...)
	}
	return route
}

// proxyHandler builds a reverse_proxy subroute forwarding to the site's rewrite
// upstream. It keeps the same security-headers handler as file routes (custom
// .zipgoconfig.json headers included), and (in localhost mode) strips the path
// prefix before proxying.
func proxyHandler(s sites.Site, stripPrefix string) obj {
	dial, useTLS := proxyDial(s.Config.Rewrite)
	routes := arr{obj{"handle": arr{securityHeaders(authorizedOrigins(s), s.Config.Headers)}}}
	if stripPrefix != "" {
		routes = append(routes, obj{"handle": arr{obj{
			"handler":           "rewrite",
			"strip_path_prefix": stripPrefix,
		}}})
	}
	if p := sites.NormalizeRewritePath(s.Config.RewritePath); p != "" {
		routes = append(routes, rewritePathRoute(p, s.Config.RewritePathPassthroughs()))
	}
	rp := obj{
		"handler":   "reverse_proxy",
		"upstreams": arr{obj{"dial": dial}},
	}
	if useTLS {
		rp["transport"] = obj{"protocol": "http", "tls": obj{}}
	}
	routes = append(routes, obj{"handle": arr{rp}})
	return obj{"handler": "subroute", "routes": routes}
}

// rewritePathRoute prepends the site's rewritePath to the upstream request path,
// so a sub-path of an upstream ("/_/" — the PocketBase admin) is reachable at the
// site's root while the browser URL stays on this host.
//
// The prepend is skipped for a request that already starts with the prefix, so a
// client that does use the upstream's own path is passed through unchanged and
// nothing is prefixed twice. That guard is what makes the key usable in practice:
// the app served under the prefix typically still calls the upstream's *root*
// paths (PocketBase's admin UI fetches /api/... absolutely), and those must reach
// the upstream as-is rather than becoming /_/api/... — so only paths that the
// upstream does not already own get moved under the prefix. Sites therefore pass
// the upstream's root paths through via `rewritePathPassthrough`.
func rewritePathRoute(prefix string, passthrough []string) obj {
	// One "not" matcher holding every exempt path: Caddy ORs the path patterns
	// inside a matcher set, and "not" negates the whole set, so the rewrite runs
	// only when the request matches none of them.
	skip := arr{prefix, prefix + "/*"}
	for _, p := range passthrough {
		skip = append(skip, p, p+"/*")
	}
	return obj{
		"match": arr{obj{"not": arr{obj{"path": skip}}}},
		"handle": arr{obj{
			"handler": "rewrite",
			"uri":     prefix + "{http.request.uri.path}{http.request.uri.query_string}",
		}},
	}
}

// proxyDial turns a rewrite value into a Caddy reverse_proxy dial address
// (host:port) and reports whether the upstream uses TLS. A bare host:port is
// returned unchanged; a value with a scheme (http://, https://) has its port
// defaulted (80/443) when omitted.
func proxyDial(rewrite string) (dial string, useTLS bool) {
	r := strings.TrimSpace(rewrite)
	if strings.Contains(r, "://") {
		if u, err := url.Parse(r); err == nil && u.Host != "" {
			host := u.Host
			useTLS = u.Scheme == "https"
			if u.Port() == "" {
				if useTLS {
					host += ":443"
				} else {
					host += ":80"
				}
			}
			return host, useTLS
		}
	}
	return r, false
}

// ---- localhost mode --------------------------------------------------------

// BuildLocalhostConfig serves all sites on a single port (9000) using path
// routing: localhost:9000/<domain>/<subdomain>. The apex maps to
// localhost:9000/<domain> (no extra segment). When metricsAddr is non-empty,
// per-server HTTP metrics are enabled and a Prometheus /metrics endpoint is
// served on that address. When logFormat is "json", structured JSON access logs
// are written to stdout (one line per request).
func BuildLocalhostConfig(domainSites []DomainSites, metricsAddr, logFormat string) (*caddy.Config, error) {
	routes := arr{}

	for _, ds := range domainSites {
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
			if !served(s) {
				continue
			}
			pathPrefix := s.LocalhostPath(ds.Domain)

			// Serve <prefix>/sub-domains-meta for any site with child
			// subdomains, before the site's own routes so the path match wins.
			if cm := childMeta(s, ds.Sites); cm != nil {
				mr, err := metaRoute(obj{"path": arr{pathPrefix + MetaPath}}, cm)
				if err != nil {
					return nil, fmt.Errorf("domain %s host %s: %w", ds.Domain, s.LocalhostPath(ds.Domain), err)
				}
				mr = guard(mr, s)
				routes = append(routes, mr)
			}

			h, err := siteHandler(s, pathPrefix)
			if err != nil {
				return nil, fmt.Errorf("domain %s host %s: %w", ds.Domain, s.LocalhostPath(ds.Domain), err)
			}

			routes = append(routes, obj{
				"match":    arr{obj{"path": arr{pathPrefix, pathPrefix + "/*"}}},
				"handle":   arr{h},
				"terminal": true,
			})
		}
	}

	servers := obj{
		"sites": obj{
			"listen": arr{fmt.Sprintf("127.0.0.1:%d", LocalhostStartPort)},
			"routes": routes,
		},
	}
	withMetrics(servers, metricsAddr)

	cfg := obj{
		"logging": accessLogging(servers, logFormat),
		"admin":   obj{"disabled": true},
		"apps": obj{
			"http": obj{"servers": servers},
			"tls":  obj{"automation": obj{"policies": arr{obj{"issuers": arr{obj{"module": "internal"}}}}}},
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
// apex from exposing its dot-suffixed subdomain folders). custom is the site's
// .zipgoconfig.json "headers" map, merged into the security headers. notFound is
// the site's custom 404 page (a file name, "" when it has none), served in place
// of Caddy's plain-text 404 body.
func fileHandler(root string, isSPA bool, stripPrefix string, hide []string, frameAncestors string, custom map[string]string, notFound string) obj {
	routes := arr{obj{"handle": arr{securityHeaders(frameAncestors, custom)}}}

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

	sub := obj{"handler": "subroute", "routes": routes}
	if !isSPA && notFound != "" {
		sub["errors"] = notFoundErrors(root, hide, notFound)
	}
	return sub
}

// notFoundErrors builds the error-handling routes of a static site's subroute:
// when the file_server raises a 404, serve the site's own 404 page as the body
// instead of Caddy's plain-text default. The status stays 404 (status_code on
// the file_server), so crawlers and `curl -f` still see a miss — this replaces
// the body, not the semantics. Any other error (a 403 on an unreadable file, a
// 500) is left to Caddy: a site's 404 page is not an excuse to swallow those.
// The security-headers handler has already run on the way in, so the custom page
// goes out with the same headers as every other response from the site.
func notFoundErrors(root string, hide []string, notFound string) obj {
	fs := obj{
		"handler":     "file_server",
		"root":        root,
		"status_code": "404",
	}
	if len(hide) > 0 {
		fs["hide"] = toArr(hide)
	}
	return obj{"routes": arr{obj{
		"match": arr{obj{"expression": "{http.error.status_code} == 404"}},
		"handle": arr{
			obj{"handler": "rewrite", "uri": "/" + notFound},
			fs,
		},
	}}}
}

func toArr(s []string) arr {
	out := make(arr, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// securityHeaders builds the response-header handler applied to every static
// route. By default it sends X-Frame-Options: SAMEORIGIN, denying cross-origin
// framing (clickjacking protection). When frameAncestors is non-empty (set per
// site via the <meta property="zipgo:authorized-origins"> tag) it instead emits
// a Content-Security-Policy frame-ancestors directive listing the allowed
// embedding origins, and omits X-Frame-Options (the two conflict; CSP
// frame-ancestors is the modern, allow-list-capable replacement).
//
// custom holds the site's .zipgoconfig.json "headers" map. Its entries are
// *merged into* this handler rather than replacing it: every default zipgo
// sends is kept unless the site names that exact header, which is how a site
// gets Cache-Control or CORS without a proxy in front. Names are canonicalised
// ("cache-control" → "Cache-Control") so an override lands on the same key as
// the default it replaces, and an empty value deletes the header (from the
// defaults and from anything the file_server or upstream set) instead of
// sending it blank.
func securityHeaders(frameAncestors string, custom map[string]string) obj {
	set := obj{
		"X-Content-Type-Options": arr{"nosniff"},
		"Referrer-Policy":        arr{"strict-origin-when-cross-origin"},
		"X-XSS-Protection":       arr{"0"},
		"Permissions-Policy":     arr{"camera=(), microphone=(), geolocation=(), payment=()"},
	}
	if frameAncestors == "" {
		set["X-Frame-Options"] = arr{"SAMEORIGIN"}
	} else {
		set["Content-Security-Policy"] = arr{"frame-ancestors 'self' " + frameAncestors}
	}

	var del []string
	for name, value := range custom {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := headerKey(set, name)
		if value == "" {
			delete(set, key)
			del = append(del, http.CanonicalHeaderKey(name))
			continue
		}
		set[key] = arr{value}
	}

	response := obj{"set": set}
	if len(del) > 0 {
		sort.Strings(del) // stable config output across reloads
		response["delete"] = toArr(del)
	}
	return obj{
		"handler":  "headers",
		"response": response,
	}
}

// headerKey returns the key a custom header must be written under in set.
// Header names are case-insensitive on the wire, so a site writing
// "x-xss-protection" has to land on the *existing* default entry
// ("X-XSS-Protection") — writing the canonical spelling would leave both in the
// map and send the header twice. Only a header with no default gets the
// canonical form ("cache-control" → "Cache-Control").
func headerKey(set obj, name string) string {
	for k := range set {
		if strings.EqualFold(k, name) {
			return k
		}
	}
	return http.CanonicalHeaderKey(name)
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
