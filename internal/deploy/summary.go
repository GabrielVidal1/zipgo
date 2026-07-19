package deploy

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Change is a single file/dir the deploy would touch, as reported by rsync's
// itemize-changes output under --dry-run.
type Change struct {
	Path string
	Kind ChangeKind
}

// ChangeKind classifies an itemized rsync line into the three buckets the
// dry-run summary reports.
type ChangeKind int

const (
	// Added is a path that does not exist on the remote yet (rsync creates it).
	Added ChangeKind = iota
	// Replaced is a path that exists on the remote and whose content or
	// attributes the deploy would overwrite.
	Replaced
	// Deleted is a remote path the mirror (--delete) would remove because it is
	// no longer present in the source.
	Deleted
)

func (k ChangeKind) String() string {
	switch k {
	case Added:
		return "added"
	case Replaced:
		return "replaced"
	case Deleted:
		return "deleted"
	default:
		return "unknown"
	}
}

// Summary is the categorised outcome of a dry-run: which remote paths would be
// added, replaced and deleted. It is what the dry-run prints before anything is
// touched.
type Summary struct {
	Added    []string
	Replaced []string
	Deleted  []string
}

// Total is the number of paths the deploy would touch across all three buckets.
func (s Summary) Total() int {
	return len(s.Added) + len(s.Replaced) + len(s.Deleted)
}

// ParseItemized turns the itemize-changes lines rsync emits under
// `--dry-run --itemize-changes` into a Summary. It is pure so it can be
// table-tested without shelling out.
//
// rsync's itemized format is an 11-character change string, e.g.:
//
//	>f+++++++++ assets/app.js   new file (added)
//	>f.st...... index.html      existing file, content/attrs changed (replaced)
//	cd+++++++++ assets/         new directory (added)
//	*deleting   stale.txt       removed by --delete (deleted)
//
// Position 1 is the update type, position 2 the file type, and positions 3–11
// are per-attribute flags where '+' marks a freshly created item and '.' an
// unchanged one. A path whose attribute flags are all '+' is new (Added);
// anything else that transfers is an overwrite (Replaced). Directory-only
// creations and unchanged lines are ignored — the summary is about file
// content, so an empty parent dir that merely gets made isn't noise worth
// listing.
func ParseItemized(out string) Summary {
	var s Summary
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		// Deletions are reported as a message line, not an itemized change.
		if rest, ok := strings.CutPrefix(line, "*deleting"); ok {
			if p := strings.TrimSpace(rest); p != "" {
				s.Deleted = append(s.Deleted, cleanPath(p))
			}
			continue
		}
		flags, p, ok := splitItemize(line)
		if !ok {
			continue // status noise ("sending incremental file list", totals, "")
		}
		// Update type '.' means "no update occurring" — the item exists and is
		// already identical, so it isn't a change worth reporting.
		if flags[0] == '.' {
			continue
		}
		switch flags[1] {
		case 'f', 'L', 'S', 'D':
			// A regular file, symlink, special or device node.
		default:
			// Directories (type 'd') and anything else: skip. A dir only ever
			// carries structure, and rsync lists the files inside it anyway.
			continue
		}
		if attrsAllNew(flags) {
			s.Added = append(s.Added, cleanPath(p))
		} else {
			s.Replaced = append(s.Replaced, cleanPath(p))
		}
	}
	sort.Strings(s.Added)
	sort.Strings(s.Replaced)
	sort.Strings(s.Deleted)
	return s
}

// splitItemize pulls the 11-char change string and the path out of one
// itemized line. It returns ok=false for lines that are not itemized changes
// (rsync's header/footer and blank lines), so callers can skip them.
func splitItemize(line string) (flags, p string, ok bool) {
	// An itemized line is "<11 flag chars><space><path>". The first two chars
	// are the update type + file type; guard on those so a stray log line that
	// merely starts with 11 non-space chars isn't misread.
	if len(line) < 13 || line[11] != ' ' {
		return "", "", false
	}
	flags = line[:11]
	if !isUpdateType(flags[0]) || !isFileType(flags[1]) {
		return "", "", false
	}
	return flags, line[12:], true
}

func isUpdateType(c byte) bool {
	// rsync update types: transfer (< >), change/create (c), hardlink (h),
	// no-change (.). '*' is handled separately as a message line.
	switch c {
	case '<', '>', 'c', 'h', '.':
		return true
	}
	return false
}

func isFileType(c byte) bool {
	switch c {
	case 'f', 'd', 'L', 'D', 'S':
		return true
	}
	return false
}

// attrsAllNew reports whether every attribute slot (chars 3–11) is '+', which
// is rsync's marker for a freshly created item — i.e. the path did not exist on
// the remote. Any other flag ('.', or a letter like s/t/p) means the item is
// being updated in place.
func attrsAllNew(flags string) bool {
	for i := 2; i < len(flags); i++ {
		if flags[i] != '+' {
			return false
		}
	}
	return true
}

// cleanPath drops a trailing slash so a directory and its listing read the same
// in the summary (rsync prints "assets/" for the dir but "assets/app.js" for
// the file).
func cleanPath(p string) string {
	return strings.TrimRight(p, "/")
}

// WriteSummary prints a human dry-run report for one host: the counts, then the
// paths grouped by bucket. It is written to w so the caller controls the stream
// (stdout in practice).
func WriteSummary(w io.Writer, host string, s Summary) {
	if s.Total() == 0 {
		fmt.Fprintf(w, "   ✓ up to date — nothing to change\n")
		return
	}
	fmt.Fprintf(w, "   dry run — %d added, %d replaced, %d deleted:\n",
		len(s.Added), len(s.Replaced), len(s.Deleted))
	writeBucket(w, "＋ add", s.Added)
	writeBucket(w, "~ replace", s.Replaced)
	writeBucket(w, "－ delete", s.Deleted)
	fmt.Fprintf(w, "   (dry run — nothing was changed)\n")
}

func writeBucket(w io.Writer, label string, paths []string) {
	for _, p := range paths {
		fmt.Fprintf(w, "     %s  %s\n", label, p)
	}
}
