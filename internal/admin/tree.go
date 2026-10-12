package admin

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"zipgo/internal/deploy"
	"zipgo/internal/sites"
)

// Site is one row of the back-office: a site folder under the managed root,
// described the way the builder routes it.
type Site struct {
	// ID is the folder's path relative to the managed root, "/"-separated
	// ("dev./card-editor."); "" is the root itself. It is what a switch names,
	// rather than the host: a host can be spelled by more than one folder tree
	// ("a.b." or "b./a."), a folder can't.
	ID string `json:"id"`
	// Parent is the ID of the site folder this one is nested in, nil for the
	// root.
	Parent *string `json:"parent"`
	Host   string  `json:"host"`
	// Label is the folder's own label ("card-editor"), the root's is its host.
	Label string `json:"label"`
	// Type is static, spa, proxy, redirect, admin, or folder for one that has
	// nothing of its own to serve and only holds subdomains.
	Type string `json:"type"`
	// Enabled is the site's own switch; Off is whether it is actually off —
	// by its own switch or, through OffBy (that parent's host), a parent's.
	Enabled bool   `json:"enabled"`
	Off     bool   `json:"off"`
	OffBy   string `json:"offBy,omitempty"`
	// Locked: the site has a password or basic auth; Password: it has zipgo's
	// login page (the lock this page can set and remove — basic auth is
	// edited by hand).
	Locked   bool `json:"locked"`
	Password bool `json:"password"`
	// ConfigError is set when the folder's .zipgoconfig.json can't be read.
	// Such a site can't be switched from here: rewriting a config the server
	// rejects would hide the mistake, not fix it.
	ConfigError string `json:"configError,omitempty"`
	// Modified is when the site's index.html (or, without one, its config or
	// folder) last changed — what a deploy touches.
	Modified *time.Time `json:"modified,omitempty"`
	// Self marks the back-office's own folder. It is never switched from here:
	// that would lock the operator out of the page they are using.
	Self bool `json:"self,omitempty"`
}

// List walks the managed root and returns every site folder under it,
// parent-first, siblings sorted by label. Unlike sites.Discover it does not
// stop at a malformed config: that site is reported with ConfigError and the
// walk goes on, so the page can still list (and switch) everything else.
func List(root, rootHost, self string) ([]Site, error) {
	if _, err := os.Stat(root); err != nil {
		return nil, err
	}
	var out []Site
	var walk func(dir, id, host, label string, parent *string, offBy string)
	walk = func(dir, id, host, label string, parent *string, offBy string) {
		s := Site{ID: id, Parent: parent, Host: host, Label: label, Enabled: true}
		cfg, err := readConfig(dir)
		if err != nil {
			s.ConfigError = err.Error()
		} else {
			s.Enabled = cfg.Enabled()
			s.Locked = cfg.Protected()
			s.Password = cfg.HasPassword()
		}
		s.Self = self != "" && sameDir(dir, self)
		s.Type = siteType(dir, cfg)
		s.Modified = modified(dir)
		// An admin folder stays up whatever its parents say (see
		// sites.Discover): switching the domain off must not take its own
		// switchboard with it.
		switch {
		case offBy != "" && !cfg.Admin:
			s.Off, s.OffBy = true, offBy
		case !s.Enabled:
			s.Off = true
		}
		out = append(out, s)

		childOff := s.OffBy
		if childOff == "" && !s.Enabled {
			childOff = host
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		var labels []string
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || !strings.HasSuffix(e.Name(), ".") {
				continue
			}
			labels = append(labels, e.Name())
		}
		sort.Strings(labels)
		for _, name := range labels {
			l := strings.TrimSuffix(name, ".")
			pid := id
			walk(filepath.Join(dir, name), path.Join(id, name), l+"."+host, l, &pid, childOff)
		}
	}
	walk(root, "", rootHost, rootHost, nil, "")
	return out, nil
}

// readConfig reads a folder's .zipgoconfig.json the way the server does; a
// missing file is the zero value.
func readConfig(dir string) (sites.Config, error) {
	raw, err := os.ReadFile(filepath.Join(dir, sites.ConfigFileName))
	if os.IsNotExist(err) {
		return sites.Config{}, nil
	}
	if err != nil {
		return sites.Config{}, err
	}
	return sites.ParseConfig(raw)
}

// siteType mirrors the builder's precedence: an admin page, then a redirect,
// then a proxy, then the folder's own files.
func siteType(dir string, cfg sites.Config) string {
	switch {
	case cfg.Admin:
		return "admin"
	case cfg.Redirect != "":
		return "redirect"
	case cfg.Rewrite != "":
		return "proxy"
	case sites.DetectSPA(dir):
		return "spa"
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
		return "static"
	}
	return "folder"
}

// modified is the time a deploy last touched the site.
func modified(dir string) *time.Time {
	for _, name := range []string{"index.html", sites.ConfigFileName, ""} {
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t := fi.ModTime().UTC().Truncate(time.Second)
			return &t
		}
	}
	return nil
}

func sameDir(a, b string) bool {
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}

// switchMu serialises switches: two taps on the same site, or on a parent and
// its child, would otherwise race on the read-modify-write of a config.
var switchMu sync.Mutex

// Switch turns the site at id (see Site.ID) off or back on by editing the
// "enable" key of its .zipgoconfig.json — exactly what `zipgo off`/`zipgo on`
// write, so the page, the CLI and `zipgo ls` never disagree. The new file is
// written in tmpDir (the domains folder: same filesystem, outside every site,
// so a half-written config is never served nor reloaded) and renamed into
// place. changed is false when the site was already in that state.
func Switch(root, tmpDir, self, id string, on bool) (changed bool, err error) {
	dir, err := resolve(root, id)
	if err != nil {
		return false, err
	}
	if self != "" && sameDir(dir, self) {
		return false, fmt.Errorf("the back-office can't switch itself off")
	}
	switchMu.Lock()
	defer switchMu.Unlock()

	file := filepath.Join(dir, sites.ConfigFileName)
	raw, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	out, changed, err := deploy.SetEnable(raw, on)
	if err != nil {
		return false, fmt.Errorf("%s: %w — fix it by hand first", sites.ConfigFileName, err)
	}
	if !changed {
		return false, nil
	}
	if out == nil {
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			return false, err
		}
		return true, nil
	}
	return true, writeFile(dir, file, tmpDir, out)
}

// resolve maps an ID back to its folder, refusing anything that isn't a chain
// of real subdomain folders under root: no "..", no hidden folder, no symlink
// out of the tree.
func resolve(root, id string) (string, error) {
	dir := root
	if id == "" {
		return dir, nil
	}
	if path.Clean(id) != id || strings.HasPrefix(id, "/") {
		return "", fmt.Errorf("bad site id %q", id)
	}
	for _, part := range strings.Split(id, "/") {
		if part == "." || part == ".." || strings.HasPrefix(part, ".") || !strings.HasSuffix(part, ".") {
			return "", fmt.Errorf("bad site id %q", id)
		}
		dir = filepath.Join(dir, part)
		fi, err := os.Lstat(dir)
		if err != nil {
			return "", fmt.Errorf("no site %q", id)
		}
		if !fi.IsDir() {
			return "", fmt.Errorf("no site %q", id)
		}
	}
	return dir, nil
}

// writeFile writes content to file via a temporary file in tmpDir. When zipgo
// runs as root (the Docker image) the file is handed to the folder's owner, so
// the operator's own `zipgo off`/deploys over SSH can still replace it.
func writeFile(dir, file, tmpDir string, content []byte) error {
	t, err := os.CreateTemp(tmpDir, ".zipgo-tmp.")
	if err != nil {
		return err
	}
	tmp := t.Name()
	defer os.Remove(tmp) // a no-op once renamed
	if _, err := t.Write(content); err != nil {
		t.Close()
		return err
	}
	if err := t.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return err
	}
	chownLike(tmp, dir)
	return os.Rename(tmp, file)
}
