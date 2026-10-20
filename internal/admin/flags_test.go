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

func withFlags(t *testing.T, root string) string {
	t.Helper()
	file := filepath.Join(root, "app.", sites.ConfigFileName)
	body := "{\n  \"headers\": {\"X-A\": \"1\"},\n  \"flags\": {\"coop\": {\"description\": \"Co-op\", \"paths\": [\"/coop\"]}, \"daily\": true}\n}\n"
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestListFlags(t *testing.T) {
	_, root := tree(t)
	withFlags(t, root)
	list, err := List(root, "example.com", filepath.Join(root, "admin."))
	if err != nil {
		t.Fatal(err)
	}
	m := byID(list)
	got := m["app."].Flags
	if len(got) != 2 || got[0].Name != "coop" || got[0].On || got[0].Description != "Co-op" || got[0].Paths[0] != "/coop" || !got[1].On {
		t.Fatalf("flags = %+v", got)
	}
	if m[""].Flags != nil {
		t.Fatalf("a site without a block lists flags: %+v", m[""].Flags)
	}
}

func TestSetFlag(t *testing.T) {
	domains, root := tree(t)
	file := withFlags(t, root)
	self := filepath.Join(root, "admin.")
	changed, err := SetFlag(root, domains, self, "app.", "coop", true)
	if err != nil || !changed {
		t.Fatalf("changed = %v, err = %v", changed, err)
	}
	raw, _ := os.ReadFile(file)
	cfg, err := sites.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Flags["coop"].On || cfg.Flags["coop"].Description != "Co-op" || cfg.Headers["X-A"] != "1" {
		t.Fatalf("config: %s", raw)
	}
	if !strings.HasPrefix(string(raw), "{\n  \"headers\"") {
		t.Fatalf("key order lost: %s", raw)
	}
	if changed, _ := SetFlag(root, domains, self, "app.", "coop", true); changed {
		t.Fatal("a no-op reported a change")
	}
	if _, err := SetFlag(root, domains, self, "app.", "nope", true); err == nil {
		t.Fatal("unknown flag: accepted")
	}
	if _, err := SetFlag(root, domains, self, "admin.", "coop", true); err == nil {
		t.Fatal("the back-office: accepted")
	}
	if _, err := SetFlag(root, domains, self, "../x.", "coop", true); err == nil {
		t.Fatal("a path outside the tree: accepted")
	}
}

func TestHandlerFlag(t *testing.T) {
	domains, root := tree(t)
	withFlags(t, root)
	a := &Admin{Root: root, RootHost: "example.com", Self: filepath.Join(root, "admin."), TmpDir: domains}
	post := func(body string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://admin.example.com/api/flag", strings.NewReader(body))
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
	w := post(`{"id": "app.", "name": "coop", "on": true}`, jsonHdr)
	if w.Code != 200 {
		t.Fatalf("flip: %d %s", w.Code, w.Body)
	}
	var payload struct {
		Sites []Site `json:"sites"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if f := byID(payload.Sites)["app."].Flags; len(f) != 2 || !f[0].On {
		t.Fatalf("the reply doesn't show the flip: %+v", f)
	}
	if w := post(`{"id": "app.", "name": "coop", "on": false}`, map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}); w.Code != 403 {
		t.Fatalf("cross-site: %d", w.Code)
	}
	if w := post(`{"id": "app.", "name": "nope", "on": true}`, jsonHdr); w.Code != 400 {
		t.Fatalf("unknown flag: %d", w.Code)
	}
}
