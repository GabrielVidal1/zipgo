package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseItemized(t *testing.T) {
	// A representative rsync --dry-run --itemize-changes stream: header line,
	// a deletion, an in-place content change, a brand-new dir and file, an
	// unchanged file, and the trailing totals — all of which the parser must
	// bucket or ignore correctly.
	out := strings.Join([]string{
		"sending incremental file list",
		"*deleting   stale.txt",
		"*deleting   old/nested.js",
		">f.st...... index.html", // existing file, attrs changed -> replaced
		"cd+++++++++ assets/",    // new dir -> ignored (files inside are listed)
		">f+++++++++ assets/app.js",
		">f+++++++++ favicon.ico",
		".f          unchanged.txt", // no-change update type -> ignored
		"",
		"sent 176 bytes  received 35 bytes  422.00 bytes/sec",
		"total size is 13  speedup is 0.06 (DRY RUN)",
	}, "\n")

	got := ParseItemized(out)
	want := Summary{
		Added:    []string{"assets/app.js", "favicon.ico"},
		Replaced: []string{"index.html"},
		Deleted:  []string{"old/nested.js", "stale.txt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseItemized:\n got  %+v\n want %+v", got, want)
	}
	if got.Total() != 5 {
		t.Errorf("Total() = %d, want 5", got.Total())
	}
}

func TestParseItemizedEmpty(t *testing.T) {
	// Nothing to do: just the header and totals, no itemized lines.
	out := "sending incremental file list\n\nsent 100 bytes  received 12 bytes\ntotal size is 0  speedup is 0.00 (DRY RUN)\n"
	got := ParseItemized(out)
	if got.Total() != 0 {
		t.Errorf("expected no changes, got %+v", got)
	}
}

func TestParseItemizedSymlinkAndTrailingCR(t *testing.T) {
	// Symlinks (type L) count as content; a trailing \r (rsync over a CRLF
	// pipe) must be stripped so it doesn't pollute the path.
	out := ">L+++++++++ link.txt -> target.txt\r\n>f.s....... page.html\r"
	got := ParseItemized(out)
	// The symlink line's path keeps the "-> target" spelling rsync emits; we
	// only assert it lands in Added and carries no stray CR.
	if len(got.Added) != 1 || strings.ContainsRune(got.Added[0], '\r') {
		t.Errorf("symlink add mishandled: %+v", got.Added)
	}
	if !reflect.DeepEqual(got.Replaced, []string{"page.html"}) {
		t.Errorf("Replaced = %v (trailing CR not stripped?)", got.Replaced)
	}
}

func TestSplitItemizeRejectsNoise(t *testing.T) {
	// Lines that merely look busy but aren't itemized changes must be rejected
	// so a log message can't be misparsed as a file.
	for _, line := range []string{
		"sending incremental file list", // no 11-char flag block at [11]==' '
		"total size is 13  speedup",     // ditto
		"xf+++++++++ nope.txt",          // bad update-type char
		">z+++++++++ nope.txt",          // bad file-type char
		"short",                         // too short
	} {
		if _, _, ok := splitItemize(line); ok {
			t.Errorf("splitItemize(%q): expected ok=false", line)
		}
	}
	flags, p, ok := splitItemize(">f+++++++++ real.txt")
	if !ok || flags != ">f+++++++++" || p != "real.txt" {
		t.Errorf("splitItemize valid line: flags=%q path=%q ok=%v", flags, p, ok)
	}
}

func TestWriteSummary(t *testing.T) {
	var b strings.Builder
	WriteSummary(&b, "app.dev.gabvdl.xyz", Summary{
		Added:    []string{"assets/app.js"},
		Replaced: []string{"index.html"},
		Deleted:  []string{"stale.txt"},
	})
	s := b.String()
	for _, want := range []string{"1 added, 1 replaced, 1 deleted", "assets/app.js", "index.html", "stale.txt", "dry run"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary missing %q:\n%s", want, s)
		}
	}

	b.Reset()
	WriteSummary(&b, "app.dev.gabvdl.xyz", Summary{})
	if !strings.Contains(b.String(), "up to date") {
		t.Errorf("empty summary should say up to date, got:\n%s", b.String())
	}
}

func TestRsyncArgs(t *testing.T) {
	// Default (delete on, no subdomains): mirror with the subdomain guard.
	got := rsyncArgs(Options{Delete: true}, "dist/", "u@h:/d/", false)
	want := []string{"-avz", "--delete", "--exclude=" + subdomainExclude, "dist/", "u@h:/d/"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("default args = %v, want %v", got, want)
	}

	// Dry-run adds --dry-run --itemize-changes before the paths.
	got = rsyncArgs(Options{Delete: true}, "dist/", "u@h:/d/", true)
	if !contains(got, "--dry-run") || !contains(got, "--itemize-changes") {
		t.Errorf("dry-run args missing itemize flags: %v", got)
	}
	if got[len(got)-2] != "dist/" || got[len(got)-1] != "u@h:/d/" {
		t.Errorf("paths must stay last: %v", got)
	}

	// --include-subdomains drops the auto-exclude; extra excludes are appended.
	got = rsyncArgs(Options{Delete: true, IncludeSubdomains: true, Excludes: []string{"*.map"}}, "dist/", "u@h:/d/", false)
	if contains(got, "--exclude="+subdomainExclude) {
		t.Errorf("--include-subdomains should drop the subdomain guard: %v", got)
	}
	if !contains(got, "--exclude=*.map") {
		t.Errorf("custom exclude missing: %v", got)
	}

	// --no-delete: no mirror, no subdomain guard.
	got = rsyncArgs(Options{Delete: false}, "dist/", "u@h:/d/", false)
	if contains(got, "--delete") || contains(got, "--exclude="+subdomainExclude) {
		t.Errorf("no-delete should not mirror: %v", got)
	}
}

func TestParseArgsPrune(t *testing.T) {
	// --prune is an alias for the default --delete mirror.
	o, err := ParseArgs([]string{"dist/", "-d", "x.gabvdl.xyz", "--ssh", "u@h:/b", "--prune"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.Delete {
		t.Error("--prune should set Delete")
	}

	// --prune after --no-delete re-enables the mirror (last flag wins).
	o, err = ParseArgs([]string{"dist/", "-d", "x", "--ssh", "u@h:/b", "--no-delete", "--prune"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.Delete {
		t.Error("--prune after --no-delete should re-enable Delete")
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// TestParseItemizedFromRealRsync exercises the parser against genuine rsync
// output (local transfer, dry-run) so the classification stays honest if
// rsync's itemize format ever drifts. Skipped when rsync is unavailable.
func TestParseItemizedFromRealRsync(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
	src := t.TempDir()
	dst := t.TempDir()
	writeFile(t, src, "index.html", "a-fresh-index-with-a-different-size")
	writeFile(t, src, "assets/app.js", "console.log(1)")
	writeFile(t, dst, "index.html", "old")       // replaced (size differs)
	writeFile(t, dst, "stale.txt", "gone")       // deleted by --delete
	writeFile(t, dst, "pb./index.html", "child") // nested subdomain: protected

	args := rsyncArgs(Options{Delete: true}, src+"/", dst+"/", true)
	out, err := exec.Command("rsync", args...).Output()
	if err != nil {
		t.Fatalf("rsync: %v", err)
	}
	got := ParseItemized(string(out))

	if !contains(got.Added, "assets/app.js") {
		t.Errorf("expected assets/app.js added; got %+v", got)
	}
	if !contains(got.Replaced, "index.html") {
		t.Errorf("expected index.html replaced; got %+v", got)
	}
	if !contains(got.Deleted, "stale.txt") {
		t.Errorf("expected stale.txt deleted; got %+v", got)
	}
	// The nested subdomain folder must be protected by the auto --exclude, so it
	// never shows up as a deletion.
	for _, p := range got.Deleted {
		if strings.HasPrefix(p, "pb.") {
			t.Errorf("nested subdomain %q must not be deleted", p)
		}
	}
}

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
