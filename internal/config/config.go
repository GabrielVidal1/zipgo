package config

import (
	"fmt"
	"os"
	"strings"
)

// ReadDomains scans domainsDir for subdirectory names and returns them as
// domain names.  An empty slice means localhost mode.  Returns nil (not an
// error) if the directory doesn't exist yet.
func ReadDomains(domainsDir string) ([]string, error) {
	entries, err := os.ReadDir(domainsDir)
	if os.IsNotExist(err) {
		return nil, nil // empty = localhost mode
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", domainsDir, err)
	}

	var domains []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		name := e.Name()
		if !strings.Contains(name, ".") {
			return nil, fmt.Errorf("domain folder %q looks invalid (must contain a dot)", name)
		}
		domains = append(domains, name)
	}
	return domains, nil
}
