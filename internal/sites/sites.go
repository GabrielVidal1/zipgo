package sites

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Site struct {
	Name  string // subdirectory name = subdomain ("root" → apex domain)
	Path  string
	IsSPA bool
}

func (s Site) Host(rootDomain string) string {
	if s.Name == "root" {
		return rootDomain
	}
	return s.Name + "." + rootDomain
}

func Discover(appsDir string) ([]Site, error) {
	entries, err := os.ReadDir(appsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("apps directory %q not found", appsDir)
		}
		return nil, fmt.Errorf("reading %s: %w", appsDir, err)
	}

	var result []Site
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(appsDir, e.Name())
		result = append(result, Site{
			Name:  e.Name(),
			Path:  path,
			IsSPA: detectSPA(path),
		})
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
