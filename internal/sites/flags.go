package sites

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Flag is one feature flag of a site's "flags" block: a switch the site's own
// code reads at runtime (from /__zipgo/flags.json) to show or hide a feature
// that isn't ready for everyone. It is written either as a bare boolean or as
// an object:
//
//	"flags": {
//	  "daily": true,
//	  "coop":  {"on": false, "description": "Co-op levels", "paths": ["/docs/61-coop-puzzles.html"]}
//	}
//
// A flag that is off also makes zipgo answer 404 on its Paths, so a feature
// that lives on pages of its own can't be reached by URL either. An object
// without "on" is off: a feature is unreleased until someone flips it.
type Flag struct {
	On bool `json:"on"`
	// Description is what the back-office shows next to the switch.
	Description string `json:"description,omitempty"`
	// Paths are the site paths that belong to the feature. Each is a prefix:
	// "/arena" covers /arena, /arena/ and everything under it.
	Paths []string `json:"paths,omitempty"`
}

// UnmarshalJSON accepts the boolean shorthand as well as the object.
func (f *Flag) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && (b[0] == 't' || b[0] == 'f') {
		var on bool
		if err := json.Unmarshal(b, &on); err != nil {
			return err
		}
		*f = Flag{On: on}
		return nil
	}
	type plain Flag
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return fmt.Errorf("a flag is true, false or {\"on\", \"description\", \"paths\"}: %w", err)
	}
	*f = Flag(p)
	return nil
}

// FlagsPath is where a site with flags serves their current values, under its
// own path prefix in localhost mode. It sits beside the login page's endpoints
// (and behind the login page, on a locked site).
const FlagsPath = "/__zipgo/flags.json"

// flagName is what a flag may be called: something every language can use as
// an identifier or a map key without quoting surprises.
var flagName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

// InvalidFlagName returns a human reason when name can't be a flag's name, or
// "" when it is fine.
func InvalidFlagName(name string) string {
	if !flagName.MatchString(name) {
		return "a flag name is a letter followed by up to 63 letters, digits, - or _"
	}
	return ""
}

// NormalizeFlagPath cleans a flag path into the prefix the builder matches: a
// leading slash, no trailing one ("arena/" → "/arena"). "" or "/" is the whole
// site, which a flag can't take down — that is what "enable" is for.
func NormalizeFlagPath(p string) string {
	p = strings.TrimSpace(p)
	if p != "" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	for len(p) > 1 && strings.HasSuffix(p, "/") {
		p = strings.TrimSuffix(p, "/")
	}
	if p == "/" {
		return ""
	}
	return p
}

// InvalidFlagPath returns a human reason when p can't be one of a flag's
// paths, or "" when it is fine.
func InvalidFlagPath(p string) string {
	n := NormalizeFlagPath(p)
	switch {
	case n == "":
		return "a flag can't cover the whole site — switch the site off instead"
	case strings.ContainsAny(n, "*?#%\\"):
		return "a flag path is a plain path prefix (no *, ?, #, % or \\)"
	case strings.Contains("/"+n+"/", "/../") || strings.Contains("/"+n+"/", "/./"):
		return "a flag path can't contain . or .. segments"
	}
	for _, r := range n {
		if r <= ' ' || r == 0x7f {
			return "a flag path can't contain spaces or control characters"
		}
	}
	return ""
}

// ValidateFlags checks every flag's name and paths, returning the first
// problem found (in name order, so the error is stable).
func ValidateFlags(flags map[string]Flag) error {
	for _, name := range FlagNames(flags) {
		if bad := InvalidFlagName(name); bad != "" {
			return fmt.Errorf("flag %q: %s", name, bad)
		}
		for _, p := range flags[name].Paths {
			if bad := InvalidFlagPath(p); bad != "" {
				return fmt.Errorf("flag %q path %q: %s", name, p, bad)
			}
		}
	}
	return nil
}

// FlagNames returns the flags' names, sorted.
func FlagNames(flags map[string]Flag) []string {
	names := make([]string, 0, len(flags))
	for name := range flags {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// HasFlags reports whether the site has a "flags" block (even an empty one):
// such a site serves FlagsPath.
func (c Config) HasFlags() bool { return c.Flags != nil }

// FlagValues is what FlagsPath answers: every flag's name and whether it is on.
func (c Config) FlagValues() map[string]bool {
	out := make(map[string]bool, len(c.Flags))
	for name, f := range c.Flags {
		out[name] = f.On
	}
	return out
}
