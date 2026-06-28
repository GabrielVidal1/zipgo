package deploy

import (
	"path"
	"reflect"
	"testing"

	"zipgo/internal/zipconfig"
)

func TestRemoteDir(t *testing.T) {
	const base = "/home/gabrielvidal/services/domains"
	cases := []struct {
		host string
		want string
	}{
		{"love-letters.game.gabvdl.xyz", base + "/gabvdl.xyz/game./love-letters."},
		{"ai.pics.gabvdl.xyz", base + "/gabvdl.xyz/pics./ai."},
		{"gabvdl.xyz", base + "/gabvdl.xyz"},
		{"www.dev.gabvdl.xyz", base + "/gabvdl.xyz/dev./www."},
		{"vidal--ayrinhac.xyz", base + "/vidal--ayrinhac.xyz"},
		{"a.b.c.zipgo.xyz", base + "/zipgo.xyz/c./b./a."},
		{"love-letters.game.gabvdl.xyz.", base + "/gabvdl.xyz/game./love-letters."}, // trailing dot tolerated
	}
	for _, c := range cases {
		got, err := RemoteDir(base, c.host)
		if err != nil {
			t.Fatalf("RemoteDir(%q): unexpected error %v", c.host, err)
		}
		if got != c.want {
			t.Errorf("RemoteDir(%q) = %q, want %q", c.host, got, c.want)
		}
	}
}

func TestHostFromRemote(t *testing.T) {
	const base = "/home/gabrielvidal/services/domains"
	ok := []struct{ dir, want string }{
		{base + "/gabvdl.xyz", "gabvdl.xyz"},
		{base + "/gabvdl.xyz/game./love-letters.", "love-letters.game.gabvdl.xyz"},
		{base + "/gabvdl.xyz/dev./www.", "www.dev.gabvdl.xyz"},
		{base + "/zipgo.xyz/c./b./a.", "a.b.c.zipgo.xyz"},
		{base + "/gabvdl.xyz/game.", "game.gabvdl.xyz"}, // parent subdomain folder
	}
	for _, c := range ok {
		got, valid := HostFromRemote(base, c.dir)
		if !valid {
			t.Errorf("HostFromRemote(%q): expected valid site folder", c.dir)
		} else if got != c.want {
			t.Errorf("HostFromRemote(%q) = %q, want %q", c.dir, got, c.want)
		}
		// Round-trips with RemoteDir.
		if back, err := RemoteDir(base, got); err == nil && back != path.Clean(c.dir) {
			t.Errorf("round-trip %q: RemoteDir = %q", c.dir, back)
		}
	}
	notSites := []string{
		base,                        // the base itself
		base + "/gabvdl.xyz/assets", // content dir (no trailing dot)
		base + "/gabvdl.xyz/game./love-letters./assets", // nested content dir
		"/some/other/path/gabvdl.xyz",                   // outside base
		base + "/nodot",                                 // apex without a dot
	}
	for _, d := range notSites {
		if _, valid := HostFromRemote(base, d); valid {
			t.Errorf("HostFromRemote(%q): expected not-a-site", d)
		}
	}
}

func TestRemoteDirInvalid(t *testing.T) {
	for _, host := range []string{"", "localhost", "x.", ".x", "a..b.com"} {
		if _, err := RemoteDir("/base", host); err == nil {
			t.Errorf("RemoteDir(%q): expected error, got nil", host)
		}
	}
}

func TestParseTarget(t *testing.T) {
	tgt, err := ParseTarget("gabrielvidal@100.74.118.12:/home/gabrielvidal/services/domains/")
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Host != "gabrielvidal@100.74.118.12" {
		t.Errorf("Host = %q", tgt.Host)
	}
	if tgt.BaseDir != "/home/gabrielvidal/services/domains" {
		t.Errorf("BaseDir = %q (trailing slash should be trimmed)", tgt.BaseDir)
	}
	for _, bad := range []string{"", "nohost", ":/path", "user@host:"} {
		if _, err := ParseTarget(bad); err == nil {
			t.Errorf("ParseTarget(%q): expected error", bad)
		}
	}
}

func TestParseArgs(t *testing.T) {
	o, err := ParseArgs([]string{
		"dist/",
		"-d", "love-letters.game.gabvdl.xyz",
		"--ssh", "gabrielvidal@host:/base",
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.Src != "dist/" {
		t.Errorf("Src = %q", o.Src)
	}
	if !reflect.DeepEqual(o.Hosts, []string{"love-letters.game.gabvdl.xyz"}) {
		t.Errorf("Hosts = %v", o.Hosts)
	}
	if o.SSH != "gabrielvidal@host:/base" {
		t.Errorf("SSH = %q", o.SSH)
	}
	if !o.Delete {
		t.Error("Delete should default to true")
	}

	// repeated -d, flags-before-positional, =-forms, --no-delete, --exclude
	o, err = ParseArgs([]string{
		"--ssh=u@h:/b",
		"-d=a.gabvdl.xyz", "--domain", "b.gabvdl.xyz",
		"--no-delete", "--exclude", "images/", "--exclude=*.map",
		"build",
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.Src != "build" {
		t.Errorf("Src = %q", o.Src)
	}
	if !reflect.DeepEqual(o.Hosts, []string{"a.gabvdl.xyz", "b.gabvdl.xyz"}) {
		t.Errorf("Hosts = %v", o.Hosts)
	}
	if o.Delete {
		t.Error("--no-delete should clear Delete")
	}
	if !reflect.DeepEqual(o.Excludes, []string{"images/", "*.map"}) {
		t.Errorf("Excludes = %v", o.Excludes)
	}
	if o.IncludeSubdomains {
		t.Error("IncludeSubdomains should default to false")
	}

	// --include-subdomains opts out of the auto-exclude
	o, err = ParseArgs([]string{
		"dist/", "-d", "x.gabvdl.xyz", "--ssh", "u@h:/b", "--include-subdomains",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !o.IncludeSubdomains {
		t.Error("--include-subdomains should set IncludeSubdomains")
	}

	// Missing src/-d/--ssh are now tolerated by ParseArgs (filled by Resolve).
	for _, args := range [][]string{
		{"dist/", "extra", "-d", "x", "--ssh", "u@h:/b"}, // two positionals
		{"dist/", "--bogus"},                             // unknown flag
	} {
		if _, err := ParseArgs(args); err == nil {
			t.Errorf("ParseArgs(%v): expected error", args)
		}
	}
}

func TestResolve(t *testing.T) {
	root := zipconfig.RootConfig{
		Target:  "u@h:/base",
		Targets: map[string]string{"alt": "u@alt:/base2"},
	}
	proj := zipconfig.ProjectConfig{
		Deploy: map[string]string{
			"a.gabvdl.xyz":     "dist",
			"b.dev.gabvdl.xyz": "dist/b",
		},
	}

	// No flags: deploy every mapped host from its mapped folder, target from root.
	o := Options{}
	if err := Resolve(&o, proj, root); err != nil {
		t.Fatal(err)
	}
	if o.SSH != "u@h:/base" {
		t.Errorf("SSH = %q", o.SSH)
	}
	want := []Job{{"a.gabvdl.xyz", "dist"}, {"b.dev.gabvdl.xyz", "dist/b"}} // sorted
	if !reflect.DeepEqual(o.Jobs, want) {
		t.Errorf("Jobs = %v, want %v", o.Jobs, want)
	}

	// -d picks one host; source comes from the map.
	o = Options{Hosts: []string{"b.dev.gabvdl.xyz"}}
	if err := Resolve(&o, proj, root); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(o.Jobs, []Job{{"b.dev.gabvdl.xyz", "dist/b"}}) {
		t.Errorf("Jobs = %v", o.Jobs)
	}

	// Named target via project "target" override.
	o = Options{}
	if err := Resolve(&o, zipconfig.ProjectConfig{Deploy: proj.Deploy, Target: "alt"}, root); err != nil {
		t.Fatal(err)
	}
	if o.SSH != "u@alt:/base2" {
		t.Errorf("named target SSH = %q", o.SSH)
	}

	// Fully explicit (no config) still works — back-compat path.
	o = Options{Src: "build", Hosts: []string{"x.gabvdl.xyz"}, SSH: "u@h:/b"}
	if err := Resolve(&o, zipconfig.ProjectConfig{}, zipconfig.RootConfig{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(o.Jobs, []Job{{"x.gabvdl.xyz", "build"}}) {
		t.Errorf("Jobs = %v", o.Jobs)
	}

	// Error cases.
	errCases := []struct {
		name string
		o    Options
		proj zipconfig.ProjectConfig
		root zipconfig.RootConfig
	}{
		{"no target", Options{Hosts: []string{"x.gabvdl.xyz"}, Src: "d"}, zipconfig.ProjectConfig{}, zipconfig.RootConfig{}},
		{"no hosts", Options{}, zipconfig.ProjectConfig{}, root},
		{"no source for host", Options{Hosts: []string{"z.gabvdl.xyz"}}, proj, root},
		{"ambiguous positional", Options{Src: "d"}, proj, root},
	}
	for _, c := range errCases {
		o := c.o
		if err := Resolve(&o, c.proj, c.root); err == nil {
			t.Errorf("Resolve(%s): expected error", c.name)
		}
	}
}
