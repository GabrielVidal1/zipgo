package sites

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Site struct {
	// Labels is the subdomain label chain, leaf-first. Empty means the apex
	// domain. e.g. ["api", "docs"] → api.docs.<rootDomain>. A single label may
	// itself contain dots (folder "foo.bar." → label "foo.bar").
	Labels []string
	Path   string
	IsSPA  bool
}

// IsApex reports whether this site is served at the bare domain.
func (s Site) IsApex() bool { return len(s.Labels) == 0 }

// Host returns the fully-qualified host for this site under rootDomain.
func (s Site) Host(rootDomain string) string {
	if len(s.Labels) == 0 {
		return rootDomain
	}
	return strings.Join(s.Labels, ".") + "." + rootDomain
}

// LocalhostPath returns the path prefix used in localhost mode, parent-first:
// /<domain>/<outer>/.../<leaf>. The apex maps to /<domain>.
func (s Site) LocalhostPath(domain string) string {
	p := "/" + domain
	for i := len(s.Labels) - 1; i >= 0; i-- {
		p += "/" + s.Labels[i]
	}
	return p
}

// Discover walks a single domain directory. The directory root itself is the
// apex site; any subdirectory whose name ends in a dot is a subdomain whose
// label is the name with the trailing dot trimmed. This applies recursively, so
// docs./api./ becomes api.docs.<rootDomain>.
func Discover(domainDir string) ([]Site, error) {
	if _, err := os.Stat(domainDir); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("domain directory %q not found", domainDir)
		}
		return nil, fmt.Errorf("reading %s: %w", domainDir, err)
	}

	var result []Site
	var walk func(dir string, labels []string) error
	walk = func(dir string, labels []string) error {
		result = append(result, Site{
			Labels: append([]string{}, labels...),
			Path:   dir,
			IsSPA:  detectSPA(dir),
		})

		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("reading %s: %w", dir, err)
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if !strings.HasSuffix(e.Name(), ".") {
				continue
			}
			label := strings.TrimSuffix(e.Name(), ".")
			if label == "" {
				continue
			}
			child := filepath.Join(dir, e.Name())
			if err := walk(child, append([]string{label}, labels...)); err != nil {
				return err
			}
		}
		return nil
	}

	if err := walk(domainDir, nil); err != nil {
		return nil, err
	}
	return result, nil
}

// detectSPA returns true when the dir has index.html + a bundler output dir.
// Covers Vite (assets/), CRA (static/), Next.js (_next/), generic (dist/).
func detectSPA(dir string) bool {
	bundleDirs := map[string]bool{"static": true, "assets": true, "_next": true, "dist": true}
	hasIndex, hasBundleDir := false, false
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if !e.IsDir() && name == "index.html" {
			hasIndex = true
		}
		if e.IsDir() && bundleDirs[name] {
			hasBundleDir = true
		}
	}
	return hasIndex && hasBundleDir
}
