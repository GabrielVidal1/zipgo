package remote

import (
	"strings"
	"testing"

	"zipgo/internal/sites"
)

func TestParseVersions(t *testing.T) {
	// Two snapshots, out of order, plus a blank line and a literal glob line
	// (what the shell leaves when .zipgo-versions is empty).
	out := "20260101T120000Z\t1735732800\t2048\t3\n" +
		"20260705T093000Z\t1751708200\t4096\t7\n" +
		"\n" +
		"*/\t\t\t\n"
	got := parseVersions(out)
	if len(got) != 2 {
		t.Fatalf("got %d versions, want 2: %+v", len(got), got)
	}
	// Newest first.
	if got[0].Name != "20260705T093000Z" || got[1].Name != "20260101T120000Z" {
		t.Errorf("not sorted newest-first: %q, %q", got[0].Name, got[1].Name)
	}
	if got[0].Size != 4096 || got[0].Files != 7 {
		t.Errorf("size/files = %d/%d, want 4096/7", got[0].Size, got[0].Files)
	}
	if got[0].Modified == "" {
		t.Error("Modified should be an RFC3339 timestamp")
	}
}

func TestParseVersionsEmpty(t *testing.T) {
	// An empty history (only the unmatched glob) yields an empty, non-nil slice.
	got := parseVersions("*/\t\t\t\n")
	if got == nil || len(got) != 0 {
		t.Errorf("want empty non-nil slice, got %+v", got)
	}
}

func TestHasVersion(t *testing.T) {
	vs := []Version{{Name: "a"}, {Name: "b"}}
	if !hasVersion(vs, "b") {
		t.Error("hasVersion should find b")
	}
	if hasVersion(vs, "c") {
		t.Error("hasVersion should not find c")
	}
}

func TestRestoreScriptKeepsServerState(t *testing.T) {
	s := restoreScript("/srv/domains/example.com/app.", "20260705T093000Z")
	// Both the backup and the restore must leave the live config alone (a
	// rollback never undoes an off switch, a lock or a flag), the history
	// folder and nested subdomain sites.
	for _, want := range []string{
		"--exclude='/" + sites.ConfigFileName + "'",
		"--exclude='" + sites.VersionsDirName + "'",
		"--exclude='*./'",
	} {
		if n := strings.Count(s, want); n != 2 {
			t.Errorf("restore script has %d × %s, want 2 (backup + restore):\n%s", n, want, s)
		}
	}
	if !strings.Contains(s, "src='/srv/domains/example.com/app./"+sites.VersionsDirName+"/20260705T093000Z'") {
		t.Errorf("restore script does not point at the snapshot:\n%s", s)
	}
}
