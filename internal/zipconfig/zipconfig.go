// Package zipconfig loads the two config files that drive `zipgo deploy` and
// the remote-management subcommands, so callers don't have to repeat the SSH
// target or the per-site source folders on every invocation.
//
//   - The ROOT config is a `.zipgo.json` file found by ascending the directory
//     tree from the current working directory (like git or .npmrc). It holds the
//     default deploy target (and optional named targets):
//
//     { "target": "user@host:/base/domains", "targets": { "raspy2": "..." } }
//
//   - The PROJECT config lives under a `"zipgo"` key inside a project's
//     package.json (also found by ascending). It maps each fully-qualified host
//     to the local folder that should be deployed to it, plus an optional target
//     override:
//
//     { "zipgo": { "deploy": { "app.dev.gabvdl.xyz": "dist" }, "target": "raspy2" } }
package zipconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// RootConfigName is the filename of the root config, discovered by ascending
// the directory tree.
const RootConfigName = ".zipgo.json"

// RootConfig is the parsed `.zipgo.json`.
type RootConfig struct {
	// Target is the default deploy destination (the default --ssh value):
	// user@host:/base/path.
	Target string `json:"target"`
	// Targets are optional named destinations a project can reference by name
	// (via its own "target"). Values are user@host:/base/path specs.
	Targets map[string]string `json:"targets,omitempty"`
}

// ProjectConfig is the `"zipgo"` object inside a project's package.json.
type ProjectConfig struct {
	// Deploy maps a fully-qualified host (e.g. app.dev.gabvdl.xyz) to the local
	// directory whose contents are synced to it.
	Deploy map[string]string `json:"deploy,omitempty"`
	// Target optionally overrides the root target for this project. It may be a
	// raw user@host:/base spec or the name of a root `targets` entry.
	Target string `json:"target,omitempty"`
}

// packageJSON is the slice of package.json we care about.
type packageJSON struct {
	Zipgo *ProjectConfig `json:"zipgo"`
}

// FindRoot ascends from startDir looking for a `.zipgo.json` file. It returns
// the file path, the parsed config, and found=false (with a zero config and no
// error) when no `.zipgo.json` exists in any ancestor.
func FindRoot(startDir string) (path string, cfg RootConfig, found bool, err error) {
	p, ok := ascend(startDir, RootConfigName)
	if !ok {
		return "", RootConfig{}, false, nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return p, RootConfig{}, true, fmt.Errorf("reading %s: %w", p, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return p, RootConfig{}, true, fmt.Errorf("parsing %s: %w", p, err)
	}
	return p, cfg, true, nil
}

// FindProject ascends from startDir looking for a package.json with a `"zipgo"`
// key. It returns the directory containing that package.json, the parsed
// project config, and found=false when no package.json with a zipgo section is
// found. A package.json without a "zipgo" key is treated as not found, so the
// search continues upward (allowing a nested package.json without zipgo config
// to defer to an ancestor that has one).
func FindProject(startDir string) (dir string, cfg ProjectConfig, found bool, err error) {
	d, err := filepath.Abs(startDir)
	if err != nil {
		return "", ProjectConfig{}, false, err
	}
	for {
		pkg := filepath.Join(d, "package.json")
		if data, readErr := os.ReadFile(pkg); readErr == nil {
			var parsed packageJSON
			if jsonErr := json.Unmarshal(data, &parsed); jsonErr != nil {
				return d, ProjectConfig{}, true, fmt.Errorf("parsing %s: %w", pkg, jsonErr)
			}
			if parsed.Zipgo != nil {
				return d, *parsed.Zipgo, true, nil
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", ProjectConfig{}, false, nil
		}
		d = parent
	}
}

// ascend walks from startDir up to the filesystem root, returning the path of
// the first ancestor (inclusive) that directly contains a file named name.
func ascend(startDir, name string) (string, bool) {
	d, err := filepath.Abs(startDir)
	if err != nil {
		return "", false
	}
	for {
		candidate := filepath.Join(d, name)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
		}
		d = parent
	}
}
