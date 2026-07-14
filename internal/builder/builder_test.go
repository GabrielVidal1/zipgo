package builder

import (
	"reflect"
	"testing"

	"zipgo/internal/sites"
)

func TestMetricsAddr(t *testing.T) {
	t.Setenv("ZIPGO_METRICS", "")
	t.Setenv("ZIPGO_METRICS_ADDR", "")
	if got := MetricsAddr(); got != "" {
		t.Fatalf("metrics off: want \"\", got %q", got)
	}

	t.Setenv("ZIPGO_METRICS", "1")
	if got := MetricsAddr(); got != DefaultMetricsAddr {
		t.Fatalf("metrics on: want %q, got %q", DefaultMetricsAddr, got)
	}

	t.Setenv("ZIPGO_METRICS_ADDR", "0.0.0.0:9100")
	if got := MetricsAddr(); got != "0.0.0.0:9100" {
		t.Fatalf("custom addr: want 0.0.0.0:9100, got %q", got)
	}
}

func TestWithMetrics(t *testing.T) {
	// Disabled: no-op, no metrics server, no per-server flag.
	servers := obj{"sites": obj{"listen": arr{":9000"}}}
	withMetrics(servers, "")
	if _, ok := servers["metrics"]; ok {
		t.Fatal("disabled: should not add a metrics server")
	}
	if _, ok := servers["sites"].(obj)["metrics"]; ok {
		t.Fatal("disabled: should not enable per-server metrics")
	}

	// Enabled: adds dedicated server + per-server metrics flag.
	servers = obj{"sites": obj{"listen": arr{":9000"}}}
	withMetrics(servers, "127.0.0.1:2019")
	ms, ok := servers["metrics"].(obj)
	if !ok {
		t.Fatal("enabled: missing metrics server")
	}
	if got := ms["listen"].(arr)[0]; got != "127.0.0.1:2019" {
		t.Fatalf("metrics server listen: got %v", got)
	}
	h := ms["routes"].(arr)[0].(obj)["handle"].(arr)[0].(obj)["handler"]
	if h != "metrics" {
		t.Fatalf("metrics route handler: want metrics, got %v", h)
	}
	if _, ok := servers["sites"].(obj)["metrics"]; !ok {
		t.Fatal("enabled: per-server metrics flag not set")
	}
}

func TestBuildConfigWithMetrics(t *testing.T) {
	cfg, err := BuildConfig(nil, "127.0.0.1:2019")
	if err != nil {
		t.Fatalf("BuildConfig: %v", err)
	}
	if cfg.AppsRaw["http"] == nil {
		t.Fatal("missing http app")
	}
}

func TestProxyDial(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantTLS bool
	}{
		{"localhost:8080", "localhost:8080", false},
		{"127.0.0.1:3000", "127.0.0.1:3000", false},
		{"http://api.example.com", "api.example.com:80", false},
		{"https://api.example.com", "api.example.com:443", true},
		{"https://api.example.com:8443", "api.example.com:8443", true},
		{"  localhost:9000  ", "localhost:9000", false},
	}
	for _, c := range cases {
		got, tls := proxyDial(c.in)
		if got != c.want || tls != c.wantTLS {
			t.Errorf("proxyDial(%q) = (%q, %v), want (%q, %v)", c.in, got, tls, c.want, c.wantTLS)
		}
	}
}

func TestServed(t *testing.T) {
	tru, fls := true, false
	dir := t.TempDir() // no index.html inside

	// No index, no rewrite → not served.
	if served(sites.Site{Path: dir}) {
		t.Error("empty dir should not be served")
	}
	// Rewrite upstream → served even without an index.html.
	if !served(sites.Site{Path: dir, Config: sites.Config{Rewrite: "localhost:8080"}}) {
		t.Error("rewrite site should be served")
	}
	// Explicitly disabled → not served, even with a rewrite.
	if served(sites.Site{Path: dir, Config: sites.Config{Enable: &fls, Rewrite: "localhost:8080"}}) {
		t.Error("disabled site should not be served")
	}
	// enable:true with a rewrite → served.
	if !served(sites.Site{Path: dir, Config: sites.Config{Enable: &tru, Rewrite: "localhost:8080"}}) {
		t.Error("enabled rewrite site should be served")
	}
}

func TestServedRedirect(t *testing.T) {
	fls := false
	dir := t.TempDir() // no index.html inside

	// Redirect target → served even without an index.html.
	if !served(sites.Site{Path: dir, Config: sites.Config{Redirect: "https://elsewhere.example"}}) {
		t.Error("redirect site should be served")
	}
	// Explicitly disabled → not served, even with a redirect.
	if served(sites.Site{Path: dir, Config: sites.Config{Enable: &fls, Redirect: "https://elsewhere.example"}}) {
		t.Error("disabled redirect site should not be served")
	}
}

func TestRedirectLocation(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// A bare origin keeps the request's path and query.
		{"https://elsewhere.example", "https://elsewhere.example{http.request.uri}"},
		{"https://elsewhere.example/", "https://elsewhere.example{http.request.uri}"},
		{"http://elsewhere.example:8080", "http://elsewhere.example:8080{http.request.uri}"},
		{"  https://elsewhere.example  ", "https://elsewhere.example{http.request.uri}"},
		// A target with a path/query/fragment is used verbatim.
		{"https://elsewhere.example/moved", "https://elsewhere.example/moved"},
		{"https://elsewhere.example/?ref=old", "https://elsewhere.example/?ref=old"},
		{"https://elsewhere.example/#here", "https://elsewhere.example/#here"},
	}
	for _, c := range cases {
		if got := redirectLocation(c.in); got != c.want {
			t.Errorf("redirectLocation(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRedirectHandler(t *testing.T) {
	// Default status: 302, path-preserving Location.
	h := redirectHandler(sites.Site{Config: sites.Config{Redirect: "https://elsewhere.example"}}, "")
	routes := h["routes"].(arr)
	sr := routes[len(routes)-1].(obj)["handle"].(arr)[0].(obj)
	if sr["handler"] != "static_response" {
		t.Fatalf("want static_response, got %v", sr["handler"])
	}
	if sr["status_code"] != "302" {
		t.Errorf("status_code: want 302 (the default), got %v", sr["status_code"])
	}
	loc := sr["headers"].(obj)["Location"]
	if !reflect.DeepEqual(loc, arr{"https://elsewhere.example{http.request.uri}"}) {
		t.Errorf("Location: got %v", loc)
	}

	// Explicit status, and (localhost mode) the site prefix is stripped before
	// the Location placeholder expands.
	h = redirectHandler(
		sites.Site{Config: sites.Config{Redirect: "https://elsewhere.example", RedirectStatus: 301}},
		"/example.com/old",
	)
	routes = h["routes"].(arr)
	rw := routes[1].(obj)["handle"].(arr)[0].(obj)
	if rw["handler"] != "rewrite" || rw["strip_path_prefix"] != "/example.com/old" {
		t.Errorf("localhost mode should strip the site prefix first, got %v", rw)
	}
	sr = routes[len(routes)-1].(obj)["handle"].(arr)[0].(obj)
	if sr["status_code"] != "301" {
		t.Errorf("status_code: want 301, got %v", sr["status_code"])
	}
}

// A redirect replaces the file server, and wins over a rewrite upstream.
func TestSiteHandlerRedirectWins(t *testing.T) {
	h, err := siteHandler(sites.Site{
		Path:   t.TempDir(),
		Config: sites.Config{Redirect: "https://elsewhere.example", Rewrite: "localhost:8080"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	routes := h["routes"].(arr)
	last := routes[len(routes)-1].(obj)["handle"].(arr)[0].(obj)
	if last["handler"] != "static_response" {
		t.Fatalf("redirect should win over rewrite, got handler %v", last["handler"])
	}
}

func TestProxyHandlerTLS(t *testing.T) {
	h := proxyHandler(sites.Site{Config: sites.Config{Rewrite: "https://api.example.com"}}, "")
	last := h["routes"].(arr)[len(h["routes"].(arr))-1].(obj)
	rp := last["handle"].(arr)[0].(obj)
	if rp["handler"] != "reverse_proxy" {
		t.Fatalf("want reverse_proxy, got %v", rp["handler"])
	}
	if rp["transport"] == nil {
		t.Fatal("https upstream should set a TLS transport")
	}
	dial := rp["upstreams"].(arr)[0].(obj)["dial"]
	if dial != "api.example.com:443" {
		t.Fatalf("dial: want api.example.com:443, got %v", dial)
	}
}

func TestSecurityHeaders(t *testing.T) {
	// Default (no authorized origins): clickjacking-safe X-Frame-Options, no CSP.
	def := securityHeaders("", nil)["response"].(obj)["set"].(obj)
	if got := def["X-Frame-Options"]; !reflect.DeepEqual(got, arr{"SAMEORIGIN"}) {
		t.Fatalf("default X-Frame-Options: want [SAMEORIGIN], got %v", got)
	}
	if _, ok := def["Content-Security-Policy"]; ok {
		t.Fatal("default: should not set Content-Security-Policy")
	}

	// With authorized origins: CSP frame-ancestors, and X-Frame-Options dropped
	// (the two conflict).
	set := securityHeaders("https://*.gabvdl.xyz", nil)["response"].(obj)["set"].(obj)
	if _, ok := set["X-Frame-Options"]; ok {
		t.Fatal("with origins: X-Frame-Options must be omitted")
	}
	want := arr{"frame-ancestors 'self' https://*.gabvdl.xyz"}
	if got := set["Content-Security-Policy"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("CSP: want %v, got %v", want, got)
	}
}

// bcryptHash is a real hash (`htpasswd -nbB claude s3cret`).
const bcryptHash = "$2y$05$lxJfaQMx3WaZsB7K5ZvLz.5Sw1mdcAx2fwJycu2oQJfxqLSySlTK."

func protectedConfig() sites.Config {
	return sites.Config{BasicAuth: map[string]string{"claude": bcryptHash}}
}

// authOf digs the authentication handler out of a site handler, or nil when the
// site is public.
func authOf(h obj) obj {
	routes, ok := h["routes"].(arr)
	if !ok || len(routes) == 0 {
		return nil
	}
	first, ok := routes[0].(obj)["handle"].(arr)
	if !ok || len(first) == 0 {
		return nil
	}
	handler, ok := first[0].(obj)
	if !ok || handler["handler"] != "authentication" {
		return nil
	}
	return handler
}

func TestBasicAuthHandler(t *testing.T) {
	if h := basicAuthHandler(sites.Config{}); h != nil {
		t.Fatalf("public site: want no auth handler, got %v", h)
	}
	if h := basicAuthHandler(sites.Config{BasicAuth: map[string]string{}}); h != nil {
		t.Fatalf("empty basicAuth: want no auth handler, got %v", h)
	}

	h := basicAuthHandler(protectedConfig())
	basic := h["providers"].(obj)["http_basic"].(obj)
	if got := basic["hash"].(obj)["algorithm"]; got != "bcrypt" {
		t.Errorf("hash algorithm = %v, want bcrypt", got)
	}
	accounts := basic["accounts"].(arr)
	if len(accounts) != 1 {
		t.Fatalf("accounts = %d, want 1", len(accounts))
	}
	acct := accounts[0].(obj)
	if acct["username"] != "claude" || acct["password"] != bcryptHash {
		t.Errorf("account = %v, want claude/%s", acct, bcryptHash)
	}
}

// The auth check must run before anything is served, whatever the site is:
// static files, an SPA, or a proxied upstream.
func TestSiteHandlerBasicAuth(t *testing.T) {
	tests := []struct {
		name string
		site sites.Site
		want bool
	}{
		{"public static site", sites.Site{Path: "."}, false},
		{"protected static site", sites.Site{Path: ".", Config: protectedConfig()}, true},
		{"protected SPA", sites.Site{Path: ".", IsSPA: true, Config: protectedConfig()}, true},
		{"public proxy", sites.Site{Path: ".", Config: sites.Config{Rewrite: "localhost:8080"}}, false},
		{"protected proxy", sites.Site{Path: ".", Config: sites.Config{
			Rewrite:   "localhost:8080",
			BasicAuth: map[string]string{"claude": bcryptHash},
		}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, err := siteHandler(tc.site, "")
			if err != nil {
				t.Fatalf("siteHandler: %v", err)
			}
			if got := authOf(h) != nil; got != tc.want {
				t.Fatalf("auth guard = %v, want %v (handler: %v)", got, tc.want, h)
			}
		})
	}
}

// A protected site's sub-domains-meta endpoint must not leak its child listing.
func TestGuardMetaRoute(t *testing.T) {
	route := obj{"handle": arr{obj{"handler": "static_response"}}}

	public := guard(obj{"handle": arr{obj{"handler": "static_response"}}}, sites.Site{})
	if h := public["handle"].(arr); len(h) != 1 {
		t.Fatalf("public meta route: want 1 handler, got %d", len(h))
	}

	protected := guard(route, sites.Site{Config: protectedConfig()})
	h := protected["handle"].(arr)
	if len(h) != 2 {
		t.Fatalf("protected meta route: want 2 handlers, got %d", len(h))
	}
	if got := h[0].(obj)["handler"]; got != "authentication" {
		t.Fatalf("first handler = %v, want authentication", got)
	}
	if got := h[1].(obj)["handler"]; got != "static_response" {
		t.Fatalf("second handler = %v, want static_response", got)
	}
}

// The .zipgoconfig.json "headers" map is *merged into* the security headers:
// it adds its own, may override a default by name, but never drops the rest.
func TestSecurityHeadersCustom(t *testing.T) {
	h := securityHeaders("", map[string]string{
		"Cache-Control":               "public, max-age=31536000, immutable",
		"access-control-allow-origin": "*",             // canonicalised
		"X-XSS-Protection":            "1; mode=block", // overrides a default
		"Referrer-Policy":             "",              // empty → delete
	})
	set := h["response"].(obj)["set"].(obj)

	// Added.
	if got := set["Cache-Control"]; !reflect.DeepEqual(got, arr{"public, max-age=31536000, immutable"}) {
		t.Errorf("Cache-Control: got %v", got)
	}
	// Canonicalised, so the header goes out as Access-Control-Allow-Origin.
	if got := set["Access-Control-Allow-Origin"]; !reflect.DeepEqual(got, arr{"*"}) {
		t.Errorf("Access-Control-Allow-Origin: got %v", got)
	}
	// Overrides the default of the same name (and only that one).
	if got := set["X-XSS-Protection"]; !reflect.DeepEqual(got, arr{"1; mode=block"}) {
		t.Errorf("X-XSS-Protection override: got %v", got)
	}
	// Empty value → deleted from the defaults and from the upstream response.
	if _, ok := set["Referrer-Policy"]; ok {
		t.Error("Referrer-Policy: empty value should remove the header")
	}
	if got := h["response"].(obj)["delete"]; !reflect.DeepEqual(got, arr{"Referrer-Policy"}) {
		t.Errorf("delete list: got %v", got)
	}
	// Untouched defaults survive the merge — this is the "merge, not clobber" bit.
	if got := set["X-Content-Type-Options"]; !reflect.DeepEqual(got, arr{"nosniff"}) {
		t.Errorf("X-Content-Type-Options should survive: got %v", got)
	}
	if got := set["X-Frame-Options"]; !reflect.DeepEqual(got, arr{"SAMEORIGIN"}) {
		t.Errorf("X-Frame-Options should survive: got %v", got)
	}
	if got := set["Permissions-Policy"]; got == nil {
		t.Error("Permissions-Policy should survive")
	}

	// No custom headers → no delete key at all (config stays as it was).
	if _, ok := securityHeaders("", nil)["response"].(obj)["delete"]; ok {
		t.Error("no custom headers: should not emit a delete list")
	}
}

// A site with custom headers gets them on both the file and the proxy route.
func TestCustomHeadersOnBothHandlers(t *testing.T) {
	cfg := sites.Config{Headers: map[string]string{"Cache-Control": "no-store"}}

	fileSub := fileHandler(t.TempDir(), false, "", nil, "", cfg.Headers)
	fileHdr := fileSub["routes"].(arr)[0].(obj)["handle"].(arr)[0].(obj)
	if got := fileHdr["response"].(obj)["set"].(obj)["Cache-Control"]; !reflect.DeepEqual(got, arr{"no-store"}) {
		t.Errorf("file route: Cache-Control not applied, got %v", got)
	}

	cfg.Rewrite = "localhost:8080"
	proxySub := proxyHandler(sites.Site{Config: cfg}, "")
	proxyHdr := proxySub["routes"].(arr)[0].(obj)["handle"].(arr)[0].(obj)
	if got := proxyHdr["response"].(obj)["set"].(obj)["Cache-Control"]; !reflect.DeepEqual(got, arr{"no-store"}) {
		t.Errorf("proxy route: Cache-Control not applied, got %v", got)
	}
}
