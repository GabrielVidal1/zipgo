package builder

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"zipgo/internal/admin"
	"zipgo/internal/sites"
)

func adminJSON(withPassword bool) string {
	if !withPassword {
		return `{"admin": true}`
	}
	return `{"admin": true, "password": {"hash": "` + bcryptHash + `"}}`
}

func siteByHost(t *testing.T, ds DomainSites, host string) sites.Site {
	t.Helper()
	for _, s := range ds.Sites {
		if s.Host(ds.Domain) == host {
			return s
		}
	}
	t.Fatalf("no site %s", host)
	return sites.Site{}
}

// The back-office only ever runs behind its login page, and manages the folder
// it is nested in.
func TestAdminSiteBehindItsGate(t *testing.T) {
	ds := discovered(t, "example.com", []string{".", "app."},
		map[string]string{"admin.": adminJSON(true)})
	s := siteByHost(t, ds, "admin.example.com")
	h, err := siteHandler(s, "admin.example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(handlerNames(h), ","); got != "zipgo_password,"+admin.HandlerName {
		t.Fatalf("handlers = %s, want the gate then the back-office", got)
	}
	ah := h["routes"].(arr)[1].(obj)["handle"].(arr)[0].(obj)
	apex := filepath.Dir(s.Path)
	if ah["root"] != apex || ah["root_host"] != "example.com" || ah["self"] != s.Path || ah["tmp_dir"] != filepath.Dir(apex) {
		t.Errorf("admin handler = %v", ah)
	}
	if _, ok := ah["prefix"]; ok {
		t.Error("no path prefix in domain mode")
	}
}

// Without a password there is no gate to put in front, so the page is refused
// outright: nothing about the managed sites is reachable.
func TestAdminSiteWithoutPasswordIsClosed(t *testing.T) {
	ds := discovered(t, "example.com", []string{"."},
		map[string]string{"admin.": adminJSON(false)})
	cfg, err := BuildConfig([]DomainSites{ds}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	app := configJSON(t, cfg.AppsRaw["http"])
	https := app["servers"].(map[string]any)["https"].(map[string]any)
	if got := statusByHost(https["routes"].([]any))["admin.example.com"]; got != "403" {
		t.Errorf("admin.example.com status = %q, want 403", got)
	}
	if b, _ := json.Marshal(app); strings.Contains(string(b), admin.HandlerName) {
		t.Error("the back-office handler must not be emitted without a password")
	}
}

// Switching the whole domain off leaves its back-office up — it is how the
// domain gets switched back on — and the back-office never shows in the
// public sub-domains-meta.
func TestAdminSiteSurvivesTheCascade(t *testing.T) {
	ds := discovered(t, "example.com", []string{".", "app."},
		map[string]string{".": `{"enable": false}`, "admin.": adminJSON(true)})
	if s := siteByHost(t, ds, "admin.example.com"); s.Off() {
		t.Fatal("the back-office must not be switched off by its parent")
	}
	if s := siteByHost(t, ds, "app.example.com"); !s.Off() {
		t.Fatal("app.example.com should be off through the apex")
	}
	cfg, err := BuildConfig([]DomainSites{ds}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	app := configJSON(t, cfg.AppsRaw["http"])
	got := statusByHost(app["servers"].(map[string]any)["https"].(map[string]any)["routes"].([]any))
	if got["admin.example.com"] != "" || got["app.example.com"] != "410" {
		t.Errorf("routes by host = %v", got)
	}

	on := discovered(t, "example.com", []string{".", "app."},
		map[string]string{"admin.": adminJSON(true)})
	if _, ok := childMeta(siteByHost(t, on, "example.com"), on.Sites)["admin."]; ok {
		t.Error("the back-office must not be listed in sub-domains-meta")
	}
}

// In localhost mode the back-office lives under its path, and the apex's www
// redirect no longer swallows the subdomain paths.
func TestAdminSiteLocalhost(t *testing.T) {
	ds := discovered(t, "example.com", []string{".", "www.", "app."},
		map[string]string{"admin.": adminJSON(true)})
	cfg, err := BuildLocalhostConfig([]DomainSites{ds}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	app := configJSON(t, cfg.AppsRaw["http"])
	routes := app["servers"].(map[string]any)["sites"].(map[string]any)["routes"].([]any)
	order := map[string]int{}
	var adminRoute map[string]any
	for i, r := range routes {
		ro := r.(map[string]any)
		paths := ro["match"].([]any)[0].(map[string]any)["path"].([]any)
		p := paths[0].(string)
		if _, seen := order[p]; !seen {
			order[p] = i
		}
		if p == "/example.com/admin" {
			adminRoute = ro
		}
	}
	for _, sub := range []string{"/example.com/admin", "/example.com/app", "/example.com/www"} {
		if order[sub] > order["/example.com"] {
			t.Errorf("%s (route %d) must come before the apex's www redirect (route %d)", sub, order[sub], order["/example.com"])
		}
	}
	b, _ := json.Marshal(adminRoute)
	for _, want := range []string{`"prefix":"/example.com/admin"`, `"root_path":"/example.com"`, `"insecure":true`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("admin route lacks %s: %s", want, b)
		}
	}
}

// "admin": "all" manages every domain: the handler gets the domains folder
// instead of one root, and keeps the gate in front.
func TestAdminSiteAllDomains(t *testing.T) {
	ds := discovered(t, "example.com", []string{".", "app."},
		map[string]string{"admin.": `{"admin": "all", "password": {"hash": "` + bcryptHash + `"}}`})
	s := siteByHost(t, ds, "admin.example.com")
	h, err := siteHandler(s, "admin.example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(handlerNames(h), ","); got != "zipgo_password,"+admin.HandlerName {
		t.Fatalf("handlers = %s, want the gate then the back-office", got)
	}
	ah := h["routes"].(arr)[1].(obj)["handle"].(arr)[0].(obj)
	domains := filepath.Dir(filepath.Dir(s.Path))
	if ah["scope"] != admin.ScopeAll || ah["domains_dir"] != domains || ah["tmp_dir"] != domains || ah["self"] != s.Path {
		t.Errorf("admin handler = %v", ah)
	}
	if _, ok := ah["root"]; ok {
		t.Error("an all-domains back-office has no single root")
	}

	// In localhost mode it keeps its own prefix; each domain is linked by
	// its own path.
	h, err = siteHandler(s, "admin.example.com", "/example.com/admin")
	if err != nil {
		t.Fatal(err)
	}
	ah = h["routes"].(arr)[1].(obj)["handle"].(arr)[0].(obj)
	if ah["prefix"] != "/example.com/admin" {
		t.Errorf("localhost admin handler = %v", ah)
	}
	if _, ok := ah["root_path"]; ok {
		t.Error("no root_path for an all-domains back-office")
	}
}
