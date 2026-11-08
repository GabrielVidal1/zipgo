package builder

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"

	"zipgo/internal/sites"
)

func TestHTTP3(t *testing.T) {
	for _, c := range []struct {
		env, proxy string
		want       bool
	}{
		{"", "", true},                // standalone: QUIC reaches it
		{"", "172.18.0.0/16", false},  // behind a TCP proxy: it can't
		{"on", "172.18.0.0/16", true}, // the operator forwards UDP 443 too
		{"off", "", false},
		{"  OFF ", "", false},
		{"bogus", "", true}, // unknown value: the default
	} {
		t.Setenv("ZIPGO_HTTP3", c.env)
		t.Setenv("ZIPGO_PROXY_PROTOCOL_ALLOW", c.proxy)
		if got := HTTP3(); got != c.want {
			t.Errorf("ZIPGO_HTTP3=%q PROXY=%q: HTTP3() = %v, want %v", c.env, c.proxy, got, c.want)
		}
	}
}

func TestBuildConfigProtocols(t *testing.T) {
	protocols := func() any {
		cfg, err := BuildConfig(nil, "", "")
		if err != nil {
			t.Fatal(err)
		}
		m := configJSON(t, cfg.AppsRaw["http"])
		return m["servers"].(map[string]any)["https"].(map[string]any)["protocols"]
	}
	t.Setenv("ZIPGO_HTTP3", "")
	t.Setenv("ZIPGO_PROXY_PROTOCOL_ALLOW", "")
	if p := protocols(); p != nil {
		t.Fatalf("standalone: protocols = %v, want Caddy's default (h1 h2 h3)", p)
	}
	t.Setenv("ZIPGO_PROXY_PROTOCOL_ALLOW", "172.18.0.0/16")
	if p := fmt.Sprint(protocols()); p != "[h1 h2]" {
		t.Fatalf("behind a proxy: protocols = %v, want [h1 h2] (no Alt-Svc: h3)", p)
	}
}

// TestServingCompressionAndCache runs the generated localhost config in a real
// Caddy and checks what a browser gets: a deploy's pre-compressed sibling, the
// on-the-fly fallback, and the Cache-Control of hashed and ordinary files.
func TestServingCompressionAndCache(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "example.com")
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	js := strings.Repeat("console.log('zipgo serves this');\n", 200) // ~7 KB
	page := "<!doctype html><title>x</title>" + strings.Repeat("<p>hello</p>", 200)

	// The apex: an SPA (index.html + assets/).
	write("index.html", page)
	write("assets/index-F6vy2umU.js", js)
	write("assets/index-F6vy2umU.js.br", "BROTLI-SIBLING") // a deploy's sibling
	write("assets/vendor-Ab12Cd34.js", js)                 // no sibling: on the fly
	write("assets/logo.png", "not really a png")
	// A site that sets its own Cache-Control keeps it everywhere.
	write("own./index.html", page)
	write("own./assets/app-Ab12Cd34.js", js)
	write("own./"+sites.ConfigFileName, `{"headers": {"Cache-Control": "max-age=60"}}`)
	// A site that opted out of on-the-fly compression.
	write("raw./index.html", page)
	write("raw./"+sites.ConfigFileName, `{"compress": false}`)

	found, err := sites.Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := BuildLocalhostConfig([]DomainSites{{Domain: "example.com", Sites: found}}, "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Listen on a free port instead of 9000 (other zipgo instances use it).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	var app map[string]any
	if err := json.Unmarshal(cfg.AppsRaw["http"], &app); err != nil {
		t.Fatal(err)
	}
	app["servers"].(map[string]any)["sites"].(map[string]any)["listen"] = []any{addr}
	raw, _ := json.Marshal(app)
	cfg.AppsRaw["http"] = raw
	if err := caddy.Run(cfg); err != nil {
		t.Fatalf("caddy run: %v", err)
	}
	t.Cleanup(func() { _ = caddy.Stop() })

	get := func(path, enc string, hdr ...string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("GET", "http://"+addr+path, nil)
		if enc != "" {
			req.Header.Set("Accept-Encoding", enc)
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		// A transport that never adds its own Accept-Encoding or decodes.
		tr := &http.Transport{DisableCompression: true}
		res, err := (&http.Client{Transport: tr}).Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	body := func(res *http.Response) string {
		b, _ := io.ReadAll(res.Body)
		return string(b)
	}

	// The deploy's brotli sibling, untouched, and immutable.
	res := get("/example.com/assets/index-F6vy2umU.js", "br, gzip")
	if got := body(res); got != "BROTLI-SIBLING" || res.Header.Get("Content-Encoding") != "br" {
		t.Fatalf("hashed js with a .br sibling: encoding %q body %q", res.Header.Get("Content-Encoding"), got)
	}
	if cc := res.Header.Get("Cache-Control"); cc != immutableCache {
		t.Fatalf("hashed js: Cache-Control %q", cc)
	}
	// A client without brotli gets the original, compressed on the fly.
	res = get("/example.com/assets/index-F6vy2umU.js", "gzip")
	if res.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip-only client: encoding %q", res.Header.Get("Content-Encoding"))
	}
	zr, err := gzip.NewReader(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	if string(plain) != js {
		t.Fatal("gzip-only client: decoded body differs from the file")
	}
	// No sibling: compressed on the fly, zstd preferred.
	res = get("/example.com/assets/vendor-Ab12Cd34.js", "gzip, zstd")
	if res.Header.Get("Content-Encoding") != "zstd" || !strings.Contains(res.Header.Get("Vary"), "Accept-Encoding") {
		t.Fatalf("on-the-fly: encoding %q vary %q", res.Header.Get("Content-Encoding"), res.Header.Get("Vary"))
	}
	if cc := res.Header.Get("Cache-Control"); cc != immutableCache {
		t.Fatalf("hashed js on the fly: Cache-Control %q", cc)
	}
	// A revalidation of a hashed file keeps it immutable.
	etag := get("/example.com/assets/vendor-Ab12Cd34.js", "").Header.Get("Etag")
	res = get("/example.com/assets/vendor-Ab12Cd34.js", "", "If-None-Match", etag)
	if res.StatusCode != http.StatusNotModified || res.Header.Get("Cache-Control") != immutableCache {
		t.Fatalf("304: status %d Cache-Control %q", res.StatusCode, res.Header.Get("Cache-Control"))
	}

	// Ordinary files are revalidated.
	for _, p := range []string{"/example.com/", "/example.com/assets/logo.png"} {
		if cc := get(p, "").Header.Get("Cache-Control"); cc != revalidateCache {
			t.Errorf("%s: Cache-Control %q, want %q", p, cc, revalidateCache)
		}
	}
	// A stale hashed URL falls back to index.html, which must not be cached
	// for a year under that URL.
	res = get("/example.com/assets/index-Zz99Yy88.js", "")
	if res.StatusCode != 200 || res.Header.Get("Cache-Control") != revalidateCache {
		t.Fatalf("SPA fallback for a missing hashed file: %d, Cache-Control %q", res.StatusCode, res.Header.Get("Cache-Control"))
	}
	// The HTML itself is compressed too.
	if enc := get("/example.com/", "gzip").Header.Get("Content-Encoding"); enc != "gzip" {
		t.Errorf("index.html: encoding %q", enc)
	}

	// The site's own Cache-Control wins, hashed files included.
	for _, p := range []string{"/example.com/own/", "/example.com/own/assets/app-Ab12Cd34.js"} {
		if cc := get(p, "").Header.Get("Cache-Control"); cc != "max-age=60" {
			t.Errorf("%s: Cache-Control %q, want the site's own", p, cc)
		}
	}
	// "compress": false: nothing compressed on the fly.
	res = get("/example.com/raw/", "gzip, zstd")
	if enc := res.Header.Get("Content-Encoding"); enc != "" {
		t.Errorf("compress:false: encoding %q", enc)
	}
	if !bytes.Equal([]byte(body(res)), []byte(page)) {
		t.Error("compress:false: body differs")
	}
}
