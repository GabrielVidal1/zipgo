package deploy

import (
	"reflect"
	"testing"
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

	for _, args := range [][]string{
		{"-d", "x.gabvdl.xyz", "--ssh", "u@h:/b"},        // missing src
		{"dist/", "--ssh", "u@h:/b"},                     // missing -d
		{"dist/", "-d", "x.gabvdl.xyz"},                  // missing --ssh
		{"dist/", "extra", "-d", "x", "--ssh", "u@h:/b"}, // two positionals
		{"dist/", "--bogus"},                             // unknown flag
	} {
		if _, err := ParseArgs(args); err == nil {
			t.Errorf("ParseArgs(%v): expected error", args)
		}
	}
}
