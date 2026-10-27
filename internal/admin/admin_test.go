package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"zipgo/internal/sites"
)

// tree builds a domains folder holding example.com:
//
//	example.com/            index.html          (static)
//	  admin./               admin + password    (the back-office)
//	  app./                 index.html + assets (spa)
//	    pb./                rewrite             (proxy)
//	  old./                 index.html, off
//	    api./               index.html          (off through old.)
//	  bad./                 malformed config
//	  holder./              nothing but sub.
//	    sub./               index.html
func tree(t *testing.T) (domains, root string) {
	t.Helper()
	domains = t.TempDir()
	root = filepath.Join(domains, "example.com")
	files := map[string]string{
		"index.html":                       "<title>apex</title>",
		"admin./" + sites.ConfigFileName:   `{"admin": true, "password": {"hash": "x"}}`,
		"app./index.html":                  "<title>app</title>",
		"app./assets/app.js":               "console.log(1)",
		"app./pb./" + sites.ConfigFileName: `{"rewrite": "pb:8090"}`,
		"old./index.html":                  "old",
		"old./" + sites.ConfigFileName:     "{\n  \"rewritePath\": \"/x\",\n  \"enable\": false\n}\n",
		"old./api./index.html":             "api",
		"bad./" + sites.ConfigFileName:     `{"enable": nope}`,
		"holder./sub./index.html":          "sub",
	}
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return domains, root
}

func byID(list []Site) map[string]Site {
	m := map[string]Site{}
	for _, s := range list {
		m[s.ID] = s
	}
	return m
}

func TestList(t *testing.T) {
	_, root := tree(t)
	list, err := List(root, "example.com", filepath.Join(root, "admin."))
	if err != nil {
		t.Fatal(err)
	}
	got := byID(list)
	tests := []struct {
		id, host, typ, offBy string
		off, self, cfgErr    bool
		parent               string
	}{
		{id: "", host: "example.com", typ: "static", parent: "-"},
		{id: "admin.", host: "admin.example.com", typ: "admin", self: true},
		{id: "app.", host: "app.example.com", typ: "spa"},
		{id: "app./pb.", host: "pb.app.example.com", typ: "proxy", parent: "app."},
		{id: "old.", host: "old.example.com", typ: "static", off: true},
		{id: "old./api.", host: "api.old.example.com", typ: "static", off: true, offBy: "old.example.com", parent: "old."},
		{id: "bad.", host: "bad.example.com", typ: "folder", cfgErr: true},
		{id: "holder.", host: "holder.example.com", typ: "folder"},
		{id: "holder./sub.", host: "sub.holder.example.com", typ: "static", parent: "holder."},
	}
	if len(list) != len(tests) {
		t.Errorf("listed %d sites, want %d", len(list), len(tests))
	}
	for _, tc := range tests {
		s, ok := got[tc.id]
		if !ok {
			t.Errorf("%q not listed", tc.id)
			continue
		}
		if s.Host != tc.host || s.Type != tc.typ || s.Off != tc.off || s.OffBy != tc.offBy || s.Self != tc.self || (s.ConfigError != "") != tc.cfgErr {
			t.Errorf("%q = %+v", tc.id, s)
		}
		switch {
		case tc.parent == "-":
			if s.Parent != nil {
				t.Errorf("root has parent %q", *s.Parent)
			}
		case s.Parent == nil || *s.Parent != tc.parent:
			t.Errorf("%q parent = %v, want %q", tc.id, s.Parent, tc.parent)
		}
	}
	if !got["admin."].Locked || !got["admin."].Password {
		t.Error("the back-office is locked by its password")
	}
	if list[0].ID != "" {
		t.Error("the root comes first")
	}
}

func TestListAdminSurvivesItsParent(t *testing.T) {
	_, root := tree(t)
	if err := os.WriteFile(filepath.Join(root, sites.ConfigFileName), []byte(`{"enable": false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := byID(mustList(t, root))
	if got["admin."].Off {
		t.Error("the back-office stays on when the domain is off")
	}
	if !got["app."].Off || got["app."].OffBy != "example.com" {
		t.Errorf("app. = %+v, want off through example.com", got["app."])
	}
}

func mustList(t *testing.T, root string) []Site {
	t.Helper()
	list, err := List(root, "example.com", filepath.Join(root, "admin."))
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestSwitch(t *testing.T) {
	domains, root := tree(t)
	self := filepath.Join(root, "admin.")
	read := func(id string) string {
		b, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(id), sites.ConfigFileName))
		return string(b)
	}

	// Off writes "enable": false where there was no config at all…
	if changed, err := Switch(root, domains, self, "app.", false); err != nil || !changed {
		t.Fatalf("off: changed=%v err=%v", changed, err)
	}
	if got := read("app."); !strings.Contains(got, `"enable": false`) {
		t.Errorf("app. config = %q", got)
	}
	if !byID(mustList(t, root))["app./pb."].Off {
		t.Error("pb. goes off with app.")
	}
	// …and on deletes the file again, since nothing else was in it.
	if changed, err := Switch(root, domains, self, "app.", true); err != nil || !changed {
		t.Fatalf("on: changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(root, "app.", sites.ConfigFileName)); !os.IsNotExist(err) {
		t.Error("switching on an otherwise empty config removes the file")
	}
	// On keeps every other key, in place.
	if _, err := Switch(root, domains, self, "old.", true); err != nil {
		t.Fatal(err)
	}
	if got := read("old."); got != "{\n  \"rewritePath\": \"/x\"\n}\n" {
		t.Errorf("old. config = %q", got)
	}
	// Asking for the current state changes nothing.
	if changed, err := Switch(root, domains, self, "old.", true); err != nil || changed {
		t.Errorf("no-op: changed=%v err=%v", changed, err)
	}
	// No temporary file is left in the domains folder.
	entries, _ := os.ReadDir(domains)
	if len(entries) != 1 {
		t.Errorf("domains folder holds %d entries, want just example.com", len(entries))
	}
}

func TestSwitchRefuses(t *testing.T) {
	domains, root := tree(t)
	self := filepath.Join(root, "admin.")
	for _, id := range []string{"admin.", "bad.", "../example.com", "app./..", "app", "/app.", "app.//pb.", ".git.", "nope.", "app./assets"} {
		if _, err := Switch(root, domains, self, id, false); err == nil {
			t.Errorf("Switch(%q) should fail", id)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(root, "bad.", sites.ConfigFileName)); string(got) != `{"enable": nope}` {
		t.Error("a malformed config is left alone")
	}
}

func TestSetLock(t *testing.T) {
	domains, root := tree(t)
	self := filepath.Join(root, "admin.")
	cfgOf := func(id string) sites.Config {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(id), sites.ConfigFileName))
		if os.IsNotExist(err) {
			return sites.Config{}
		}
		c, err := sites.ParseConfig(b)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	pw, err := SetLock(root, domains, self, "app.", "generate", "")
	if err != nil || pw == "" {
		t.Fatalf("generate: %q %v", pw, err)
	}
	c := cfgOf("app.")
	if c.Password == nil || bcrypt.CompareHashAndPassword([]byte(c.Password.Hash), []byte(pw)) != nil {
		t.Fatalf("app. config after generate = %+v", c)
	}

	// A change keeps the block's title and session length.
	raw := `{"password": {"hash": "` + c.Password.Hash + `", "title": "App", "sessionDays": 30}}`
	os.WriteFile(filepath.Join(root, "app.", sites.ConfigFileName), []byte(raw), 0o644)
	if _, err := SetLock(root, domains, self, "app.", "set", "correct horse"); err != nil {
		t.Fatal(err)
	}
	c = cfgOf("app.")
	if c.Password.Title != "App" || c.Password.SessionDays != 30 ||
		bcrypt.CompareHashAndPassword([]byte(c.Password.Hash), []byte("correct horse")) != nil {
		t.Errorf("app. password after set = %+v", c.Password)
	}

	// Remove drops the block, and the file with it when nothing else is left.
	if _, err := SetLock(root, domains, self, "app.", "remove", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "app.", sites.ConfigFileName)); !os.IsNotExist(err) {
		t.Error("removing the only key removes the file")
	}
	// …and keeps the other keys otherwise.
	if _, err := SetLock(root, domains, self, "old.", "set", "12345678"); err != nil {
		t.Fatal(err)
	}
	if _, err := SetLock(root, domains, self, "old.", "remove", ""); err != nil {
		t.Fatal(err)
	}
	if c := cfgOf("old."); c.Password != nil || c.Enabled() || c.RewritePath != "/x" {
		t.Errorf("old. after remove = %+v", c)
	}

	for _, tc := range []struct{ id, action, pw string }{
		{"app.", "set", "short"},
		{"app.", "frobnicate", ""},
		{"admin.", "generate", ""},
		{"bad.", "generate", ""},
		{"../x", "generate", ""},
	} {
		if _, err := SetLock(root, domains, self, tc.id, tc.action, tc.pw); err == nil {
			t.Errorf("SetLock(%q, %q) should fail", tc.id, tc.action)
		}
	}
}

func TestSizes(t *testing.T) {
	_, root := tree(t)
	sizesCache.byRoot = nil // tests share the package cache
	got, err := Sizes(root)
	if err != nil {
		t.Fatal(err)
	}
	// A site counts its own files only: app.'s assets yes, pb.'s config no.
	if got["app."].Files != 2 || got["app./pb."].Files != 1 || got[""].Files != 1 {
		t.Errorf("sizes = %+v", got)
	}
	if got["app."].Bytes != int64(len("<title>app</title>")+len("console.log(1)")) {
		t.Errorf("app. bytes = %d", got["app."].Bytes)
	}
}

func TestHandler(t *testing.T) {
	domains, root := tree(t)
	a := &Admin{Root: root, RootHost: "example.com", Self: filepath.Join(root, "admin."), TmpDir: domains}
	do := func(method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://admin.example.com"+path, strings.NewReader(body))
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		if err := a.ServeHTTP(w, r, nil); err != nil {
			t.Fatal(err)
		}
		return w
	}
	jsonHdr := map[string]string{"Content-Type": "application/json", "Origin": "https://admin.example.com", "Sec-Fetch-Site": "same-origin"}

	w := do("GET", "/", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "app.js") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Errorf("page: %d %q", w.Code, w.Header())
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("page headers = %v", w.Header())
	}
	if w := do("GET", "/app.js", "", nil); w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") {
		t.Errorf("app.js: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if w := do("GET", "/nope", "", nil); w.Code != 404 {
		t.Errorf("unknown path: %d", w.Code)
	}

	w = do("GET", "/api/sites", "", nil)
	var payload struct {
		Host  string `json:"host"`
		Sites []struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"sites"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || payload.Host != "example.com" {
		t.Fatalf("sites: %d %s", w.Code, w.Body)
	}
	urls := map[string]string{}
	for _, s := range payload.Sites {
		urls[s.ID] = s.URL
	}
	if urls["app./pb."] != "https://pb.app.example.com" || urls["admin."] != "" || urls["holder."] != "" {
		t.Errorf("urls = %v", urls)
	}

	// State changes need a same-origin JSON POST.
	for name, tc := range map[string]struct {
		method string
		hdr    map[string]string
		code   int
	}{
		"GET":            {"GET", jsonHdr, 405},
		"form post":      {"POST", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, 415},
		"cross-site":     {"POST", map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}, 403},
		"foreign origin": {"POST", map[string]string{"Content-Type": "application/json", "Origin": "https://evil.example"}, 403},
		"switch self":    {"POST", jsonHdr, 400},
	} {
		body := `{"id": "app.", "on": false}`
		if name == "switch self" {
			body = `{"id": "admin.", "on": false}`
		}
		if w := do(tc.method, "/api/switch", body, tc.hdr); w.Code != tc.code {
			t.Errorf("%s: %d, want %d (%s)", name, w.Code, tc.code, w.Body)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "app.", sites.ConfigFileName)); !os.IsNotExist(err) {
		t.Fatal("a refused request must not switch anything")
	}
	if w := do("POST", "/api/switch", `{"id": "app.", "on": false}`, jsonHdr); w.Code != 200 || !strings.Contains(w.Body.String(), `"offBy":"app.example.com"`) {
		t.Errorf("switch: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/password", `{"id": "app.", "action": "generate"}`, jsonHdr); w.Code != 200 || !strings.Contains(w.Body.String(), `"password":"`) {
		t.Errorf("password: %d %s", w.Code, w.Body)
	}
}

// In localhost mode the page sits under a path prefix: the bare prefix
// redirects to the slash form (the page's URLs are relative), and links are
// paths.
func TestHandlerPrefix(t *testing.T) {
	domains, root := tree(t)
	a := &Admin{Root: root, RootHost: "example.com", Self: filepath.Join(root, "admin."), TmpDir: domains,
		Prefix: "/example.com/admin", RootPath: "/example.com"}
	r := httptest.NewRequest("GET", "http://localhost:9000/example.com/admin", nil)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r, nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/example.com/admin/" {
		t.Errorf("bare prefix: %d %s", w.Code, w.Header().Get("Location"))
	}
	r = httptest.NewRequest("GET", "http://localhost:9000/example.com/admin/api/sites", nil)
	w = httptest.NewRecorder()
	a.ServeHTTP(w, r, nil)
	if !strings.Contains(w.Body.String(), `"url":"/example.com/app/pb/"`) {
		t.Errorf("localhost links: %s", w.Body)
	}
}
