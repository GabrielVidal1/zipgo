package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ReadRootDomain(appsDir string) (string, error) {
	path := filepath.Join(appsDir, "root.txt")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil // empty string = localhost mode
	}
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}

	domain := strings.TrimSpace(string(raw))
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimPrefix(domain, "http://")
	domain = strings.TrimSuffix(domain, "/")

	if domain == "" {
		return "", nil // empty string = localhost mode
	}
	if !strings.Contains(domain, ".") {
		return "", fmt.Errorf("domain %q looks invalid", domain)
	}
	return domain, nil
}
