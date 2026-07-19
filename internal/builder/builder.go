package builder

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"zipgo/internal/flags"
	"zipgo/internal/gate"
	"zipgo/internal/meta"
	"zipgo/internal/sites"

	"github.com/caddyserver/caddy/v2"
	// Modules this package emits are linked here, where they are emitted, so a
	// generated config can never name one the binary lacks (the tests load the
	// config in real Caddy to prove it):
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/proxyprotocol" // caddy.listeners.proxy_protocol (withProxyProtocol)
	_ "github.com/caddyserver/caddy/v2/modules/logging"                 // caddy.logging.encoders.filter (accessLogging)
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
		// The JSON encoder is wrapped in a filter that drops the gate's
		// unlock parameter (?__zipgo_pwd=…) from the logged URI: a link that
		// carries a site's password must not leave it in the access log.
		"encoder": obj{
			"format": "filter",
			"wrap":   obj{"format": "json"},
			"fields": obj{
				"request>uri": obj{
					"filter":  "query",
					"actions": arr{obj{"type": "delete", "parameter": gate.UnlockParam}},
				},
			},
		},
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
	return envList("ZIPGO_PROXY_PROTOCOL_ALLOW")
}

// TrustedProxies returns the CIDR ranges whose X-Forwarded-For header Caddy
// believes, from ZIPGO_TRUSTED_PROXIES (comma-separated). Unset, it falls back
// to ZIPGO_PROXY_PROTOCOL_ALLOW: the proxy that prepends a PROXY header on :443
// is the one that forwards :80 (and any re-encrypted hop) with X-Forwarded-For,
// so the client IP — in the access log and in the gate's per-address throttle —
// is the visitor's on every path in. Nothing is trusted when both are unset:
// anyone could then claim any address.
func TrustedProxies() []string {
	if l := envList("ZIPGO_TRUSTED_PROXIES"); l != nil {
		return l
	}
	return ProxyProtocolAllow()
}

// RewriteAllow returns the upstream patterns a site's "rewrite" may point at,
// from ZIPGO_REWRITE_ALLOW (comma-separated globs over the dial address, e.g.
// "*-pocketbase:8090,100.64.0.5:80"). Unset means any upstream, as before. Set,
// it turns a rewrite into an operator decision: a deployed .zipgoconfig.json
// can no longer publish whatever the server can reach (another container, a
// tailnet service) under a public host — see UpstreamAllowed.
func RewriteAllow() []string {
	return envList("ZIPGO_REWRITE_ALLOW")
}

// UpstreamAllowed reports whether a reverse-proxy dial address (host:port)
// matches one of the allow patterns. A nil allow list allows everything.
func UpstreamAllowed(dial string, allow []string) bool {
	if allow == nil {
		return true
	}
	for _, p := range allow {
		if ok, _ := path.Match(p, dial); ok {
			return true
		}
	}
	return false
}

// envList splits a comma-separated environment variable, trimming blanks;
// unset or blank yields nil.
func envList(name string) []string {
	raw := strings.TrimSpace(os.Getenv(name))
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

// withTrustedProxies makes each content server take the client address from
// X-Forwarded-For when the connection comes from one of `ranges`.
func withTrustedProxies(servers obj, ranges []string) {
	if len(ranges) == 0 {
		return
	}
	for name, s := range servers {
		if name == "metrics" {
			continue
		}
		s.(obj)["trusted_proxies"] = obj{"source": "static", "ranges": toArr(ranges)}
	}
}

// Connection limits on every content server. Caddy (2.11.6+) already cuts a
// request whose body read or response write stalls for a minute and caps
// request headers at 16 KiB; these add a short deadline for the request line
// and headers, which is where a slowloris client spends its time, and shorten
// the keep-alive idle time so a flood of idle connections is cheaper to carry.
const (
	readHeaderTimeout = "15s"
	idleTimeout       = "2m"
)

func withTimeouts(servers obj) {
	for name, s := range servers {
		if name == "metrics" {
			continue
		}
		s.(obj)["read_header_timeout"] = readHeaderTimeout
		s.(obj)["idle_timeout"] = idleTimeout
	}
}

// hstsValue is the Strict-Transport-Security a site served only over HTTPS
// sends: one year, this host only. No includeSubDomains — a site's subdomains
// may be served by something else entirely (a plain-HTTP preview host, say) —
// and no preload, which is a per-domain decision for the operator; a site that
// wants either sets the header itself in its "headers".
const hstsValue = "max-age=31536000"

// httpAllowed reports whether a site is also served on :80 — it opted in with
// allowHttp and has no password. A password site is HTTPS-only whatever
// allowHttp says: its session cookie is Secure (a browser would never send it
// back over HTTP), and a password has no business crossing the network in
// clear.
func httpAllowed(s sites.Site) bool {
	return s.Config.HTTPAllowed() && !s.Config.HasPassword()
}

// httpsOnly reports whether a site is reached over HTTPS and nothing else, so
// it can promise that to browsers with HSTS: domain mode (localhost mode is
// plain HTTP) and not served on :80.
func httpsOnly(s sites.Site, stripPrefix string) bool {
	return stripPrefix == "" && !httpAllowed(s)
}

// dotfileRoute answers 404 for any path with a component that starts with a
// dot — .git, .env, .DS_Store, a build tool's leftovers — before the file
// server can serve it. The one exception is the /.well-known/ tree, which is
// where standards put files that are meant to be fetched (security.txt,
// assetlinks.json, apple-app-site-association). The "*/.*" pattern is a
// substring match on the cleaned request path, so //.git, /x/../.env and
// %2E-escaped dots are caught too. The error handler (rather than a static
// response) lets a static site's own 404 page answer.
func dotfileRoute() obj {
	return obj{
		"match": arr{obj{
			"path": arr{"*/.*"},
			"not":  arr{obj{"path": arr{"/.well-known/*"}}},
		}},
		"handle": arr{obj{"handler": "error", "status_code": "404"}},
	}
}

// misdirectedRoute is the last route of the HTTPS server: a request whose Host
// matches no site answers 421 instead of an empty 200. With strict_sni_host
// on, the Host already had to match the TLS SNI, so this only meets a host
// that has a certificate but no longer has a route.
func misdirectedRoute() obj {
	return obj{"handle": arr{obj{
		"handler":     "static_response",
		"status_code": "421",
		"headers":     obj{"Content-Type": arr{"text/plain; charset=utf-8"}, "Cache-Control": arr{"no-store"}},
		"body":        "421 Misdirected Request\n",
	}}}
}

// escapingSymlinks lists the symlinks under a site folder (its nested
// subdomain folders excluded — they are sites of their own) whose target is
// outside that folder, or inside one of its nested sites. Caddy's file server
// follows symlinks wherever they point, so a deployed link could serve another
// site's files — a locked site's included — or anything else zipgo can read,
// the certificate store and the session key first. These are hidden from the
// file server (a prefix match, so a linked folder hides its whole subtree).
// Links that stay inside the site are fine and left alone.
func escapingSymlinks(root string) []string {
	base := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		base = r
	}
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != root && strings.HasSuffix(d.Name(), ".") {
			return filepath.SkipDir // a nested site: walked on its own
		}
		if d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		target, err := filepath.EvalSymlinks(p)
		if err != nil {
			return nil // dangling: a 404 anyway
		}
		if !insideSite(base, target) {
			log.Printf("🚫  %s: symlink → %s points outside the site, hidden", p, target)
			out = append(out, p)
		}
		return nil
	})
	return out
}

// insideSite reports whether target lies within the site folder base without
// crossing into a nested subdomain folder.
func insideSite(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part != "." && strings.HasSuffix(part, ".") {
			return false
		}
	}
	return true
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

// hasContent reports whether a site has something to serve — an index.html
// (file mode), a rewrite upstream (reverse-proxy mode), a redirect target or
// the back-office (admin). A folder without any of those only exists to hold
// subdomains.
func hasContent(s sites.Site) bool {
	return s.Config.IsAdmin() || s.Config.Redirect != "" || s.Config.Rewrite != "" || hasIndex(s)
}

// served reports whether a site gets its normal route: it is not switched off
// (by its own "enable": false or a parent's — see sites.Site.Disabled) and it
// has something to serve. Disabled sites are also excluded from
// sub-domains-meta.
func served(s sites.Site) bool {
	return !s.Off() && hasContent(s)
}

// Gone reports whether a site answers 410 Gone instead: it is switched off and
// has something to serve, i.e. it was live. A folder with nothing of its own
// (one that only holds subdomains) stays unrouted when off, as it was while on —
// routing it would put a host that never had a certificate into the TLS
// subjects and send Caddy off to request one just to say "gone".
func Gone(s sites.Site) bool {
	return s.Off() && hasContent(s)
}

// GoneBody is the page a disabled site answers every request with, under a 410
// status — the site existed and was deliberately taken down, which a crawler
// reads as "drop it" where a 404 would mean "maybe later".
const GoneBody = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>410 Gone</title>
<style>
  :root { color-scheme: light dark; }
  body { margin: 0; min-height: 100vh; display: grid; place-items: center;
         font: 16px/1.5 system-ui, sans-serif; text-align: center; }
  h1 { margin: 0 0 .25rem; font-size: 1.5rem; }
  p { margin: 0; opacity: .7; }
</style>
</head>
<body>
<main>
<h1>This site is no longer available</h1>
<p>410 Gone</p>
</main>
</body>
</html>
`

// goneHandler builds the subroute a disabled site is served with: the usual
// security headers, then a static 410 page. Cache-Control: no-store keeps
// browsers from holding on to the 410 once the site is switched back on.
func goneHandler(hsts bool) obj {
	headers := securityHeaders("", map[string]string{"Cache-Control": "no-store"}, hsts)
	return obj{"handler": "subroute", "routes": arr{
		obj{"handle": arr{headers}},
		obj{"handle": arr{obj{
			"handler":     "static_response",
			"status_code": "410",
			"headers":     obj{"Content-Type": arr{"text/html; charset=utf-8"}},
			"body":        GoneBody,
		}}},
	}}
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
	// The value lands in a response header: keep control characters (a
	// newline first of all) out of it, whatever the HTML parser let through.
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, m.Zipgo["authorized-origins"]))
}

// hasWww reports whether the domain has a www subdomain site.
func hasWww(all []sites.Site) bool {
	for _, s := range all {
		if len(s.Labels) == 1 && s.Labels[0] == "www" && !s.Off() && hasIndex(s) {
			return true
		}
	}
	return false
}

// childMeta returns the metadata of parent's direct child subdomains, keyed by
// the child's folder name (its leaf label plus a trailing dot, e.g. "docs.").
// A site C is a direct child of parent when its label chain is parent's chain
// with exactly one extra leaf prepended (chains are leaf-first). Children
// without an index.html are skipped, and so are protected ones (a password or
// basicAuth): the listing is public, and a locked site's title and description
// must not leak through its parent's feed. Returns nil when parent has no
// children.
func childMeta(parent sites.Site, all []sites.Site) map[string]meta.Meta {
	var result map[string]meta.Meta
	for _, c := range all {
		if len(c.Labels) != len(parent.Labels)+1 {
			continue
		}
		if !labelsEqual(c.Labels[1:], parent.Labels) {
			continue
		}
		if c.Off() || !hasIndex(c) || c.Config.Protected() || c.Config.IsAdmin() {
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
			if Gone(s) {
				// A switched-off site keeps its host (and its certificate) so
				// visitors get a clear 410 rather than a TLS error.
				r := obj{
					"match":    arr{obj{"host": arr{s.Host(ds.Domain)}}},
					"handle":   arr{goneHandler(!s.Config.HTTPAllowed())},
					"terminal": true,
				}
				routes = append(routes, r)
				if s.Config.HTTPAllowed() {
					httpRoutes = append(httpRoutes, r)
				}
				if !s.IsApex() {
					subjects = append(subjects, s.Host(ds.Domain))
				}
				continue
			}
			if !served(s) {
				continue
			}
			plainHTTP := httpAllowed(s)
			host := s.Host(ds.Domain)
			// Serve /sub-domains-meta for any site that has child subdomains,
			// before the site's own file_server so the path match wins.
			if cm := childMeta(s, ds.Sites); cm != nil {
				mr, err := metaRoute(obj{"host": arr{host}, "path": arr{MetaPath}}, cm)
				if err != nil {
					return nil, fmt.Errorf("domain %s host %s: %w", ds.Domain, host, err)
				}
				mr = guard(mr, s, host, "")
				routes = append(routes, mr)
				if plainHTTP {
					httpRoutes = append(httpRoutes, mr)
				}
			}
			r, err := domainRoute(s, host)
			if err != nil {
				return nil, fmt.Errorf("domain %s host %s: %w", ds.Domain, s.Host(ds.Domain), err)
			}
			routes = append(routes, r)
			if plainHTTP {
				httpRoutes = append(httpRoutes, r)
			}
			if !s.IsApex() {
				subjects = append(subjects, s.Host(ds.Domain))
			}
		}
	}

	// Catch-all on HTTPS: a Host that is no site answers 421, not an empty 200.
	routes = append(routes, misdirectedRoute())

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
			// A request's Host must be the host the TLS handshake was for.
			// zipgo routes by Host, so without this a client could present
			// the SNI of one site — the certificate of a public one, the one
			// a proxy in front lets through — and ask for another in the Host
			// header: a locked or geo-fenced site served under a public
			// site's name. Caddy issues one certificate per host (no
			// wildcard), so browsers never coalesce two hosts on one
			// connection and nothing legitimate is refused.
			"strict_sni_host": true,
		},
		"http_redirect": obj{
			"listen": arr{":80"},
			"routes": httpRoutes,
		},
	}
	// Domain mode only: in localhost mode nothing sits in front of zipgo.
	withProxyProtocol(servers, ProxyProtocolAllow())
	withTrustedProxies(servers, TrustedProxies())
	withTimeouts(servers)
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

func domainRoute(s sites.Site, host string) (obj, error) {
	h, err := siteHandler(s, host, "")
	if err != nil {
		return nil, err
	}
	return obj{
		"match":    arr{obj{"host": arr{host}}},
		"handle":   arr{h},
		"terminal": true,
	}, nil
}

// siteHandler builds the handler for a single site: a redirect subroute when the
// site declares a redirect target, a reverse_proxy subroute when it declares a
// rewrite upstream, otherwise the file_server subroute. A site that (wrongly)
// sets both redirect and rewrite redirects — doctor flags the combination.
// host is the site's host and stripPrefix the localhost path prefix to strip
// ("" in domain mode). A locked site (password and/or basicAuth) is wrapped so
// the check runs before anything is served — files, SPA fallback and proxied
// upstreams alike.
func siteHandler(s sites.Site, host, stripPrefix string) (obj, error) {
	inner, err := unguardedSiteHandler(s, host, stripPrefix, httpsOnly(s, stripPrefix))
	if err != nil {
		return nil, err
	}
	checks := lockHandlers(s, host, stripPrefix)
	// The flags come after the locks: a locked site's switches, and the paths
	// they close, are nobody's business before the login page.
	if h := flagsHandler(s, host, stripPrefix); h != nil {
		checks = append(checks, h)
	}
	if len(checks) == 0 {
		return inner, nil
	}
	routes := arr{}
	for _, h := range checks {
		routes = append(routes, obj{"handle": arr{h}})
	}
	return obj{"handler": "subroute", "routes": append(routes, obj{"handle": arr{inner}})}, nil
}

// lockHandlers are the checks a locked site runs before serving anything, in
// order: the login page first (a visitor with no session never sees anything
// else), then HTTP basic auth. Both run when both are set — two locks, two
// prompts; doctor warns about that combination. Empty for a public site.
func lockHandlers(s sites.Site, host, prefix string) arr {
	var hs arr
	if h := passwordHandler(s.Config, host, prefix); h != nil {
		hs = append(hs, h)
	}
	if h := basicAuthHandler(s.Config); h != nil {
		hs = append(hs, h)
	}
	return hs
}

// flagsHandler builds the feature-flags handler (internal/flags) for a site
// with a "flags" block, or nil when it has none: it serves FlagsPath and
// answers 404 on the paths of every flag that is off. A back-office has no
// features to flag.
func flagsHandler(s sites.Site, host, prefix string) obj {
	c := s.Config
	if !c.HasFlags() || c.IsAdmin() {
		return nil
	}
	h := obj{
		"handler": flags.HandlerName,
		"host":    host,
		"flags":   c.FlagValues(),
	}
	var closed []string
	for _, name := range sites.FlagNames(c.Flags) {
		f := c.Flags[name]
		if f.On {
			continue
		}
		for _, p := range f.Paths {
			if n := sites.NormalizeFlagPath(p); n != "" {
				closed = append(closed, n)
			}
		}
	}
	if len(closed) > 0 {
		h["closed"] = closed
	}
	if prefix != "" {
		h["prefix"] = prefix
	}
	return h
}

// passwordHandler builds the login-page handler (internal/gate) for a site's
// "password" block, or nil when it has none. prefix is the site's localhost
// path prefix; localhost mode is plain HTTP, so it also makes the session
// cookie non-Secure.
func passwordHandler(c sites.Config, host, prefix string) obj {
	if !c.HasPassword() {
		return nil
	}
	h := obj{
		"handler":      gate.HandlerName,
		"host":         host,
		"hash":         c.Password.Hash,
		"session_days": c.Password.Days(),
	}
	if t := strings.TrimSpace(c.Password.Title); t != "" {
		h["title"] = t
	}
	if prefix != "" {
		h["prefix"] = prefix
		h["insecure"] = true
	}
	return h
}

// lockedHeaders are the defaults a password site's responses get on top of the
// usual security headers (a site's own "headers" still override them):
// revalidate every time, so content a browser cached doesn't outlive a logout
// or a password change, and stay out of search engines even if a page leaks.
var lockedHeaders = map[string]string{
	"Cache-Control": "private, no-cache",
	"X-Robots-Tag":  "noindex, nofollow",
}

// siteHeaders is the custom-headers map a site's handlers apply: its own
// "headers", on top of lockedHeaders when it has a password.
func siteHeaders(c sites.Config) map[string]string {
	if !c.HasPassword() {
		return c.Headers
	}
	out := map[string]string{}
	for k, v := range lockedHeaders {
		out[k] = v
	}
	for k, v := range c.Headers {
		for d := range lockedHeaders {
			if strings.EqualFold(d, k) {
				delete(out, d)
			}
		}
		out[k] = v
	}
	return out
}

// unguardedSiteHandler picks what the site serves, before any auth check is
// layered on top: the back-office, a redirect, a proxied upstream, or its own
// files. hsts says whether the site is HTTPS-only (see httpsOnly).
func unguardedSiteHandler(s sites.Site, host, stripPrefix string, hsts bool) (obj, error) {
	if s.Config.IsAdmin() {
		return adminHandler(s, host, stripPrefix, hsts)
	}
	if s.Config.Redirect != "" {
		return redirectHandler(s, stripPrefix, hsts), nil
	}
	if s.Config.Rewrite != "" {
		return proxyHandler(s, host, stripPrefix, hsts), nil
	}
	absPath, err := filepath.Abs(s.Path)
	if err != nil {
		return nil, err
	}
	// Hide subdomain folders, the per-site config file, the deploy-history
	// snapshots (.zipgo-versions) and any symlink that leaves the site from the
	// file_server.
	hide := append(dotHide(absPath), filepath.Join(absPath, sites.ConfigFileName), filepath.Join(absPath, sites.VersionsDirName))
	hide = append(hide, escapingSymlinks(absPath)...)
	notFound := ""
	if s.HasNotFoundPage() {
		notFound = s.NotFoundPage
	}
	return fileHandler(absPath, fileOpts{
		spa:            s.IsSPA,
		browse:         s.Config.BrowseEnabled(),
		stripPrefix:    stripPrefix,
		hide:           hide,
		frameAncestors: authorizedOrigins(s),
		headers:        siteHeaders(s.Config),
		notFound:       notFound,
		hsts:           hsts,
	}), nil
}

// redirectHandler builds a subroute that answers every request for the site with
// a redirect to the site's configured target, instead of serving files. In
// localhost mode the site's path prefix is stripped first, so the placeholder in
// the Location header expands to the path *within* the site.
func redirectHandler(s sites.Site, stripPrefix string, hsts bool) obj {
	routes := arr{obj{"handle": arr{securityHeaders(authorizedOrigins(s), siteHeaders(s.Config), hsts)}}}
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
	if !c.HasBasicAuth() {
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

// guard prepends a site's lock checks (lockHandlers) to an already-built
// route's handler chain, so an endpoint served beside the site
// (sub-domains-meta) is protected with them rather than leaking the child
// listing of a password-protected site.
func guard(route obj, s sites.Site, host, prefix string) obj {
	checks := lockHandlers(s, host, prefix)
	if len(checks) == 0 {
		return route
	}
	if h, ok := route["handle"].(arr); ok {
		route["handle"] = append(checks, h...)
	}
	return route
}

// proxyHandler builds a reverse_proxy subroute forwarding to the site's rewrite
// upstream. It keeps the same security-headers handler as file routes (custom
// .zipgoconfig.json headers included), and (in localhost mode) strips the path
// prefix before proxying. When ZIPGO_REWRITE_ALLOW is set, an upstream it does
// not list is not proxied at all: the site answers 403 and the refusal is
// logged, so a deploy can't quietly publish an internal service.
func proxyHandler(s sites.Site, host, stripPrefix string, hsts bool) obj {
	dial, useTLS := proxyDial(s.Config.Rewrite)
	if allow := RewriteAllow(); !UpstreamAllowed(dial, allow) {
		log.Printf("🚫  %s: rewrite upstream %q is not in ZIPGO_REWRITE_ALLOW (%s) — the site answers 403",
			host, dial, strings.Join(allow, ","))
		return refusedUpstreamHandler(hsts)
	}
	routes := arr{obj{"handle": arr{securityHeaders(authorizedOrigins(s), siteHeaders(s.Config), hsts)}}}
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

// refusedUpstreamHandler is what a site whose rewrite upstream is outside
// ZIPGO_REWRITE_ALLOW serves: a plain 403 that names the fix for the operator
// and nothing about the upstream.
func refusedUpstreamHandler(hsts bool) obj {
	return obj{"handler": "subroute", "routes": arr{
		obj{"handle": arr{securityHeaders("", map[string]string{"Cache-Control": "no-store"}, hsts)}},
		obj{"handle": arr{obj{
			"handler":     "static_response",
			"status_code": "403",
			"headers":     obj{"Content-Type": arr{"text/plain; charset=utf-8"}},
			"body":        "403 Forbidden: this site's upstream is not allowed on this server (ZIPGO_REWRITE_ALLOW)\n",
		}}},
	}}
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

		// If a www. subdomain folder exists, the bare domain path redirects to
		// the www path. The route is placed just before the apex's own one —
		// after every subdomain's, which are sorted first — because its
		// "<apex>/*" matcher would otherwise swallow every subdomain path too.
		var wwwRoute obj
		if www {
			apexPath := "/" + ds.Domain
			wwwPath := apexPath + "/www"
			wwwRoute = obj{
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
			}
		}

		for _, s := range sorted {
			pathPrefix := s.LocalhostPath(ds.Domain)
			if s.IsApex() && wwwRoute != nil {
				routes = append(routes, wwwRoute)
			}
			if Gone(s) {
				routes = append(routes, obj{
					"match":    arr{obj{"path": arr{pathPrefix, pathPrefix + "/*"}}},
					"handle":   arr{goneHandler(false)},
					"terminal": true,
				})
				continue
			}
			if !served(s) {
				continue
			}

			// Serve <prefix>/sub-domains-meta for any site with child
			// subdomains, before the site's own routes so the path match wins.
			if cm := childMeta(s, ds.Sites); cm != nil {
				mr, err := metaRoute(obj{"path": arr{pathPrefix + MetaPath}}, cm)
				if err != nil {
					return nil, fmt.Errorf("domain %s host %s: %w", ds.Domain, s.LocalhostPath(ds.Domain), err)
				}
				mr = guard(mr, s, s.Host(ds.Domain), pathPrefix)
				routes = append(routes, mr)
			}

			h, err := siteHandler(s, s.Host(ds.Domain), pathPrefix)
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
	withTimeouts(servers)
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

// fileOpts is how a site's folder is served (see fileHandler).
type fileOpts struct {
	// spa: unmatched paths fall back to index.html.
	spa bool
	// browse: a folder without an index gets a directory listing. Off unless
	// the site's .zipgoconfig.json says "browse": true — a listing shows
	// every file a build left behind.
	browse bool
	// stripPrefix is the localhost-mode path prefix ("" in domain mode).
	stripPrefix string
	// hide lists absolute paths the file_server neither lists nor serves:
	// the nested subdomain folders, the config file, escaping symlinks.
	hide []string
	// frameAncestors is the site's zipgo:authorized-origins value.
	frameAncestors string
	// headers is the site's "headers" map, merged into the security headers.
	headers map[string]string
	// notFound is the site's custom 404 page (a file name, "" for none).
	notFound string
	// hsts: the site is HTTPS-only and says so.
	hsts bool
}

// fileHandler builds the subroute that serves a single site from root.
// When stripPrefix is non-empty (localhost mode) a strip_path_prefix rewrite is
// inserted so requests under /domain/name are served as if they were at root.
// Dotfiles are refused before the file server sees them (dotfileRoute). SPA
// sites fall back to index.html for unmatched paths. A custom 404 page, when
// the site has one, replaces Caddy's plain-text 404 body.
func fileHandler(root string, o fileOpts) obj {
	routes := arr{obj{"handle": arr{securityHeaders(o.frameAncestors, o.headers, o.hsts)}}}

	if o.stripPrefix != "" {
		routes = append(routes, obj{"handle": arr{obj{
			"handler":           "rewrite",
			"strip_path_prefix": o.stripPrefix,
		}}})
	}

	routes = append(routes, dotfileRoute())

	hide, notFound := o.hide, o.notFound
	if o.spa {
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
		}
		if o.browse {
			fs["browse"] = obj{}
		}
		if len(hide) > 0 {
			fs["hide"] = toArr(hide)
		}
		routes = append(routes, obj{"handle": arr{fs}})
	}

	sub := obj{"handler": "subroute", "routes": routes}
	if !o.spa && notFound != "" {
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
//
// hsts adds Strict-Transport-Security (hstsValue) for a site that is only ever
// served over HTTPS — a site that opted into plain HTTP must not promise
// browsers otherwise, and localhost mode is plain HTTP throughout.
func securityHeaders(frameAncestors string, custom map[string]string, hsts bool) obj {
	set := obj{
		"X-Content-Type-Options": arr{"nosniff"},
		"Referrer-Policy":        arr{"strict-origin-when-cross-origin"},
		"X-XSS-Protection":       arr{"0"},
		"Permissions-Policy":     arr{"camera=(), microphone=(), geolocation=(), payment=()"},
	}
	if hsts {
		set["Strict-Transport-Security"] = arr{hstsValue}
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
