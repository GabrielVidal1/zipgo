package builder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/caddyserver/caddy/v2"

	"zipgo/internal/sites"
)

func boolp(b bool) *bool { return &b }

// fileServers collects every file_server handler in a subroute, in order.
func fileServers(sub obj) []obj {
	var out []obj
	routes, _ := sub["routes"].(arr)
	for _, r := range routes {
		for _, h := range r.(obj)["handle"].(arr) {
			if h.(obj)["handler"] == "file_server" {
				out = append(out, h.(obj))
			}
		}
	}
	return out
}

// firstHeaders finds the first headers handler in a handler tree (subroutes
// included) and returns its response "set" map.
func firstHeaders(h obj) obj {
	if h["handler"] == "headers" {
		return h["response"].(obj)["set"].(obj)
	}
	routes, _ := h["routes"].(arr)
	for _, r := range routes {
		for _, inner := range r.(obj)["handle"].(arr) {
			if set := firstHeaders(inner.(obj)); set != nil {
				return set
			}
		}
	}
	return nil
}

// routeFor returns a server's site route for host (the one matching the host
// alone — not its sub-domains-meta route, which also matches a path).
func routeFor(t *testing.T, routes []any, host string) map[string]any {
	t.Helper()
	for _, r := range routes {
		route := r.(map[string]any)
		matchers, _ := route["match"].([]any)
		for _, m := range matchers {
			matcher := m.(map[string]any)
			if _, byPath := matcher["path"]; byPath {
				continue
			}
			for _, h := range matcher["host"].([]any) {
				if h == host {
					return route
				}
			}
		}
	}
	t.Fatalf("no route for %s", host)
	return nil
}

func TestSecurityHeadersHSTS(t *testing.T) {
	set := securityHeaders("", nil, true)["response"].(obj)["set"].(obj)
	if got := set["Strict-Transport-Security"]; !reflect.DeepEqual(got, arr{hstsValue}) {
		t.Fatalf("HTTPS-only site: Strict-Transport-Security = %v, want %q", got, hstsValue)
	}
	if _, ok := securityHeaders("", nil, false)["response"].(obj)["set"].(obj)["Strict-Transport-Security"]; ok {
		t.Fatal("a site served over HTTP must not send HSTS")
	}
	// A site can take it back, or change it, through its own headers.
	h := securityHeaders("", map[string]string{"strict-transport-security": ""}, true)
	if _, ok := h["response"].(obj)["set"].(obj)["Strict-Transport-Security"]; ok {
		t.Fatal("an empty override should delete HSTS")
	}
	if del := h["response"].(obj)["delete"]; !reflect.DeepEqual(del, arr{"Strict-Transport-Security"}) {
		t.Fatalf("delete list = %v", del)
	}
	h = securityHeaders("", map[string]string{"Strict-Transport-Security": "max-age=600"}, true)
	if got := h["response"].(obj)["set"].(obj)["Strict-Transport-Security"]; !reflect.DeepEqual(got, arr{"max-age=600"}) {
		t.Fatalf("override = %v", got)
	}
}

// Dotfiles are refused before the file server, static site and SPA alike,
// after the headers (so the 404 carries them) and the localhost prefix strip
// (so the check sees the path inside the site); /.well-known/ is exempt.
func TestFileHandlerRefusesDotfilesFirst(t *testing.T) {
	for _, spa := range []bool{false, true} {
		sub := fileHandler(t.TempDir(), fileOpts{spa: spa, stripPrefix: "/example.com/x"})
		names := handlerNames(sub)
		if len(names) < 4 || names[0] != "headers" || names[1] != "rewrite" || names[2] != "error" {
			t.Fatalf("spa=%v: handlers = %v, want headers, rewrite, error, …", spa, names)
		}
		dot := sub["routes"].(arr)[2].(obj)
		m := dot["match"].(arr)[0].(obj)
		if !reflect.DeepEqual(m["path"], arr{"*/.*"}) {
			t.Fatalf("spa=%v: dotfile matcher path = %v", spa, m["path"])
		}
		not := m["not"].(arr)[0].(obj)
		if !reflect.DeepEqual(not["path"], arr{"/.well-known/*"}) {
			t.Fatalf("spa=%v: /.well-known/ exemption = %v", spa, not["path"])
		}
		if h := dot["handle"].(arr)[0].(obj); h["handler"] != "error" || h["status_code"] != "404" {
			t.Fatalf("spa=%v: dotfile handler = %v", spa, h)
		}
	}
	// Domain mode: no prefix strip, the check comes right after the headers.
	if names := handlerNames(fileHandler(t.TempDir(), fileOpts{})); names[1] != "error" {
		t.Fatalf("domain mode handlers = %v", names)
	}
}

func TestBrowseIsOptIn(t *testing.T) {
	fs := fileServers(fileHandler(t.TempDir(), fileOpts{}))
	if len(fs) != 1 {
		t.Fatalf("static site: %d file servers", len(fs))
	}
	if _, ok := fs[0]["browse"]; ok {
		t.Fatal("a static site must not list folders unless it asks to")
	}
	fs = fileServers(fileHandler(t.TempDir(), fileOpts{browse: true}))
	if _, ok := fs[0]["browse"]; !ok {
		t.Fatal(`"browse": true should enable the listing`)
	}
	// An SPA answers index.html for every path: nothing to list, ever.
	for _, f := range fileServers(fileHandler(t.TempDir(), fileOpts{spa: true, browse: true})) {
		if _, ok := f["browse"]; ok {
			t.Fatal("an SPA must never list folders")
		}
	}
	if !(sites.Config{Browse: boolp(true)}).BrowseEnabled() || (sites.Config{}).BrowseEnabled() {
		t.Fatal("BrowseEnabled: default off, true when set")
	}
}

func TestEscapingSymlinks(t *testing.T) {
	root := t.TempDir()
	site := filepath.Join(root, "example.com")
	nested := filepath.Join(site, "pb.")
	for _, d := range []string{filepath.Join(site, "assets"), nested} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{
		filepath.Join(site, "index.html"), filepath.Join(site, "assets", "app.js"),
		filepath.Join(root, "secret.txt"), filepath.Join(nested, "index.html"),
	} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	links := map[string]string{ // link → target
		"app-link.js":  filepath.Join(site, "assets", "app.js"), // inside: fine
		"assets-rel":   "assets",                                // relative, inside: fine
		"leak.txt":     filepath.Join(root, "secret.txt"),       // outside the site
		"leak-rel.txt": "../secret.txt",                         // outside, relative
		"child":        nested,                                  // into a nested site
		"dangling":     filepath.Join(site, "nowhere"),          // a 404 anyway
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(site, name)); err != nil {
			t.Fatal(err)
		}
	}
	// A link inside the nested site is that site's business, not this one's.
	if err := os.Symlink(filepath.Join(root, "secret.txt"), filepath.Join(nested, "leak.txt")); err != nil {
		t.Fatal(err)
	}

	got := escapingSymlinks(site)
	sort.Strings(got)
	want := []string{filepath.Join(site, "child"), filepath.Join(site, "leak-rel.txt"), filepath.Join(site, "leak.txt")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("escaping symlinks = %v, want %v", got, want)
	}
}

func TestInsideSite(t *testing.T) {
	base := filepath.Join(string(filepath.Separator), "d", "a.com")
	cases := []struct {
		target string
		want   bool
	}{
		{filepath.Join(base, "x"), true},
		{base, true},
		{filepath.Join(base, "assets", "app.js"), true},
		{filepath.Join(string(filepath.Separator), "d", "b.com", "x"), false},
		{filepath.Join(base, "pb.", "x"), false}, // a nested site
		{base + "x", false},                      // a sibling that merely shares the prefix
		{filepath.Join(string(filepath.Separator), "d"), false},
	}
	for _, c := range cases {
		if got := insideSite(base, c.target); got != c.want {
			t.Errorf("insideSite(%s, %s) = %v, want %v", base, c.target, got, c.want)
		}
	}
}

func TestUpstreamAllowed(t *testing.T) {
	if !UpstreamAllowed("anything:1", nil) {
		t.Fatal("no allowlist: everything is allowed")
	}
	allow := []string{"*-pocketbase:8090", "3d-gallery:80", "100.104.50.115:80"}
	for _, dial := range []string{"moooo-pocketbase:8090", "3d-gallery:80", "100.104.50.115:80"} {
		if !UpstreamAllowed(dial, allow) {
			t.Errorf("%s should be allowed", dial)
		}
	}
	for _, dial := range []string{"moooo-pocketbase:8091", "localhost:2019", "100.104.50.115:8096", "3d-gallery:8080", ""} {
		if UpstreamAllowed(dial, allow) {
			t.Errorf("%s should be refused", dial)
		}
	}
	t.Setenv("ZIPGO_REWRITE_ALLOW", " *-pocketbase:8090 , 3d-gallery:80 ,")
	if got := RewriteAllow(); !reflect.DeepEqual(got, []string{"*-pocketbase:8090", "3d-gallery:80"}) {
		t.Fatalf("RewriteAllow = %v", got)
	}
	t.Setenv("ZIPGO_REWRITE_ALLOW", "")
	if RewriteAllow() != nil {
		t.Fatal("unset: nil")
	}
}

func TestProxyHandlerRefusesUnlistedUpstream(t *testing.T) {
	t.Setenv("ZIPGO_REWRITE_ALLOW", "*-pocketbase:8090")
	h := proxyHandler(sites.Site{Config: sites.Config{Rewrite: "pb-pocketbase:8090"}}, "pb.example.test", "", true)
	if names := handlerNames(h); names[len(names)-1] != "reverse_proxy" {
		t.Fatalf("listed upstream: handlers = %v", names)
	}
	h = proxyHandler(sites.Site{Config: sites.Config{Rewrite: "100.64.0.1:8096"}}, "x.example.test", "", true)
	names := handlerNames(h)
	if !reflect.DeepEqual(names, []string{"headers", "static_response"}) {
		t.Fatalf("unlisted upstream: handlers = %v", names)
	}
	resp := h["routes"].(arr)[1].(obj)["handle"].(arr)[0].(obj)
	if resp["status_code"] != "403" {
		t.Fatalf("unlisted upstream: status %v", resp["status_code"])
	}
	if _, ok := firstHeaders(h)["Strict-Transport-Security"]; !ok {
		t.Fatal("the refusal keeps the site's security headers")
	}
	t.Setenv("ZIPGO_REWRITE_ALLOW", "")
	if names := handlerNames(proxyHandler(sites.Site{Config: sites.Config{Rewrite: "100.64.0.1:8096"}}, "x.example.test", "", false)); names[len(names)-1] != "reverse_proxy" {
		t.Fatalf("no allowlist: handlers = %v", names)
	}
}

func TestTrustedProxies(t *testing.T) {
	t.Setenv("ZIPGO_PROXY_PROTOCOL_ALLOW", "172.18.0.0/16")
	t.Setenv("ZIPGO_TRUSTED_PROXIES", "")
	if got := TrustedProxies(); !reflect.DeepEqual(got, []string{"172.18.0.0/16"}) {
		t.Fatalf("fallback to the PROXY protocol sources: %v", got)
	}
	t.Setenv("ZIPGO_TRUSTED_PROXIES", "10.0.0.0/8, fd00::/8")
	if got := TrustedProxies(); !reflect.DeepEqual(got, []string{"10.0.0.0/8", "fd00::/8"}) {
		t.Fatalf("explicit: %v", got)
	}
	t.Setenv("ZIPGO_PROXY_PROTOCOL_ALLOW", "")
	t.Setenv("ZIPGO_TRUSTED_PROXIES", "")
	if TrustedProxies() != nil {
		t.Fatal("nothing set: trust nobody")
	}
	servers := obj{"https": obj{}, "metrics": obj{}}
	withTrustedProxies(servers, []string{"172.18.0.0/16"})
	tp := servers["https"].(obj)["trusted_proxies"].(obj)
	if tp["source"] != "static" || !reflect.DeepEqual(tp["ranges"], arr{"172.18.0.0/16"}) {
		t.Fatalf("trusted_proxies = %v", tp)
	}
	if _, ok := servers["metrics"].(obj)["trusted_proxies"]; ok {
		t.Fatal("the metrics server is never behind the proxy")
	}
	withTrustedProxies(obj{"https": obj{}}, nil) // no-op, no panic
}

// The domain-mode config carries every hardening default, and real Caddy
// accepts it.
func TestBuildConfigHardening(t *testing.T) {
	t.Setenv("ZIPGO_PROXY_PROTOCOL_ALLOW", "172.18.0.0/16")
	t.Setenv("ZIPGO_TRUSTED_PROXIES", "")
	t.Setenv("ZIPGO_REWRITE_ALLOW", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	apex := site(t, root, nil, "Example", sites.Config{})
	plain := site(t, root, []string{"plain"}, "Plain", sites.Config{AllowHTTP: boolp(true)})
	lockedCfg := passwordConfig()
	lockedCfg.AllowHTTP = boolp(true) // ignored: a password site is HTTPS-only
	locked := site(t, root, []string{"locked"}, "Locked", lockedCfg)
	off := site(t, root, []string{"old"}, "Old", sites.Config{Enable: boolp(false)})
	proxy := site(t, root, []string{"pb"}, "PB", sites.Config{Rewrite: "pb:8090"})
	all := []sites.Site{apex, plain, locked, off, proxy}
	for i := range all[1:] {
		all[i+1].Disabled = all[i+1].Config.Enable != nil && !*all[i+1].Config.Enable
	}
	cfg, err := BuildConfig([]DomainSites{{Domain: "example.com", Sites: all}}, "", "json")
	if err != nil {
		t.Fatal(err)
	}

	var app map[string]any
	if err := json.Unmarshal(cfg.AppsRaw["http"], &app); err != nil {
		t.Fatal(err)
	}
	servers := app["servers"].(map[string]any)
	https := servers["https"].(map[string]any)
	if https["strict_sni_host"] != true {
		t.Fatal("HTTPS server: strict_sni_host must be on")
	}
	if _, ok := servers["http_redirect"].(map[string]any)["strict_sni_host"]; ok {
		t.Fatal("the :80 server has no SNI to be strict about")
	}
	for name, s := range servers {
		srv := s.(map[string]any)
		if srv["read_header_timeout"] != readHeaderTimeout || srv["idle_timeout"] != idleTimeout {
			t.Fatalf("%s: timeouts = %v / %v", name, srv["read_header_timeout"], srv["idle_timeout"])
		}
		tp, _ := srv["trusted_proxies"].(map[string]any)
		if tp == nil || tp["source"] != "static" {
			t.Fatalf("%s: trusted_proxies = %v", name, srv["trusted_proxies"])
		}
	}
	routes := https["routes"].([]any)
	last := routes[len(routes)-1].(map[string]any)
	if _, matched := last["match"]; matched {
		t.Fatal("the last HTTPS route must be the catch-all")
	}
	if h := last["handle"].([]any)[0].(map[string]any); h["handler"] != "static_response" || h["status_code"] != "421" {
		t.Fatalf("catch-all = %v", h)
	}

	// HSTS: every HTTPS-only site, including a switched-off one and a
	// password site whatever its allowHttp; not a site served on :80.
	hsts := func(host string) bool {
		raw, _ := json.Marshal(routeFor(t, routes, host)["handle"].([]any)[0])
		var h obj
		if err := json.Unmarshal(raw, &h); err != nil {
			t.Fatal(err)
		}
		set := firstHeaders(h)
		if set == nil {
			t.Fatalf("%s: no headers handler", host)
		}
		_, ok := set["Strict-Transport-Security"]
		return ok
	}
	for host, want := range map[string]bool{
		"example.com":        true,
		"plain.example.com":  false,
		"locked.example.com": true,
		"old.example.com":    true,
		"pb.example.com":     true,
	} {
		if got := hsts(host); got != want {
			t.Errorf("%s: HSTS %v, want %v", host, got, want)
		}
	}

	// The access log drops the unlock password from the logged URI.
	var enc map[string]any
	if err := json.Unmarshal(cfg.Logging.Logs["access"].EncoderRaw, &enc); err != nil {
		t.Fatal(err)
	}
	if enc["format"] != "filter" || enc["wrap"].(map[string]any)["format"] != "json" {
		t.Fatalf("access encoder = %v", enc)
	}
	uri := enc["fields"].(map[string]any)["request>uri"].(map[string]any)
	action := uri["actions"].([]any)[0].(map[string]any)
	if uri["filter"] != "query" || action["type"] != "delete" || action["parameter"] != "__zipgo_pwd" {
		t.Fatalf("request>uri filter = %v", uri)
	}

	if err := caddy.Validate(cfg); err != nil {
		t.Fatalf("caddy rejected the config: %v", err)
	}
}

// Localhost mode is plain HTTP on loopback: no HSTS, no SNI, but the same
// connection limits.
func TestBuildLocalhostConfigHardening(t *testing.T) {
	root := t.TempDir()
	apex := site(t, root, nil, "Example", sites.Config{})
	cfg, err := BuildLocalhostConfig([]DomainSites{{Domain: "example.com", Sites: []sites.Site{apex}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var app map[string]any
	if err := json.Unmarshal(cfg.AppsRaw["http"], &app); err != nil {
		t.Fatal(err)
	}
	srv := app["servers"].(map[string]any)["sites"].(map[string]any)
	if _, ok := srv["strict_sni_host"]; ok {
		t.Fatal("localhost: no TLS, no strict SNI")
	}
	if srv["read_header_timeout"] != readHeaderTimeout {
		t.Fatalf("localhost: read_header_timeout = %v", srv["read_header_timeout"])
	}
	raw, _ := json.Marshal(srv["routes"].([]any)[0].(map[string]any)["handle"].([]any)[0])
	var h obj
	if err := json.Unmarshal(raw, &h); err != nil {
		t.Fatal(err)
	}
	if _, ok := firstHeaders(h)["Strict-Transport-Security"]; ok {
		t.Fatal("localhost: plain HTTP must not send HSTS")
	}
}

// A control character in the framing meta tag never reaches a header.
func TestAuthorizedOriginsStripsControlCharacters(t *testing.T) {
	dir := t.TempDir()
	html := "<html><head><meta property=\"zipgo:authorized-origins\" content=\"https://a.example\r\nX-Injected: 1\"></head></html>"
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := authorizedOrigins(sites.Site{Path: dir}); got != "https://a.exampleX-Injected: 1" {
		t.Fatalf("authorizedOrigins = %q", got)
	}
}
