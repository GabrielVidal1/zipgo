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
	def := securityHeaders("")["response"].(obj)["set"].(obj)
	if got := def["X-Frame-Options"]; !reflect.DeepEqual(got, arr{"SAMEORIGIN"}) {
		t.Fatalf("default X-Frame-Options: want [SAMEORIGIN], got %v", got)
	}
	if _, ok := def["Content-Security-Policy"]; ok {
		t.Fatal("default: should not set Content-Security-Policy")
	}

	// With authorized origins: CSP frame-ancestors, and X-Frame-Options dropped
	// (the two conflict).
	set := securityHeaders("https://*.gabvdl.xyz")["response"].(obj)["set"].(obj)
	if _, ok := set["X-Frame-Options"]; ok {
		t.Fatal("with origins: X-Frame-Options must be omitted")
	}
	want := arr{"frame-ancestors 'self' https://*.gabvdl.xyz"}
	if got := set["Content-Security-Policy"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("CSP: want %v, got %v", want, got)
	}
}
