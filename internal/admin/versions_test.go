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

// historyTree adds deploy history to tree's app.: the live deploy (v3) on top
// of two kept ones, plus the server state a rollback must leave alone — a
// config that switches a flag off, and a nested pb. site.
func historyTree(t *testing.T) (domains, root string) {
	t.Helper()
	domains, root = tree(t)
	v := "app./" + sites.VersionsDirName + "/"
	files := map[string]string{
		"app./index.html":                    "<title>v3</title>",
		"app./assets/v3.js":                  "v3",
		"app./" + sites.ConfigFileName:       `{"flags": {"beta": false}}`,
		v + "20261001T080000Z/index.html":    "<title>v1</title>",
		v + "20261020T120000Z/index.html":    "<title>v2</title>",
		v + "20261020T120000Z/assets/app.js": "console.log(2)",
		v + "20261020T120000Z/old/only.txt":  "gone in v3",
		// A snapshot taken before configs were left out of them: its config
		// must not come back.
		v + "20261020T120000Z/" + sites.ConfigFileName: `{"enable": false}`,
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

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestVersions(t *testing.T) {
	_, root := historyTree(t)
	got, err := Versions(root, "app.")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "20261020T120000Z" || got[1].Name != "20261001T080000Z" {
		t.Fatalf("versions = %+v, want newest first", got)
	}
	if got[0].At.Format(stampLayout) != "20261020T120000Z" || got[0].Files != 4 {
		t.Errorf("newest = %+v", got[0])
	}
	if none, err := Versions(root, "old."); err != nil || len(none) != 0 {
		t.Errorf("a site without history: %v, %v", none, err)
	}
	if _, err := Versions(root, "../x."); err == nil {
		t.Error("a bad id must be refused")
	}
}

func TestRollback(t *testing.T) {
	domains, root := historyTree(t)
	app := filepath.Join(root, "app.")
	config := read(t, filepath.Join(app, sites.ConfigFileName))

	backup, err := Rollback(root, domains, "", "app.", "20261020T120000Z")
	if err != nil {
		t.Fatal(err)
	}
	// v2 is live, v3's own file is gone, v2's are back.
	if got := read(t, filepath.Join(app, "index.html")); got != "<title>v2</title>" {
		t.Errorf("index.html = %q", got)
	}
	if read(t, filepath.Join(app, "old/only.txt")) != "gone in v3" || read(t, filepath.Join(app, "assets/app.js")) != "console.log(2)" {
		t.Error("v2's files are not all back")
	}
	if _, err := os.Stat(filepath.Join(app, "assets/v3.js")); !os.IsNotExist(err) {
		t.Error("v3's own file should be gone")
	}
	// Server state untouched: the live config, the nested site.
	if got := read(t, filepath.Join(app, sites.ConfigFileName)); got != config {
		t.Errorf("config = %q, want the live one kept", got)
	}
	if _, err := os.Stat(filepath.Join(app, "pb.", sites.ConfigFileName)); err != nil {
		t.Error("the nested pb. site must survive a rollback")
	}
	// What was live (v3) is kept — content only — so the rollback undoes.
	kept := filepath.Join(app, sites.VersionsDirName, backup)
	if backup == "" || read(t, filepath.Join(kept, "index.html")) != "<title>v3</title>" || read(t, filepath.Join(kept, "assets/v3.js")) != "v3" {
		t.Fatalf("backup %q does not hold v3", backup)
	}
	for _, p := range []string{sites.ConfigFileName, "pb.", sites.VersionsDirName} {
		if _, err := os.Stat(filepath.Join(kept, p)); !os.IsNotExist(err) {
			t.Errorf("backup must not hold %s", p)
		}
	}
	if vs, _ := Versions(root, "app."); len(vs) != 3 || vs[0].Name != backup {
		t.Errorf("versions after = %+v", vs)
	}

	// And back again.
	if _, err := Rollback(root, domains, "", "app.", backup); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(app, "index.html")) != "<title>v3</title>" {
		t.Error("rolling back to the backup should restore v3")
	}
	if _, err := os.Stat(filepath.Join(app, "old")); !os.IsNotExist(err) {
		t.Error("v2's folder should be gone again")
	}
	if entries, _ := os.ReadDir(domains); len(entries) != 1 {
		t.Errorf("temp files left in the domains folder: %v", entries)
	}
}

func TestRollbackRefuses(t *testing.T) {
	domains, root := historyTree(t)
	for _, v := range []string{"", "../../x", ".hidden", "nope", "a/b"} {
		if _, err := Rollback(root, domains, "", "app.", v); err == nil {
			t.Errorf("version %q should be refused", v)
		}
	}
	self := filepath.Join(root, "admin.")
	if _, err := Rollback(root, domains, self, "admin.", "x"); err == nil {
		t.Error("the back-office must not roll itself back")
	}
	if read(t, filepath.Join(root, "app./index.html")) != "<title>v3</title>" {
		t.Error("a refused rollback must change nothing")
	}
}

func TestSizesSkipHistory(t *testing.T) {
	_, root := historyTree(t)
	sizesCache.byRoot = nil
	got, err := Sizes(root)
	if err != nil {
		t.Fatal(err)
	}
	// index.html, assets/app.js, assets/v3.js, the config: the kept deploys
	// don't count.
	if got["app."].Files != 4 {
		t.Errorf("app. files = %d, want 4", got["app."].Files)
	}
}

func TestHandlerVersions(t *testing.T) {
	domains, root := historyTree(t)
	a := &Admin{Root: root, RootHost: "example.com", Self: filepath.Join(root, "admin."), TmpDir: domains}
	do := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://admin.example.com"+path, strings.NewReader(body))
		if method == "POST" {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Sec-Fetch-Site", "same-origin")
		}
		w := httptest.NewRecorder()
		if err := a.ServeHTTP(w, r, nil); err != nil {
			t.Fatal(err)
		}
		return w
	}
	w := do("GET", "/api/versions?id=app.", "")
	var got struct {
		Versions []Version `json:"versions"`
		Backup   string    `json:"backup"`
		Sites    []Site    `json:"sites"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 || len(got.Versions) != 2 {
		t.Fatalf("versions: %d %s", w.Code, w.Body)
	}
	if w := do("GET", "/api/versions?id=nope.", ""); w.Code != 400 {
		t.Errorf("unknown site: %d", w.Code)
	}
	if w := do("GET", "/api/rollback", ""); w.Code != 405 {
		t.Errorf("GET rollback: %d", w.Code)
	}
	w = do("POST", "/api/rollback", `{"id": "app.", "version": "20261001T080000Z"}`)
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
		t.Fatalf("rollback: %d %s", w.Code, w.Body)
	}
	if got.Backup == "" || len(got.Versions) != 3 || len(got.Sites) == 0 {
		t.Errorf("rollback answer = %+v", got)
	}
	if read(t, filepath.Join(root, "app./index.html")) != "<title>v1</title>" {
		t.Error("app. should serve v1")
	}
}
