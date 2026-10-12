package deploy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"zipgo/internal/sites"
)

func TestSetEnable(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		on      bool
		want    string // "" with wantNil → delete the file
		wantNil bool
		changed bool
	}{
		{name: "off, no config", raw: "", on: false,
			want: "{\n  \"enable\": false\n}\n", changed: true},
		{name: "off leads with enable, keeps the rest in order", raw: `{"rewrite":"app:8090","headers":{"X-A":"1"}}`, on: false,
			want: "{\n  \"enable\": false,\n  \"rewrite\": \"app:8090\",\n  \"headers\": {\n    \"X-A\": \"1\"\n  }\n}\n", changed: true},
		{name: "off overwrites an enable where it stands", raw: `{"rewrite":"app:8090","enable":true}`, on: false,
			want: "{\n  \"rewrite\": \"app:8090\",\n  \"enable\": false\n}\n", changed: true},
		{name: "off when already off", raw: `{"enable": false}`, on: false,
			want: `{"enable": false}`, changed: false},
		{name: "on drops enable and deletes an emptied file", raw: `{"enable": false}`, on: true,
			wantNil: true, changed: true},
		{name: "on keeps the other keys", raw: `{"enable": false, "allowHttp": true}`, on: true,
			want: "{\n  \"allowHttp\": true\n}\n", changed: true},
		{name: "on when already on (explicit)", raw: `{"enable": true}`, on: true,
			want: `{"enable": true}`, changed: false},
		{name: "on when already on (no config)", raw: "", on: true,
			want: "", changed: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, changed, err := SetEnable([]byte(c.raw), c.on)
			if err != nil {
				t.Fatal(err)
			}
			if changed != c.changed {
				t.Errorf("changed = %v, want %v", changed, c.changed)
			}
			if c.wantNil {
				if out != nil {
					t.Errorf("want nil (delete the file), got %q", out)
				}
				return
			}
			if string(out) != c.want {
				t.Errorf("got\n%s\nwant\n%s", out, c.want)
			}
			if changed {
				if _, err := sites.ParseConfig(out); err != nil {
					t.Errorf("output is not a valid config: %v", err)
				}
			}
		})
	}
}

func TestSetEnableRefusesWhatTheServerRejects(t *testing.T) {
	for _, raw := range []string{`{not json`, `[1, 2]`, `{"headers": {"Bad Name": "x"}}`, `{"enable": "no"}`} {
		if _, _, err := SetEnable([]byte(raw), false); err == nil {
			t.Errorf("%s: want an error, got none", raw)
		}
	}
}

func TestParentDirs(t *testing.T) {
	base := "/srv/domains"
	cases := map[string][]string{
		"/srv/domains/example.com":            nil,
		"/srv/domains/example.com/app.":       {"/srv/domains/example.com"},
		"/srv/domains/example.com/app./pb.":   {"/srv/domains/example.com", "/srv/domains/example.com/app."},
		"/srv/domains/example.com/dev./a./b.": {"/srv/domains/example.com", "/srv/domains/example.com/dev.", "/srv/domains/example.com/dev./a."},
	}
	for dir, want := range cases {
		if got := ParentDirs(base, dir); !reflect.DeepEqual(got, want) {
			t.Errorf("ParentDirs(%q) = %v, want %v", dir, got, want)
		}
	}
}

func TestParseSwitchArgs(t *testing.T) {
	o, err := ParseSwitchArgs([]string{"a.example.com", "--ssh", "u@h:/b", "b.example.com", "-n"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(o.Hosts, []string{"a.example.com", "b.example.com"}) || o.SSH != "u@h:/b" || !o.DryRun {
		t.Errorf("got %+v", o)
	}
	if _, err := ParseSwitchArgs(nil); err == nil {
		t.Error("no host should be an error")
	}
	if _, err := ParseSwitchArgs([]string{"x.example.com", "--force"}); err == nil {
		t.Error("an unknown flag should be an error")
	}
}

// localRemote makes runRemote run the scripts with the local sh, so the very
// scripts that go over SSH are exercised against a temp folder.
func localRemote(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("base64"); err != nil {
		t.Skip("base64 not installed")
	}
	prev := runRemote
	runRemote = func(_, script string, stdin []byte) (string, error) {
		cmd := exec.Command("sh", "-c", script)
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		out, err := cmd.Output()
		return string(out), err
	}
	t.Cleanup(func() { runRemote = prev })
}

// tree creates base/<dirs> and returns base.
func tree(t *testing.T, dirs ...string) string {
	t.Helper()
	base := t.TempDir()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return base
}

func readConfigFile(t *testing.T, dir string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, sites.ConfigFileName))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b), true
}

func TestSwitchOffAndOn(t *testing.T) {
	localRemote(t)
	base := tree(t, "example.com/app./pb.", "example.com/app./assets/not-a-site.", "example.com/blog.")
	app := filepath.Join(base, "example.com", "app.")
	if err := os.WriteFile(filepath.Join(app, sites.ConfigFileName), []byte(`{"headers": {"X-A": "1"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	tgt := Target{Host: "local", BaseDir: base}

	var out bytes.Buffer
	if err := Switch(&out, tgt, SwitchOptions{Hosts: []string{"app.example.com"}}, false); err != nil {
		t.Fatal(err)
	}
	cfg, _ := readConfigFile(t, app)
	if want := "{\n  \"enable\": false,\n  \"headers\": {\n    \"X-A\": \"1\"\n  }\n}\n"; cfg != want {
		t.Errorf("config after off:\n%s\nwant\n%s", cfg, want)
	}
	if got := out.String(); !strings.Contains(got, "app.example.com is now off — it serves nothing itself") ||
		!strings.Contains(got, "1 nested site with it: pb.app.example.com") {
		t.Errorf("off output:\n%s", got)
	}
	if entries, _ := os.ReadDir(app); len(entries) != 3 { // assets/, pb./, config
		t.Errorf("left a temp file behind: %v", entries)
	}

	out.Reset()
	if err := Switch(&out, tgt, SwitchOptions{Hosts: []string{"app.example.com"}}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already off") {
		t.Errorf("second off should be a no-op, got:\n%s", out.String())
	}

	// A nested site under an off parent: `on` reports the parent in the way.
	out.Reset()
	if err := Switch(&out, tgt, SwitchOptions{Hosts: []string{"pb.app.example.com"}}, true); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "off through app.example.com") ||
		!strings.Contains(got, "zipgo on app.example.com") {
		t.Errorf("on under an off parent:\n%s", got)
	}

	out.Reset()
	if err := Switch(&out, tgt, SwitchOptions{Hosts: []string{"app.example.com"}}, true); err != nil {
		t.Fatal(err)
	}
	cfg, _ = readConfigFile(t, app)
	if want := "{\n  \"headers\": {\n    \"X-A\": \"1\"\n  }\n}\n"; cfg != want {
		t.Errorf("config after on:\n%s\nwant\n%s", cfg, want)
	}
	if !strings.Contains(out.String(), "back on") {
		t.Errorf("on output:\n%s", out.String())
	}

	// A site with a page of its own answers 410; with no config of its own, it
	// gets none back after off + on.
	blog := filepath.Join(base, "example.com", "blog.")
	if err := os.WriteFile(filepath.Join(blog, "index.html"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	for _, on := range []bool{false, true} {
		if err := Switch(&out, tgt, SwitchOptions{Hosts: []string{"blog.example.com"}}, on); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(out.String(), "blog.example.com is now off — it answers 410 Gone") {
		t.Errorf("off output for a site with a page:\n%s", out.String())
	}
	if _, ok := readConfigFile(t, blog); ok {
		t.Error("off + on should leave no config behind")
	}
}

func TestSwitchDryRunTouchesNothing(t *testing.T) {
	localRemote(t)
	base := tree(t, "example.com/app.")
	var out bytes.Buffer
	err := Switch(&out, Target{Host: "local", BaseDir: base}, SwitchOptions{Hosts: []string{"app.example.com"}, DryRun: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := readConfigFile(t, filepath.Join(base, "example.com", "app.")); ok {
		t.Error("a dry run wrote a config")
	}
	if !strings.Contains(out.String(), "would be off") {
		t.Errorf("dry-run output:\n%s", out.String())
	}
}

func TestSwitchRefusals(t *testing.T) {
	localRemote(t)
	base := tree(t, "example.com/app.")
	tgt := Target{Host: "local", BaseDir: base}
	var out bytes.Buffer
	if err := Switch(&out, tgt, SwitchOptions{Hosts: []string{"missing.example.com"}}, false); err == nil ||
		!strings.Contains(err.Error(), "not deployed") {
		t.Errorf("an undeployed host should be refused, got %v", err)
	}
	app := filepath.Join(base, "example.com", "app.")
	if err := os.WriteFile(filepath.Join(app, sites.ConfigFileName), []byte(`{oops`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Switch(&out, tgt, SwitchOptions{Hosts: []string{"app.example.com"}}, false); err == nil {
		t.Error("a malformed config should be refused, not rewritten")
	}
	if cfg, _ := readConfigFile(t, app); cfg != `{oops` {
		t.Errorf("the malformed config was touched: %q", cfg)
	}
}

// A redeploy of a switched-off site: the remote config is excluded from the
// mirror (so --delete can't remove it and the build can't overwrite it), and the
// build's own config is merged back in with the switch kept off.
func TestDeployKeepsAnOffSiteOff(t *testing.T) {
	localRemote(t)
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
	base := tree(t, "example.com/app.")
	remote := filepath.Join(base, "example.com", "app.")
	if err := os.WriteFile(filepath.Join(remote, sites.ConfigFileName), []byte(`{"enable": false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "index.html"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	tgt := Target{Host: "local", BaseDir: base}

	st, err := inspect(tgt, "app.example.com", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Self.ownOff() {
		t.Fatal("inspect should see the site as off")
	}
	o := Options{Delete: true, Excludes: []string{"/" + sites.ConfigFileName}}
	if out, err := exec.Command("rsync", rsyncArgs(o, src+"/", remote+"/", false)...).CombinedOutput(); err != nil {
		t.Fatalf("rsync: %v\n%s", err, out)
	}
	if cfg, ok := readConfigFile(t, remote); !ok || cfg != `{"enable": false}` {
		t.Fatalf("the mirror removed or changed the off switch: %q", cfg)
	}

	// The build ships a config of its own: merged in, still off.
	if err := os.WriteFile(filepath.Join(src, sites.ConfigFileName), []byte(`{"headers": {"X-B": "2"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mergeKeptOff(tgt, remote, src); err != nil {
		t.Fatal(err)
	}
	cfg, _ := readConfigFile(t, remote)
	c, err := sites.ParseConfig([]byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if c.Enabled() || c.Headers["X-B"] != "2" {
		t.Errorf("merged config = %s, want the build's headers with enable:false", cfg)
	}
}
