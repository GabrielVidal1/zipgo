package flags

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"zipgo/internal/password"
	"zipgo/internal/sites"
)

func TestParseArgs(t *testing.T) {
	o, err := ParseArgs([]string{"ballance.game.example.com"})
	if err != nil || o.Verb != "ls" || o.Host != "ballance.game.example.com" {
		t.Fatalf("bare host: %+v, %v", o, err)
	}
	o, err = ParseArgs([]string{"add", "spore.example.com", "arena", "--description", "The arena", "--path", "/editor/arena", "--path=/x", "--on"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Verb != "add" || o.Names[0] != "arena" || *o.Description != "The arena" || len(o.Paths) != 2 || !*o.On {
		t.Fatalf("add: %+v", o)
	}
	o, err = ParseArgs([]string{"on", "x.example.com", "coop", "daily", "--dir", "/srv"})
	if err != nil || len(o.Names) != 2 || o.Dir != "/srv" {
		t.Fatalf("on: %+v, %v", o, err)
	}
	for _, bad := range [][]string{
		{},
		{"frob", "x.example.com"},
		{"on", "x.example.com"},
		{"on", "x.example.com", "coop", "--on"},
		{"add", "x.example.com", "a", "b"},
		{"add", "x.example.com", "co op"},
		{"add", "x.example.com", "a", "--path", "/"},
		{"ls", "x.example.com", "--ssh", "a:/b", "--dir", "/c"},
	} {
		if _, err := ParseArgs(bad); err == nil {
			t.Errorf("%q: accepted", bad)
		}
	}
}

func TestRunLocal(t *testing.T) {
	base := t.TempDir()
	store := password.Local{Base: base}
	host := "ballance.game.example.com"
	run := func(args ...string) string {
		t.Helper()
		o, err := ParseArgs(args)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := Run(o, store, &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if out := run(host); !strings.Contains(out, "has no flags") {
		t.Fatalf("ls before: %s", out)
	}
	run("add", host, "coop", "--description", "Co-op levels", "--path", "/docs/61.html")
	run("add", host, "daily", "--on")
	run("on", host, "coop")
	out := run(host)
	if !strings.Contains(out, "● on   coop   Co-op levels  (covers /docs/61.html)") || !strings.Contains(out, "● on   daily") {
		t.Fatalf("ls after:\n%s", out)
	}
	run("rm", host, "daily")
	raw, err := os.ReadFile(filepath.Join(base, "example.com", "game.", "ballance.", sites.ConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := sites.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Flags) != 1 || !cfg.Flags["coop"].On || cfg.Flags["coop"].Description != "Co-op levels" {
		t.Fatalf("config: %s", raw)
	}
	if out := run("off", host, "coop"); !strings.Contains(out, "⏸ off  coop") {
		t.Fatalf("off: %s", out)
	}
}
