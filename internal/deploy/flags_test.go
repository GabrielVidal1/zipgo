package deploy

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"zipgo/internal/sites"
)

func compact(t *testing.T, b []byte) string {
	t.Helper()
	var out bytes.Buffer
	if err := json.Compact(&out, b); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b)
	}
	return out.String()
}

func TestSetFlag(t *testing.T) {
	tests := []struct {
		name, raw, flag string
		on              bool
		want            string // compact; "" = unchanged
	}{
		{"bool shorthand stays one", `{"flags": {"coop": false, "daily": true}}`, "coop", true,
			`{"flags":{"coop":true,"daily":true}}`},
		{"object: on set in place", `{"password": {"hash": "h"}, "flags": {"coop": {"description": "Co-op", "on": true, "paths": ["/c"]}}}`, "coop", false,
			`{"password":{"hash":"h"},"flags":{"coop":{"description":"Co-op","on":false,"paths":["/c"]}}}`},
		{"object without on: on comes first", `{"flags": {"coop": {"description": "Co-op"}}}`, "coop", true,
			`{"flags":{"coop":{"on":true,"description":"Co-op"}}}`},
		{"already so", `{"flags": {"coop": {"description": "Co-op"}}}`, "coop", false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, changed, err := SetFlag([]byte(tc.raw), tc.flag, tc.on)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if changed || string(out) != tc.raw {
					t.Fatalf("changed = %v, out = %s", changed, out)
				}
				return
			}
			if !changed {
				t.Fatal("changed = false")
			}
			if got := compact(t, out); got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
			if _, err := sites.ParseConfig(out); err != nil {
				t.Fatalf("the server would reject it: %v", err)
			}
		})
	}
}

func TestSetFlagRefuses(t *testing.T) {
	if _, _, err := SetFlag([]byte(`{"flags": {}}`), "coop", true); err == nil || !strings.Contains(err.Error(), `no flag "coop"`) {
		t.Errorf("unknown flag: %v", err)
	}
	if _, _, err := SetFlag(nil, "coop", true); err == nil {
		t.Error("no config: accepted")
	}
	if _, _, err := SetFlag([]byte(`{"enable": nope}`), "coop", true); err == nil {
		t.Error("malformed config: accepted")
	}
}

func TestDeclareFlag(t *testing.T) {
	on, desc := true, "Co-op levels"
	tests := []struct {
		name, raw string
		spec      FlagSpec
		want      string
	}{
		{"new flag in a new block, off", `{"password": {"hash": "h"}}`, FlagSpec{},
			`{"password":{"hash":"h"},"flags":{"coop":false}}`},
		{"no config at all", ``, FlagSpec{On: &on},
			`{"flags":{"coop":true}}`},
		{"description and paths make it an object", `{"flags": {"daily": true}}`, FlagSpec{Description: &desc, Paths: []string{"docs/61.html/"}},
			`{"flags":{"daily":true,"coop":{"on":false,"description":"Co-op levels","paths":["/docs/61.html"]}}}`},
		{"an update keeps the switch", `{"flags": {"coop": true}}`, FlagSpec{Description: &desc},
			`{"flags":{"coop":{"on":true,"description":"Co-op levels"}}}`},
		{"empty paths clear them", `{"flags": {"coop": {"on": true, "paths": ["/x"]}}}`, FlagSpec{Paths: []string{}},
			`{"flags":{"coop":true}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, changed, err := DeclareFlag([]byte(tc.raw), "coop", tc.spec)
			if err != nil {
				t.Fatal(err)
			}
			if !changed {
				t.Fatal("changed = false")
			}
			if got := compact(t, out); got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
	if _, changed, _ := DeclareFlag([]byte(`{"flags": {"coop": false}}`), "coop", FlagSpec{}); changed {
		t.Error("a no-op declare reported a change")
	}
	if _, _, err := DeclareFlag(nil, "co op", FlagSpec{}); err == nil {
		t.Error("bad name: accepted")
	}
	if _, _, err := DeclareFlag(nil, "coop", FlagSpec{Paths: []string{"/"}}); err == nil {
		t.Error("whole-site path: accepted")
	}
}

func TestRemoveFlag(t *testing.T) {
	out, changed, err := RemoveFlag([]byte(`{"flags": {"coop": false, "daily": true}}`), "coop")
	if err != nil || !changed {
		t.Fatalf("changed = %v, err = %v", changed, err)
	}
	if got := compact(t, out); got != `{"flags":{"daily":true}}` {
		t.Fatalf("got %s", got)
	}
	// The block stays, empty: the site still serves /__zipgo/flags.json.
	out, _, _ = RemoveFlag(out, "daily")
	if got := compact(t, out); got != `{"flags":{}}` {
		t.Fatalf("last flag: got %s", got)
	}
	if _, changed, _ := RemoveFlag(out, "nope"); changed {
		t.Fatal("removing an unknown flag reported a change")
	}
}

// The build declares, the server decides.
func TestMergeFlags(t *testing.T) {
	tests := []struct {
		name, local, remote, want string
	}{
		{"server switch wins over the build's",
			`{"coop": false}`, `{"coop": true}`, `{"coop":true}`},
		{"the build's description and paths, the server's switch",
			`{"coop": {"on": false, "description": "Co-op v2", "paths": ["/c2"]}}`, `{"coop": {"on": true, "description": "Co-op", "paths": ["/c"]}}`,
			`{"coop":{"on":true,"description":"Co-op v2","paths":["/c2"]}}`},
		{"a bare boolean in the build leaves the server's entry alone",
			`{"coop": false}`, `{"coop": {"on": true, "description": "Co-op"}}`, `{"coop":{"on":true,"description":"Co-op"}}`},
		{"a new flag arrives as shipped, server-only flags stay",
			`{"arena": {"description": "Arena"}, "coop": true}`, `{"coop": false, "admin-made": true}`,
			`{"arena":{"description":"Arena"},"coop":false,"admin-made":true}`},
		{"no flags in the build", ``, `{"coop": true}`, `{"coop":true}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MergeFlags(json.RawMessage(tc.local), json.RawMessage(tc.remote))
			if err != nil {
				t.Fatal(err)
			}
			if g := compact(t, got); g != tc.want {
				t.Fatalf("got  %s\nwant %s", g, tc.want)
			}
		})
	}
}

// A deploy lands the build's config with the server's flags merged in, and the
// server's flags alone when the build has none.
func TestKeepServerStateFlags(t *testing.T) {
	tests := []struct {
		name, local, remote, want string
	}{
		{"flags appended when the build has none",
			`{"headers": {"A": "1"}}`, `{"flags": {"coop": true}}`,
			`{"headers":{"A":"1"},"flags":{"coop":true}}`},
		{"merged in place, next to a kept lock",
			`{"flags": {"coop": false, "arena": false}, "headers": {}}`, `{"password": {"hash": "h"}, "flags": {"coop": true}}`,
			`{"flags":{"coop":true,"arena":false},"headers":{},"password":{"hash":"h"}}`},
		{"no flags on the server: the build's as shipped",
			`{"flags": {"coop": false}}`, `{"password": {"hash": "h"}}`,
			`{"flags":{"coop":false},"password":{"hash":"h"}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := KeepServerState([]byte(tc.local), []byte(tc.remote), false)
			if err != nil {
				t.Fatal(err)
			}
			if got := compact(t, out); got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestKeepConfigFlags(t *testing.T) {
	raw := []byte(`{"flags": {"coop": true}}`)
	cfg, _ := sites.ParseConfig(raw)
	if !keepConfig(folder{Config: raw, Own: cfg}) {
		t.Fatal("a config with flags is mirrored: a deploy would reset the switches")
	}
}

func TestFlagSummary(t *testing.T) {
	got := FlagSummary(map[string]sites.Flag{"coop": {}, "arena": {On: true}})
	if got != "arena on, coop off" {
		t.Fatalf("got %q", got)
	}
}

// What Ballance relies on: a flipped switch survives every deploy, locked or
// not, and a build can declare a new flag — which arrives off.
func TestRunKeepsTheFlags(t *testing.T) {
	for _, tc := range []struct {
		name, server string
	}{
		{"public site", `{"flags": {"coop": {"on": true, "description": "old"}}}`},
		{"locked site", `{"password": {"hash": "$2a$12$server"}, "flags": {"coop": {"on": true, "description": "old"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := localDeploy(t)
			site := filepath.Join(base, "example.com", "game.", "ballance.")
			writeFiles(t, site, map[string]string{sites.ConfigFileName: tc.server, "index.html": "old"})
			src := t.TempDir()
			writeFiles(t, src, map[string]string{
				"index.html":         "game",
				sites.ConfigFileName: `{"flags": {"coop": {"on": false, "description": "Co-op levels"}, "arena": {"description": "Arena"}}}`,
			})
			if err := Run(Options{Delete: true, SSH: "u@h:" + base, Jobs: []Job{{Host: "ballance.game.example.com", Src: src}}}); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(site, sites.ConfigFileName))
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := sites.ParseConfig(raw)
			if err != nil {
				t.Fatal(err)
			}
			if !cfg.Flags["coop"].On || cfg.Flags["coop"].Description != "Co-op levels" {
				t.Errorf("coop = %+v: the deploy flipped it back or kept the old description", cfg.Flags["coop"])
			}
			if f, ok := cfg.Flags["arena"]; !ok || f.On {
				t.Errorf("arena = %+v, %v: a new flag arrives off", f, ok)
			}
		})
	}
}
