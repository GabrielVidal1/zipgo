package admin

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"zipgo/internal/sites"
)

// Version is one past deploy kept under a site's .zipgo-versions folder — what
// `zipgo deploy` snapshots before it overwrites a site, and what a rollback
// swaps back in.
type Version struct {
	// Name is the snapshot folder's name, a UTC stamp ("20261029T140200Z",
	// "-1" appended for a same-second retry) of when it was replaced. It is
	// what a rollback names.
	Name string `json:"name"`
	// At is when that version was deployed: its index.html's time, the same
	// clock as a Site's Modified.
	At    time.Time `json:"at"`
	Bytes int64     `json:"bytes"`
	Files int       `json:"files"`
}

// stampLayout is the snapshot folder name format, shared with the deploy
// snapshot script (`date -u '+%Y%m%dT%H%M%SZ'`).
const stampLayout = "20060102T150405Z"

// Versions lists the deploys kept for the site at id, newest first. A site
// with no history yet has none (not an error).
func Versions(root, id string) ([]Version, error) {
	dir, err := resolve(root, id)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(dir, sites.VersionsDirName))
	if os.IsNotExist(err) {
		return []Version{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Version{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		v := Version{Name: e.Name()}
		if info, err := os.Stat(filepath.Join(dir, sites.VersionsDirName, v.Name, "index.html")); err == nil {
			v.At = info.ModTime().UTC()
		} else if len(v.Name) >= len(stampLayout) {
			v.At, _ = time.Parse(stampLayout, v.Name[:len(stampLayout)])
		}
		_ = filepath.WalkDir(filepath.Join(dir, sites.VersionsDirName, v.Name), func(_ string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
				v.Bytes += info.Size()
				v.Files++
			}
			return nil
		})
		out = append(out, v)
	}
	// The stamp sorts chronologically as a string.
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

// rollbackMu serialises rollbacks: two at once on one site would interleave
// their copies.
var rollbackMu sync.Mutex

// Rollback swaps the kept deploy version back into the live folder of the site
// at id — what `zipgo rollback <host> <version>` does over SSH, done here on
// the server's own disk. What is live is snapshotted first (backup is that
// snapshot's name, "" when there was nothing to keep), so the rollback can be
// undone by rolling back to it.
//
// Like a deploy it ships content, never server state: the site's
// .zipgoconfig.json (switch, lock, flags), the history folder and nested
// subdomain sites are neither copied nor deleted. Files are written through
// tmpDir and renamed into place, so a visitor never gets half a file.
func Rollback(root, tmpDir, self, id, version string) (backup string, err error) {
	dir, err := resolve(root, id)
	if err != nil {
		return "", err
	}
	if self != "" && sameDir(dir, self) {
		return "", fmt.Errorf("the back-office can't roll itself back")
	}
	if version == "" || strings.ContainsAny(version, `/\`) || strings.HasPrefix(version, ".") {
		return "", fmt.Errorf("bad version %q", version)
	}
	vdir := filepath.Join(dir, sites.VersionsDirName)
	src := filepath.Join(vdir, version)
	if fi, err := os.Lstat(src); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("no version %q kept for this site", version)
	}

	rollbackMu.Lock()
	defer rollbackMu.Unlock()

	if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
		backup = newStamp(vdir, time.Now())
		if err := copyTree(dir, filepath.Join(vdir, backup), tmpDir, dir); err != nil {
			os.RemoveAll(filepath.Join(vdir, backup))
			return "", fmt.Errorf("keeping the live version: %w", err)
		}
	}
	if err := copyTree(src, dir, tmpDir, dir); err != nil {
		return backup, err
	}
	if err := prune(dir, src); err != nil {
		return backup, err
	}
	forgetSizes(root)
	return backup, nil
}

// newStamp is a free snapshot name in vdir for t, the way the deploy snapshot
// names one: the UTC stamp, "-n" appended on a same-second clash.
func newStamp(vdir string, t time.Time) string {
	base := t.UTC().Format(stampLayout)
	name := base
	for n := 1; ; n++ {
		if _, err := os.Lstat(filepath.Join(vdir, name)); os.IsNotExist(err) {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, n)
	}
}

// skipContent reports whether rel (a "/"-separated path under a site folder)
// is outside the site's content: its config and history at the top, and any
// nested subdomain folder — the same boundary as the deploy mirror's excludes.
func skipContent(rel string, isDir bool) bool {
	if rel == sites.ConfigFileName || rel == sites.VersionsDirName {
		return true
	}
	return isDir && strings.HasSuffix(filepath.Base(rel), ".")
}

// copyTree copies the content of src over dst (creating dst), file by file
// through tmpDir. Symlinks are left out: a snapshot never needs one (deploys
// don't ship links that leave the site) and a copied link could point
// anywhere. Owner follows owner (see chownLike).
func copyTree(src, dst, tmpDir, owner string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return mkdirLike(dst, owner)
		}
		if skipContent(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		switch {
		case d.IsDir():
			if fi, err := os.Lstat(target); err == nil && !fi.IsDir() {
				if err := os.Remove(target); err != nil {
					return err
				}
			}
			return mkdirLike(target, owner)
		case d.Type().IsRegular():
			if fi, err := os.Lstat(target); err == nil && fi.IsDir() {
				if err := os.RemoveAll(target); err != nil {
					return err
				}
			}
			return copyFile(p, target, tmpDir, owner)
		}
		return nil
	})
}

func mkdirLike(dir, owner string) error {
	if _, err := os.Lstat(dir); err == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	chownLike(dir, owner)
	return nil
}

// copyFile writes a copy of src at dst via a temporary file in tmpDir, keeping
// its mtime as rsync -a does: a version keeps its deploy time (what the page
// shows as "Deployed"), and a restored file's ETag still changes since its
// size or time differ from what it replaces.
func copyFile(src, dst, tmpDir, owner string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	t, err := os.CreateTemp(tmpDir, ".zipgo-tmp.")
	if err != nil {
		return err
	}
	tmp := t.Name()
	defer os.Remove(tmp) // a no-op once renamed
	if _, err := io.Copy(t, in); err != nil {
		t.Close()
		return err
	}
	if err := t.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, info.Mode().Perm()|0o444); err != nil {
		return err
	}
	if err := os.Chtimes(tmp, time.Now(), info.ModTime()); err != nil {
		return err
	}
	chownLike(tmp, owner)
	return os.Rename(tmp, dst)
}

// prune removes from dir what the snapshot src doesn't have — the --delete
// half of the mirror — leaving out the same config, history and subdomain
// folders copyTree leaves alone.
func prune(dir, src string) error {
	var gone []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if skipContent(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		fi, err := os.Lstat(filepath.Join(src, filepath.FromSlash(rel)))
		if err != nil || fi.IsDir() != d.IsDir() || (!d.IsDir() && !fi.Mode().IsRegular()) {
			gone = append(gone, p)
			if d.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, p := range gone {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}

// forgetSizes drops root's cached sizes: they changed.
func forgetSizes(root string) {
	sizesCache.Lock()
	delete(sizesCache.byRoot, root)
	sizesCache.Unlock()
}
