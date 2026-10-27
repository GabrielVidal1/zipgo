package admin

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"zipgo/internal/sites"
)

// allTree is tree's domains folder with a second domain, a folder that isn't
// a domain (no dot) and a hidden one, and example.com's back-office made the
// back-office of every domain.
func allTree(t *testing.T) (domains string) {
	t.Helper()
	domains, root := tree(t)
	for name, body := range map[string]string{
		"other.org/www./index.html":                  "www",
		"other.org/www./assets/a.js":                 "1",
		"notadomain/x./index.html":                   "x",
		".hidden.org/index.html":                     "h",
		"example.com/admin./" + sites.ConfigFileName: `{"admin": "all", "password": {"hash": "x"}}`,
	} {
		p := filepath.Join(domains, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = root
	return domains
}

type allPayload struct {
	Scope string   `json:"scope"`
	Roots []string `json:"roots"`
	Sites []struct {
		ID     string  `json:"id"`
		Parent *string `json:"parent"`
		Host   string  `json:"host"`
		Off    bool    `json:"off"`
		Self   bool    `json:"self"`
		URL    string  `json:"url"`
	} `json:"sites"`
}

func TestHandlerAll(t *testing.T) {
	domains := allTree(t)
	self := filepath.Join(domains, "example.com", "admin.")
	a := &Admin{Scope: ScopeAll, DomainsDir: domains, Self: self, TmpDir: domains}
	jsonHdr := map[string]string{"Content-Type": "application/json", "Origin": "https://admin.example.com", "Sec-Fetch-Site": "same-origin"}
	do := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://admin.example.com"+path, strings.NewReader(body))
		if method == "POST" {
			for k, v := range jsonHdr {
				r.Header.Set(k, v)
			}
		}
		w := httptest.NewRecorder()
		if err := a.ServeHTTP(w, r, nil); err != nil {
			t.Fatal(err)
		}
		return w
	}
	sitesOf := func(w *httptest.ResponseRecorder) allPayload {
		t.Helper()
		var p allPayload
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || w.Code != 200 {
			t.Fatalf("sites: %d %s", w.Code, w.Body)
		}
		return p
	}

	p := sitesOf(do("GET", "/api/sites", ""))
	if p.Scope != "all" || strings.Join(p.Roots, ",") != "example.com,other.org" {
		t.Fatalf("scope %q, roots %v: a domain is a folder with a dot, not hidden", p.Scope, p.Roots)
	}
	got := map[string]int{}
	for i, s := range p.Sites {
		got[s.ID] = i
	}
	for _, id := range []string{"example.com", "example.com/app./pb.", "other.org", "other.org/www."} {
		if _, ok := got[id]; !ok {
			t.Errorf("no row %q in %v", id, got)
		}
	}
	if s := p.Sites[got["example.com"]]; s.Parent != nil || s.Host != "example.com" {
		t.Errorf("a domain is a root: %+v", s)
	}
	if s := p.Sites[got["other.org/www."]]; s.Parent == nil || *s.Parent != "other.org" || s.Host != "www.other.org" || s.URL != "https://www.other.org" {
		t.Errorf("www.other.org = %+v", s)
	}
	if s := p.Sites[got["example.com/app./pb."]]; *s.Parent != "example.com/app." || s.URL != "https://pb.app.example.com" {
		t.Errorf("pb.app.example.com = %+v", s)
	}
	if !p.Sites[got["example.com/admin."]].Self {
		t.Error("the back-office's own row should be marked self")
	}

	// A switch names its domain first; the file it writes is the CLI's.
	p = sitesOf(do("POST", "/api/switch", `{"id": "other.org/www.", "on": false}`))
	if !p.Sites[got["other.org/www."]].Off {
		t.Error("www.other.org should be off")
	}
	if raw, err := os.ReadFile(filepath.Join(domains, "other.org", "www.", sites.ConfigFileName)); err != nil || !strings.Contains(string(raw), `"enable": false`) {
		t.Errorf("config: %s %v", raw, err)
	}
	// A whole domain switches too, and its back-office stays up.
	p = sitesOf(do("POST", "/api/switch", `{"id": "example.com", "on": false}`))
	if s := p.Sites[got["example.com/admin."]]; s.Off {
		t.Error("the back-office must survive its domain being switched off")
	}
	sitesOf(do("POST", "/api/switch", `{"id": "example.com", "on": true}`))

	for _, id := range []string{"example.com/admin.", "", "/other.org", "../x", "notadomain/x.", ".hidden.org",
		"other.org/../example.com", "example.com/../other.org/www.", "nope.org", "other.org/nope."} {
		if w := do("POST", "/api/switch", `{"id": "`+id+`", "on": false}`); w.Code != 400 {
			t.Errorf("switch %q: %d, want 400 (%s)", id, w.Code, w.Body)
		}
	}
	if _, err := os.Stat(filepath.Join(domains, "notadomain", "x.", sites.ConfigFileName)); !os.IsNotExist(err) {
		t.Error("a folder outside the domains must never be written")
	}

	if w := do("POST", "/api/password", `{"id": "other.org/www.", "action": "generate"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"password":"`) {
		t.Errorf("password: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/password", `{"id": "example.com/admin.", "action": "generate"}`); w.Code != 400 {
		t.Errorf("re-password self: %d", w.Code)
	}

	sizesCache.byRoot = nil
	var z struct {
		Sizes map[string]Size `json:"sizes"`
	}
	if w := do("GET", "/api/sizes", ""); json.Unmarshal(w.Body.Bytes(), &z) != nil || z.Sizes["other.org/www."].Files != 3 || z.Sizes["example.com/app."].Files != 2 {
		t.Errorf("sizes = %+v", z.Sizes)
	}
}

// In localhost mode every domain's sites are linked by their /<domain>/… path.
func TestHandlerAllPrefix(t *testing.T) {
	domains := allTree(t)
	a := &Admin{Scope: ScopeAll, DomainsDir: domains, Self: filepath.Join(domains, "example.com", "admin."), TmpDir: domains,
		Prefix: "/example.com/admin"}
	r := httptest.NewRequest("GET", "http://localhost:9000/example.com/admin/api/sites", nil)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r, nil)
	for _, want := range []string{`"url":"/other.org/www/"`, `"url":"/example.com/app/pb/"`, `"url":"/example.com/"`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("localhost links lack %s: %s", want, w.Body)
		}
	}
}
