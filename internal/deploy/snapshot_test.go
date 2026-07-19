package deploy

import (
	"strings"
	"testing"
)

func TestParseArgsKeep(t *testing.T) {
	// Default: snapshotting on, DefaultKeep retained.
	o, err := ParseArgs([]string{"dist/", "-d", "x.gabvdl.xyz", "--ssh", "u@h:/b"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Keep != DefaultKeep {
		t.Errorf("Keep = %d, want default %d", o.Keep, DefaultKeep)
	}

	// --keep N (space and =-form) sets the retention.
	for _, args := range [][]string{
		{"dist/", "-d", "x", "--ssh", "u@h:/b", "--keep", "3"},
		{"dist/", "-d", "x", "--ssh", "u@h:/b", "--keep=3"},
	} {
		o, err := ParseArgs(args)
		if err != nil {
			t.Fatalf("ParseArgs(%v): %v", args, err)
		}
		if o.Keep != 3 {
			t.Errorf("ParseArgs(%v): Keep = %d, want 3", args, o.Keep)
		}
	}

	// --no-history and --keep 0 both disable snapshotting.
	for _, args := range [][]string{
		{"dist/", "-d", "x", "--ssh", "u@h:/b", "--no-history"},
		{"dist/", "-d", "x", "--ssh", "u@h:/b", "--keep", "0"},
	} {
		o, err := ParseArgs(args)
		if err != nil {
			t.Fatalf("ParseArgs(%v): %v", args, err)
		}
		if o.Keep != 0 {
			t.Errorf("ParseArgs(%v): Keep = %d, want 0 (disabled)", args, o.Keep)
		}
	}

	// A negative or non-numeric --keep is an error.
	for _, bad := range []string{"-1", "x", ""} {
		if _, err := ParseArgs([]string{"dist/", "-d", "x", "--ssh", "u@h:/b", "--keep=" + bad}); err == nil {
			t.Errorf("--keep=%q: expected error", bad)
		}
	}
}

func TestParseKeep(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
		ok   bool
	}{
		{"5", 5, true}, {"0", 0, true}, {" 7 ", 7, true},
		{"-1", 0, false}, {"", 0, false}, {"nope", 0, false},
	} {
		n, err := parseKeep(c.in)
		if c.ok && err != nil {
			t.Errorf("parseKeep(%q): unexpected error %v", c.in, err)
		}
		if !c.ok && err == nil {
			t.Errorf("parseKeep(%q): expected error", c.in)
		}
		if c.ok && n != c.want {
			t.Errorf("parseKeep(%q) = %d, want %d", c.in, n, c.want)
		}
	}
}

// TestSnapshotScript checks the invariants that make a snapshot safe: it guards
// on a real prior deploy, excludes the history folder and nested subdomains, and
// prunes to the requested count.
func TestSnapshotScript(t *testing.T) {
	const dir = "/base/gabvdl.xyz/game./love-letters."
	s := snapshotScript(dir, 5)

	// Guards: no snapshot without the folder and a real index.html.
	if !strings.Contains(s, "[ -d \"$d\" ] || exit 0") {
		t.Error("missing folder-exists guard")
	}
	if !strings.Contains(s, "[ -f \"$d/index.html\" ] || exit 0") {
		t.Error("missing index.html guard (would snapshot an empty/broken deploy)")
	}
	// The history folder and nested subdomain sites are excluded from the copy.
	if !strings.Contains(s, "--exclude='.zipgo-versions'") {
		t.Error("snapshot must exclude the .zipgo-versions folder")
	}
	if !strings.Contains(s, "--exclude='*./'") {
		t.Error("snapshot must exclude nested subdomain folders")
	}
	// Snapshots go under the site's own .zipgo-versions folder.
	if !strings.Contains(s, dir+"/"+".zipgo-versions") {
		t.Errorf("snapshot dir not under %s/.zipgo-versions", dir)
	}
	// Prune keeps the requested count (head -n -5 drops all but the newest 5).
	if !strings.Contains(s, "head -n -5") {
		t.Errorf("prune should keep 5 newest, script:\n%s", s)
	}

	// keep=2 changes the prune count.
	if !strings.Contains(snapshotScript(dir, 2), "head -n -2") {
		t.Error("keep=2 should prune to head -n -2")
	}
}

// TestVersionsExclude documents the rsync pattern that keeps a redeploy's
// --delete mirror from wiping the history it just wrote.
func TestVersionsExclude(t *testing.T) {
	if versionsExclude != "/.zipgo-versions/" {
		t.Errorf("versionsExclude = %q, want /.zipgo-versions/", versionsExclude)
	}
}
