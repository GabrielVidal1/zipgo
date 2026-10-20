package sites

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseConfigFlags(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{"flags": {
		"daily": true,
		"coop": {"description": "Co-op levels", "paths": ["/docs/61.html"]},
		"arena": {"on": true, "paths": ["editor/arena/"]}
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Flag{
		"daily": {On: true},
		"coop":  {On: false, Description: "Co-op levels", Paths: []string{"/docs/61.html"}},
		"arena": {On: true, Paths: []string{"editor/arena/"}},
	}
	if !reflect.DeepEqual(cfg.Flags, want) {
		t.Fatalf("flags = %#v", cfg.Flags)
	}
	if !cfg.HasFlags() {
		t.Fatal("HasFlags = false")
	}
	if got := cfg.FlagValues(); !reflect.DeepEqual(got, map[string]bool{"daily": true, "coop": false, "arena": true}) {
		t.Fatalf("FlagValues = %v", got)
	}
	if got := FlagNames(cfg.Flags); !reflect.DeepEqual(got, []string{"arena", "coop", "daily"}) {
		t.Fatalf("FlagNames = %v", got)
	}
}

// An empty block still means the site serves /__zipgo/flags.json; no block
// means it doesn't.
func TestHasFlags(t *testing.T) {
	empty, err := ParseConfig([]byte(`{"flags": {}}`))
	if err != nil || !empty.HasFlags() {
		t.Fatalf("empty block: HasFlags = %v, %v", empty.HasFlags(), err)
	}
	none, _ := ParseConfig([]byte(`{"headers": {}}`))
	if none.HasFlags() {
		t.Fatal("no block: HasFlags = true")
	}
}

func TestParseConfigRejectsBadFlags(t *testing.T) {
	for name, body := range map[string]string{
		"bad name":       `{"flags": {"co op": true}}`,
		"leading digit":  `{"flags": {"2p": true}}`,
		"whole site":     `{"flags": {"x": {"paths": ["/"]}}}`,
		"glob":           `{"flags": {"x": {"paths": ["/arena/*"]}}}`,
		"traversal":      `{"flags": {"x": {"paths": ["/a/../b"]}}}`,
		"wrong type":     `{"flags": {"x": "on"}}`,
		"not an object":  `{"flags": true}`,
		"on is a string": `{"flags": {"x": {"on": "yes"}}}`,
	} {
		if _, err := ParseConfig([]byte(body)); err == nil {
			t.Errorf("%s: accepted %s", name, body)
		}
	}
}

func TestNormalizeFlagPath(t *testing.T) {
	for in, want := range map[string]string{
		"/arena":         "/arena",
		"arena/":         "/arena",
		"/editor/arena/": "/editor/arena",
		" /x//":          "/x",
		"/":              "",
		"":               "",
	} {
		if got := NormalizeFlagPath(in); got != want {
			t.Errorf("NormalizeFlagPath(%q) = %q, want %q", in, got, want)
		}
	}
	if bad := InvalidFlagPath("/docs/61-coop puzzles.html"); !strings.Contains(bad, "spaces") {
		t.Errorf("a space: %q", bad)
	}
}
