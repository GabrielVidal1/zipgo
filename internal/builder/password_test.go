package builder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"
	// The handlers zipgo's configs use, as main.go registers them.
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/caddyauth"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/fileserver"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/headers"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/rewrite"
	_ "github.com/caddyserver/caddy/v2/modules/caddytls"

	"zipgo/internal/gate"
	"zipgo/internal/sites"
)

func passwordConfig() sites.Config {
	return sites.Config{Password: &sites.Password{Hash: bcryptHash, Title: "Ballance"}}
}

// handlerNames lists the handler of each route of a site handler's outer
// subroute, in order.
func handlerNames(h obj) []string {
	var names []string
	routes, _ := h["routes"].(arr)
	for _, r := range routes {
		hs := r.(obj)["handle"].(arr)
		names = append(names, hs[0].(obj)["handler"].(string))
	}
	return names
}

// The login page runs before anything a site serves, and before basic auth when
// a site (oddly) has both.
func TestSiteHandlerPassword(t *testing.T) {
	both := passwordConfig()
	both.BasicAuth = map[string]string{"claude": bcryptHash}
	tests := []struct {
		name string
		site sites.Site
		want string
	}{
		{"static", sites.Site{Path: ".", Config: passwordConfig()}, "zipgo_password,subroute"},
		{"spa", sites.Site{Path: ".", IsSPA: true, Config: passwordConfig()}, "zipgo_password,subroute"},
		{"proxy", sites.Site{Path: ".", Config: sites.Config{Rewrite: "localhost:8080", Password: passwordConfig().Password}}, "zipgo_password,subroute"},
		{"redirect", sites.Site{Path: ".", Config: sites.Config{Redirect: "https://elsewhere.example", Password: passwordConfig().Password}}, "zipgo_password,subroute"},
		{"password and basicAuth", sites.Site{Path: ".", Config: both}, "zipgo_password,authentication,subroute"},
		{"basicAuth only", sites.Site{Path: ".", Config: protectedConfig()}, "authentication,subroute"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, err := siteHandler(tc.site, "ballance.game.gabvdl.xyz", "")
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(handlerNames(h), ","); got != tc.want {
				t.Fatalf("handlers = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestPasswordHandler(t *testing.T) {
	if h := passwordHandler(sites.Config{}, "x.example.com", ""); h != nil {
		t.Fatalf("public site: %v", h)
	}
	tests := []struct {
		name   string
		cfg    sites.Password
		prefix string
		want   obj
	}{
		{"domain mode, defaults", sites.Password{Hash: bcryptHash}, "",
			obj{"handler": "zipgo_password", "host": "x.example.com", "hash": bcryptHash, "session_days": sites.DefaultSessionDays}},
		{"title and days", sites.Password{Hash: bcryptHash, Title: " Ballance ", SessionDays: 30}, "",
			obj{"handler": "zipgo_password", "host": "x.example.com", "hash": bcryptHash, "session_days": 30, "title": "Ballance"}},
		{"days clamped", sites.Password{Hash: bcryptHash, SessionDays: 9999}, "",
			obj{"handler": "zipgo_password", "host": "x.example.com", "hash": bcryptHash, "session_days": sites.MaxSessionDays}},
		{"localhost mode", sites.Password{Hash: bcryptHash}, "/example.com/x",
			obj{"handler": "zipgo_password", "host": "x.example.com", "hash": bcryptHash, "session_days": sites.DefaultSessionDays,
				"prefix": "/example.com/x", "insecure": true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.cfg
			got := passwordHandler(sites.Config{Password: &p}, "x.example.com", tc.prefix)
			gj, _ := json.Marshal(got)
			wj, _ := json.Marshal(tc.want)
			if string(gj) != string(wj) {
				t.Fatalf("got  %s\nwant %s", gj, wj)
			}
		})
	}
}

func TestGuardMetaRoutePassword(t *testing.T) {
	route := guard(obj{"handle": arr{obj{"handler": "static_response"}}},
		sites.Site{Config: passwordConfig()}, "game.example.com", "")
	h := route["handle"].(arr)
	if len(h) != 2 || h[0].(obj)["handler"] != gate.HandlerName {
		t.Fatalf("meta route of a password site: %v", h)
	}
}

// site writes a site folder with an index.html (and an optional title) under
// root and returns it.
func site(t *testing.T, root string, labels []string, title string, cfg sites.Config) sites.Site {
	t.Helper()
	dir := root
	for i := len(labels) - 1; i >= 0; i-- {
		dir = filepath.Join(dir, labels[i]+".")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	html := "<html><head><title>" + title + "</title><meta name=\"description\" content=\"" + title + " desc\"></head></html>"
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
	return sites.Site{Labels: labels, Path: dir, Config: cfg}
}

// A locked child stays out of its parent's public sub-domains-meta: its title
// and description are the very things the lock is hiding.
func TestChildMetaSkipsProtected(t *testing.T) {
	root := t.TempDir()
	parent := site(t, root, []string{"game"}, "Games", sites.Config{})
	all := []sites.Site{
		parent,
		site(t, root, []string{"moooo", "game"}, "MOOOO", sites.Config{}),
		site(t, root, []string{"ballance", "game"}, "Ballance", passwordConfig()),
		site(t, root, []string{"staging", "game"}, "Staging", protectedConfig()),
	}
	got := childMeta(parent, all)
	if _, ok := got["moooo."]; !ok || len(got) != 1 {
		t.Fatalf("children = %v, want only moooo.", got)
	}
	body, _ := json.Marshal(got)
	if strings.Contains(string(body), "Ballance") || strings.Contains(string(body), "Staging") {
		t.Fatalf("a protected child leaked: %s", body)
	}
}

// allowHttp can't open a password site to plain HTTP: the session cookie is
// Secure, and the password would cross the wire in clear.
func TestPasswordSiteIsHTTPSOnly(t *testing.T) {
	root := t.TempDir()
	yes := true
	open := site(t, root, []string{"open"}, "Open", sites.Config{AllowHTTP: &yes})
	locked := passwordConfig()
	locked.AllowHTTP = &yes
	ballance := site(t, root, []string{"ballance"}, "Ballance", locked)
	cfg, err := BuildConfig([]DomainSites{{Domain: "example.com", Sites: []sites.Site{open, ballance}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var app struct {
		Servers map[string]struct {
			Routes []struct {
				Match []struct {
					Host []string `json:"host"`
				} `json:"match"`
			} `json:"routes"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(cfg.AppsRaw["http"], &app); err != nil {
		t.Fatal(err)
	}
	var hosts []string
	for _, r := range app.Servers["http_redirect"].Routes {
		for _, m := range r.Match {
			hosts = append(hosts, m.Host...)
		}
	}
	if strings.Join(hosts, ",") != "open.example.com" {
		t.Fatalf(":80 serves %v, want only open.example.com", hosts)
	}
}

func TestSiteHeadersLocked(t *testing.T) {
	if got := siteHeaders(sites.Config{Headers: map[string]string{"A": "1"}}); len(got) != 1 {
		t.Fatalf("public site headers changed: %v", got)
	}
	c := passwordConfig()
	c.Headers = map[string]string{"cache-control": "private, max-age=60", "X-Extra": "1"}
	got := siteHeaders(c)
	want := map[string]string{"cache-control": "private, max-age=60", "X-Extra": "1", "X-Robots-Tag": "noindex, nofollow"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// The generated config must load in real Caddy: the module is registered under
// the name the builder emits, and every field decodes.
func TestPasswordConfigLoadsInCaddy(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir()) // the server key and the local CA land here
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	parent := site(t, root, nil, "Example", sites.Config{})
	child := site(t, root, []string{"ballance"}, "Ballance", passwordConfig())
	cfg, err := BuildLocalhostConfig([]DomainSites{{Domain: "example.com", Sites: []sites.Site{parent, child}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := caddy.Validate(cfg); err != nil {
		t.Fatalf("caddy rejected the config: %v", err)
	}
}
