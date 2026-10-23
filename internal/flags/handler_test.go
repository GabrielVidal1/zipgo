package flags

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// site stands in for whatever the site serves after the flags.
var site = caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("X-Site", "1")
	w.Write([]byte("site:" + r.URL.Path))
	return nil
})

func serve(h *Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil), site)
	return rec
}

// A closed path is closed whatever its case: Caddy's own path matcher is
// case-insensitive, and so is more than one client-side router.
func TestClosedPathsIgnoreCase(t *testing.T) {
	h := &Handler{Host: "ballance.example.com", Flags: map[string]bool{"coop": false}, Closed: []string{"/arena"}}
	for _, p := range []string{"/arena", "/Arena", "/ARENA/level/1", "//arena/", "/x/../arena"} {
		if rec := serve(h, http.MethodGet, p); rec.Code != 404 || rec.Header().Get("X-Site") != "" {
			t.Errorf("%s: status %d, reached the site: %q", p, rec.Code, rec.Header().Get("X-Site"))
		}
	}
	for _, p := range []string{"/arenas", "/arena-2", "/"} {
		if rec := serve(h, http.MethodGet, p); rec.Code != 200 || rec.Header().Get("X-Site") == "" {
			t.Errorf("%s: status %d, should reach the site", p, rec.Code)
		}
	}
}

func TestFlagsEndpoint(t *testing.T) {
	h := &Handler{Host: "ballance.example.com", Flags: map[string]bool{"coop": false, "daily": true}}
	rec := serve(h, http.MethodGet, "/__zipgo/flags.json")
	if rec.Code != 200 || rec.Header().Get("X-Site") != "" {
		t.Fatalf("status %d, reached the site: %q", rec.Code, rec.Header().Get("X-Site"))
	}
	if ct, cc := rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"); ct != "application/json" || cc != "no-store" {
		t.Fatalf("Content-Type %q, Cache-Control %q", ct, cc)
	}
	var got Response
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Flags, h.Flags) {
		t.Fatalf("flags = %v", got.Flags)
	}
	if rec := serve(h, http.MethodPost, "/__zipgo/flags.json"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", rec.Code)
	}
	if rec := serve(h, http.MethodHead, "/__zipgo/flags.json"); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("HEAD: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	// An empty block answers an empty object, never null.
	rec = serve(&Handler{}, http.MethodGet, "/__zipgo/flags.json")
	if body := rec.Body.String(); body != "{\"flags\":{}}\n" {
		t.Fatalf("empty: %q", body)
	}
}

func TestClosedPaths(t *testing.T) {
	h := &Handler{Flags: map[string]bool{"arena": false}, Closed: []string{"/editor/arena", "/docs/61.html"}}
	for target, closed := range map[string]bool{
		"/editor/arena":            true,
		"/editor/arena/":           true,
		"/editor/arena/index.html": true,
		"//editor//arena/x.js":     true,
		"/editor/x/../arena/":      true,
		"/editor/%61rena/":         true, // decoded before matching
		"/docs/61.html":            true,
		"/docs/61.html.br":         false,
		"/editor/arenas/":          false,
		"/editor/":                 false,
		"/":                        false,
		"/editor/arena-hud/x.html": false,
	} {
		rec := serve(h, http.MethodGet, target)
		if got := rec.Code == http.StatusNotFound; got != closed {
			t.Errorf("%s: status %d, want closed=%v", target, rec.Code, closed)
		}
		if closed && rec.Header().Get("X-Site") != "" {
			t.Errorf("%s: reached the site", target)
		}
	}
}

// In localhost mode the site lives under a path prefix: its own paths are
// matched under it, and anything outside it is none of its business.
func TestPrefix(t *testing.T) {
	h := &Handler{Prefix: "/example.com/spore", Flags: map[string]bool{"arena": false}, Closed: []string{"/arena"}}
	if rec := serve(h, http.MethodGet, "/example.com/spore/__zipgo/flags.json"); rec.Code != 200 || rec.Header().Get("X-Site") != "" {
		t.Fatalf("prefixed endpoint: %d", rec.Code)
	}
	if rec := serve(h, http.MethodGet, "/example.com/spore/arena/"); rec.Code != 404 {
		t.Fatalf("prefixed closed path: %d", rec.Code)
	}
	if rec := serve(h, http.MethodGet, "/arena/"); rec.Code != 200 {
		t.Fatalf("outside the prefix: %d", rec.Code)
	}
}
