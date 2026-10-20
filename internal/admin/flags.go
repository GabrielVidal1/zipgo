package admin

import (
	"fmt"
	"os"
	"path/filepath"

	"zipgo/internal/deploy"
	"zipgo/internal/sites"
)

// Flag is one feature flag of a site, as the page lists it.
type Flag struct {
	Name        string   `json:"name"`
	On          bool     `json:"on"`
	Description string   `json:"description,omitempty"`
	Paths       []string `json:"paths,omitempty"`
}

// flagRows lists a config's flags by name.
func flagRows(cfg sites.Config) []Flag {
	if !cfg.HasFlags() {
		return nil
	}
	out := []Flag{}
	for _, name := range sites.FlagNames(cfg.Flags) {
		f := cfg.Flags[name]
		out = append(out, Flag{Name: name, On: f.On, Description: f.Description, Paths: f.Paths})
	}
	return out
}

// SetFlag switches the flag called name of the site at id on or off by
// editing its .zipgoconfig.json — the same edit as `zipgo flag on|off`
// (deploy.SetFlag) — written like Switch writes. changed is false when the
// flag already said so.
func SetFlag(root, tmpDir, self, id, name string, on bool) (changed bool, err error) {
	dir, err := resolve(root, id)
	if err != nil {
		return false, err
	}
	if self != "" && sameDir(dir, self) {
		return false, fmt.Errorf("the back-office has no flags")
	}
	switchMu.Lock()
	defer switchMu.Unlock()

	file := filepath.Join(dir, sites.ConfigFileName)
	raw, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	out, changed, err := deploy.SetFlag(raw, name, on)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	return true, writeFile(dir, file, tmpDir, out)
}
