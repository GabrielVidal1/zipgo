package builder

import (
	"reflect"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"

	"zipgo/internal/sites"
)

func flaggedConfig() sites.Config {
	return sites.Config{Flags: map[string]sites.Flag{
		"daily": {On: true, Paths: []string{"/daily"}},
		"coop":  {Description: "Co-op levels", Paths: []string{"docs/61.html", "/coop/"}},
		"arena": {},
	}}
}

// The flags run after the locks — a locked site's switches and closed paths
// stay behind its login page — and before whatever the site serves.
func TestSiteHandlerFlags(t *testing.T) {
	locked := flaggedConfig()
	locked.Password = passwordConfig().Password
	tests := []struct {
		name string
		cfg  sites.Config
		want string
	}{
		{"public", flaggedConfig(), "zipgo_flags,subroute"},
		{"locked", locked, "zipgo_password,zipgo_flags,subroute"},
		{"empty block still serves the endpoint", sites.Config{Flags: map[string]sites.Flag{}}, "zipgo_flags,subroute"},
		{"proxy", sites.Config{Rewrite: "localhost:8080", Flags: flaggedConfig().Flags}, "zipgo_flags,subroute"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, err := siteHandler(sites.Site{Path: ".", Config: tc.cfg}, "ballance.game.gabvdl.xyz", "")
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(handlerNames(h), ","); got != tc.want {
				t.Fatalf("handlers = %s, want %s", got, tc.want)
			}
		})
	}
	// No block, no handler: the path is the site's own.
	h, _ := siteHandler(sites.Site{Path: ".", Config: sites.Config{}}, "x.example.com", "")
	if h["handler"] != "subroute" || strings.Contains(strings.Join(handlerNames(h), ","), "zipgo_flags") {
		t.Fatalf("no flags: %v", handlerNames(h))
	}
}

func TestFlagsHandler(t *testing.T) {
	h := flagsHandler(sites.Site{Config: flaggedConfig()}, "x.example.com", "/example.com/x")
	want := obj{
		"handler": "zipgo_flags",
		"host":    "x.example.com",
		"flags":   map[string]bool{"daily": true, "coop": false, "arena": false},
		"closed":  []string{"/docs/61.html", "/coop"}, // only the off flags', normalised
		"prefix":  "/example.com/x",
	}
	if !reflect.DeepEqual(h, want) {
		t.Fatalf("got  %#v\nwant %#v", h, want)
	}
	admin := sites.Config{Admin: true, Password: passwordConfig().Password, Flags: flaggedConfig().Flags}
	if h := flagsHandler(sites.Site{Config: admin}, "admin.example.com", ""); h != nil {
		t.Fatalf("a back-office got flags: %v", h)
	}
}

func TestFlagsConfigLoadsInCaddy(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	locked := flaggedConfig()
	locked.Password = passwordConfig().Password
	all := []sites.Site{
		site(t, root, nil, "Example", sites.Config{}),
		site(t, root, []string{"ballance"}, "Ballance", locked),
		site(t, root, []string{"spore"}, "Spore", flaggedConfig()),
	}
	for name, build := range map[string]func([]DomainSites, string, string) (*caddy.Config, error){
		"localhost": BuildLocalhostConfig, "domain": BuildConfig,
	} {
		cfg, err := build([]DomainSites{{Domain: "example.com", Sites: all}}, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if err := caddy.Validate(cfg); err != nil {
			t.Fatalf("%s: caddy rejected the config: %v", name, err)
		}
	}
}
