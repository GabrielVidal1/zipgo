package deploy

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"zipgo/internal/sites"
)

const lockedCfg = `{"password": {"hash": "$2a$12$server"}, "headers": {"X-A": "server"}}`

func TestKeepConfig(t *testing.T) {
	tests := []struct {
		name string
		f    folder
		want bool
	}{
		{"no config", folder{}, false},
		{"plain config: mirrored as before", folder{Config: []byte(`{"headers": {"A": "1"}}`)}, false},
		{"password", folder{Config: []byte(lockedCfg)}, true},
		{"basicAuth", folder{Config: []byte(`{"basicAuth": {"gate": "h"}}`)}, true},
		{"switched off", folder{Config: []byte(`{"enable": false}`), Own: sites.Config{Enable: new(bool)}}, true},
		{"unreadable: might be a lock", folder{Err: os.ErrInvalid}, true},
	}
	for _, tc := range tests {
		if got := keepConfig(tc.f); got != tc.want {
			t.Errorf("%s: keepConfig = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A build's own .zipgoconfig.json lands with the server's lock (and off switch)
// kept: its other keys win, the server's lock wins over the build's.
func TestKeepServerState(t *testing.T) {
	tests := []struct {
		name, local, remote string
		keepOff             bool
		want                string // compact JSON
		wantIgnored         string
	}{
		{name: "public site: the build's config as is",
			local: `{"headers": {"A": "1"}}`, remote: `{"headers": {"A": "0"}}`,
			want: `{"headers":{"A":"1"}}`},
		{name: "the lock is appended",
			local: `{"headers": {"X-A": "build"}}`, remote: lockedCfg,
			want: `{"headers":{"X-A":"build"},"password":{"hash":"$2a$12$server"}}`},
		{name: "the build's own password loses",
			local: `{"password": {"hash": "$2a$12$build"}, "enable": true}`, remote: lockedCfg,
			want: `{"password":{"hash":"$2a$12$server"},"enable":true}`, wantIgnored: "password"},
		{name: "the same lock is not reported",
			local: `{"password": {"hash": "$2a$12$server"}}`, remote: lockedCfg,
			want: `{"password":{"hash":"$2a$12$server"}}`},
		{name: "off and locked",
			local: `{"headers": {}}`, remote: `{"enable": false, "basicAuth": {"gate": "h"}, "password": {"hash": "p"}}`, keepOff: true,
			want: `{"enable":false,"headers":{},"password":{"hash":"p"},"basicAuth":{"gate":"h"}}`},
		{name: "an empty build config",
			local: ``, remote: lockedCfg,
			want: `{"password":{"hash":"$2a$12$server"}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, ignored, err := KeepServerState([]byte(tc.local), []byte(tc.remote), tc.keepOff)
			if err != nil {
				t.Fatal(err)
			}
			var c strings.Builder
			var v any
			if err := json.Unmarshal(out, &v); err != nil {
				t.Fatalf("not JSON: %s", out)
			}
			compact := strings.Join(strings.Fields(string(out)), "")
			c.WriteString(compact)
			if c.String() != tc.want {
				t.Errorf("got  %s\nwant %s", c.String(), tc.want)
			}
			if strings.Join(ignored, ",") != tc.wantIgnored {
				t.Errorf("ignored = %v, want %q", ignored, tc.wantIgnored)
			}
		})
	}
	if _, _, err := KeepServerState([]byte(`{"oops`), []byte(lockedCfg), false); err == nil {
		t.Error("a malformed build config was merged")
	}
}

// localDeploy points Run at a local folder: the remote scripts run with sh, and
// the rsync destination is a plain path.
func localDeploy(t *testing.T) string {
	t.Helper()
	localRemote(t)
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
	prev := remoteSpec
	remoteSpec = func(_, dir string) string { return dir }
	t.Cleanup(func() { remoteSpec = prev })
	return t.TempDir()
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// What the Ballance session relies on: mirroring a build into a locked site
// deletes the old build's files but never the lock — with or without a config
// in the build.
func TestRunKeepsTheLock(t *testing.T) {
	base := localDeploy(t)
	site := filepath.Join(base, "example.com", "game.", "ballance.")
	writeFiles(t, site, map[string]string{
		sites.ConfigFileName: lockedCfg,
		"index.html":         "placeholder",
		"old.js":             "stale",
	})

	for _, tc := range []struct {
		name  string
		build map[string]string
		want  string // the X-A header the server's config ends with
	}{
		{"build without a config", map[string]string{"index.html": "game", "assets/app.js": "js"}, "server"},
		{"build with its own config", map[string]string{"index.html": "game", sites.ConfigFileName: `{"headers": {"X-A": "build"}}`}, "build"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := t.TempDir()
			writeFiles(t, src, tc.build)
			o := Options{Delete: true, SSH: "u@h:" + base, Jobs: []Job{{Host: "ballance.game.example.com", Src: src}}}
			if err := Run(o); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(site, "old.js")); !os.IsNotExist(err) {
				t.Error("the mirror kept a stale file")
			}
			if b, _ := os.ReadFile(filepath.Join(site, "index.html")); string(b) != "game" {
				t.Errorf("index.html = %q", b)
			}
			raw, err := os.ReadFile(filepath.Join(site, sites.ConfigFileName))
			if err != nil {
				t.Fatalf("the lock is gone: %v", err)
			}
			cfg, err := sites.ParseConfig(raw)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Password == nil || cfg.Password.Hash != "$2a$12$server" {
				t.Fatalf("lock = %+v (%s)", cfg.Password, raw)
			}
			if cfg.Headers["X-A"] != tc.want {
				t.Errorf("X-A = %q, want %q", cfg.Headers["X-A"], tc.want)
			}
			if left, _ := filepath.Glob(filepath.Join(base, ".zipgo-tmp.*")); len(left) != 0 {
				t.Errorf("temp files left: %v", left)
			}
		})
	}
}

// An unlocked site is mirrored exactly as before — config included.
func TestRunMirrorsAnUnlockedConfig(t *testing.T) {
	base := localDeploy(t)
	site := filepath.Join(base, "example.com", "app.")
	writeFiles(t, site, map[string]string{sites.ConfigFileName: `{"headers": {"X-A": "old"}}`, "index.html": "old"})
	src := t.TempDir()
	writeFiles(t, src, map[string]string{"index.html": "new"})
	if err := Run(Options{Delete: true, SSH: "u@h:" + base, Jobs: []Job{{Host: "app.example.com", Src: src}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(site, sites.ConfigFileName)); !os.IsNotExist(err) {
		t.Error("an unlocked config should be mirrored away, as before")
	}
}

func TestWriteConfigCreatesTheFolder(t *testing.T) {
	localRemote(t)
	base := t.TempDir()
	dir := filepath.Join(base, "example.com", "new.")
	if err := writeConfig(Target{Host: "local", BaseDir: base}, dir, []byte(lockedCfg)); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, sites.ConfigFileName)); err != nil || string(b) != lockedCfg {
		t.Fatalf("config = %q, %v", b, err)
	}
	if left, _ := filepath.Glob(filepath.Join(base, ".zipgo-tmp.*")); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
}
