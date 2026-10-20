package deploy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"zipgo/internal/sites"
)

// The edits below are the only way a "flags" block changes: the back-office,
// `zipgo flag` and a deploy all go through them, so they keep the file's key
// order and refuse a config the server would reject, like SetEnable.

// FlagSpec is what DeclareFlag sets on a flag. A nil field is left as it is
// (or at its default, for a new flag).
type FlagSpec struct {
	On          *bool
	Description *string
	Paths       []string // nil = keep; empty = none
}

// SetFlag returns raw with the flag called name switched on or off, every
// other key left as it was. A flag written as a bare boolean stays one; an
// object gets its "on" key set in place. changed is false when the flag
// already says so; an unknown flag is an error (DeclareFlag creates one).
func SetFlag(raw []byte, name string, on bool) (out []byte, changed bool, err error) {
	cfg, ms, err := parseConfig(raw)
	if err != nil {
		return nil, false, err
	}
	f, ok := cfg.Flags[name]
	if !ok {
		return nil, false, fmt.Errorf("no flag %q", name)
	}
	if f.On == on {
		return raw, false, nil
	}
	out, err = editFlags(ms, func(fs []member) ([]member, error) {
		for i := range fs {
			if fs[i].key != name {
				continue
			}
			v, err := withOn(fs[i].val, on)
			if err != nil {
				return nil, err
			}
			fs[i].val = v
		}
		return fs, nil
	})
	return out, err == nil, err
}

// DeclareFlag returns raw with the flag called name created or updated from
// spec. A new flag is off unless spec says otherwise; an existing one keeps
// its switch, description and paths where spec leaves them nil. It is written
// as a bare boolean while it has nothing but a switch.
func DeclareFlag(raw []byte, name string, spec FlagSpec) (out []byte, changed bool, err error) {
	if bad := sites.InvalidFlagName(name); bad != "" {
		return nil, false, fmt.Errorf("flag %q: %s", name, bad)
	}
	for _, p := range spec.Paths {
		if bad := sites.InvalidFlagPath(p); bad != "" {
			return nil, false, fmt.Errorf("flag %q path %q: %s", name, p, bad)
		}
	}
	cfg, ms, err := parseConfig(raw)
	if err != nil {
		return nil, false, err
	}
	f, existed := cfg.Flags[name]
	if spec.On != nil {
		f.On = *spec.On
	}
	if spec.Description != nil {
		f.Description = *spec.Description
	}
	if spec.Paths != nil {
		f.Paths = nil
		for _, p := range spec.Paths {
			f.Paths = append(f.Paths, sites.NormalizeFlagPath(p))
		}
	}
	if existed && sameFlag(f, cfg.Flags[name]) {
		return raw, false, nil
	}
	val, err := flagJSON(f)
	if err != nil {
		return nil, false, err
	}
	out, err = editFlags(ms, func(fs []member) ([]member, error) {
		for i := range fs {
			if fs[i].key == name {
				fs[i].val = val
				return fs, nil
			}
		}
		return append(fs, member{key: name, val: val}), nil
	})
	return out, err == nil, err
}

// RemoveFlag returns raw without the flag called name. The "flags" block
// itself stays, even empty: the site keeps serving /__zipgo/flags.json, which
// its code may still fetch.
func RemoveFlag(raw []byte, name string) (out []byte, changed bool, err error) {
	cfg, ms, err := parseConfig(raw)
	if err != nil {
		return nil, false, err
	}
	if _, ok := cfg.Flags[name]; !ok {
		return raw, false, nil
	}
	out, err = editFlags(ms, func(fs []member) ([]member, error) {
		kept := fs[:0]
		for _, m := range fs {
			if m.key != name {
				kept = append(kept, m)
			}
		}
		return kept, nil
	})
	return out, err == nil, err
}

// MergeFlags is how a deploy lands a build's "flags" block (local) on a
// server that already has one (remote): the build declares, the server
// decides. Every flag keeps the server's switch; a flag the build declares as
// an object brings its description and paths (the build knows where its
// feature lives), one it declares as a bare boolean leaves the server's entry
// alone, a flag new to the server arrives as the build ships it (off, unless
// the build says on), and one only the server has — added from the
// back-office — is kept. Keys keep the build's order, server-only flags
// follow.
func MergeFlags(local, remote json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(remote)) == 0 || string(remote) == "null" {
		return local, nil
	}
	rs, err := parseObject(remote)
	if err != nil {
		return nil, fmt.Errorf("the server's flags: %w", err)
	}
	var ls []member
	if len(bytes.TrimSpace(local)) > 0 && string(local) != "null" {
		if ls, err = parseObject(local); err != nil {
			return nil, fmt.Errorf("the build's flags: %w", err)
		}
	}
	server := map[string]json.RawMessage{}
	for _, m := range rs {
		server[m.key] = m.val
	}
	out := make([]member, 0, len(ls)+len(rs))
	seen := map[string]bool{}
	for _, m := range ls {
		if seen[m.key] {
			continue
		}
		seen[m.key] = true
		sv, ok := server[m.key]
		if !ok {
			out = append(out, m)
			continue
		}
		if isBool(m.val) {
			out = append(out, member{key: m.key, val: sv})
			continue
		}
		var sf sites.Flag
		if err := json.Unmarshal(sv, &sf); err != nil {
			return nil, fmt.Errorf("the server's flag %q: %w", m.key, err)
		}
		v, err := withOn(m.val, sf.On)
		if err != nil {
			return nil, fmt.Errorf("the build's flag %q: %w", m.key, err)
		}
		out = append(out, member{key: m.key, val: v})
	}
	for _, m := range rs {
		if !seen[m.key] {
			seen[m.key] = true
			out = append(out, m)
		}
	}
	return compactObject(out)
}

// FlagSummary lists flags as "coop off, daily on", by name.
func FlagSummary(flags map[string]sites.Flag) string {
	parts := make([]string, 0, len(flags))
	for _, name := range sites.FlagNames(flags) {
		state := "off"
		if flags[name].On {
			state = "on"
		}
		parts = append(parts, name+" "+state)
	}
	return strings.Join(parts, ", ")
}

// parseConfig parses raw (possibly empty) both ways: as the server reads it,
// and as members in file order for the rewrite.
func parseConfig(raw []byte) (sites.Config, []member, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return sites.Config{}, nil, nil
	}
	cfg, err := sites.ParseConfig(raw)
	if err != nil {
		return sites.Config{}, nil, err
	}
	ms, err := parseObject(raw)
	return cfg, ms, err
}

// editFlags applies edit to the members of the config's "flags" block (an
// empty one when it has none, appended as the last key) and returns the whole
// config.
func editFlags(ms []member, edit func([]member) ([]member, error)) ([]byte, error) {
	idx := -1
	var fs []member
	for i, m := range ms {
		if m.key == "flags" {
			idx = i
			var err error
			if fs, err = parseObject(m.val); err != nil {
				return nil, fmt.Errorf("its \"flags\" block: %w", err)
			}
		}
	}
	fs, err := edit(fs)
	if err != nil {
		return nil, err
	}
	val, err := compactObject(fs)
	if err != nil {
		return nil, err
	}
	if idx < 0 {
		ms = append(ms, member{key: "flags", val: val})
	} else {
		ms[idx].val = val
	}
	return marshalObject(ms)
}

// withOn returns a flag's JSON (a boolean or an object) switched to on, in
// the same shape: an object gets its "on" key set in place, or first.
func withOn(val json.RawMessage, on bool) (json.RawMessage, error) {
	b := json.RawMessage("false")
	if on {
		b = json.RawMessage("true")
	}
	if isBool(val) {
		return b, nil
	}
	ms, err := parseObject(val)
	if err != nil {
		return nil, err
	}
	placed := false
	for i := range ms {
		if ms[i].key == "on" {
			ms[i].val, placed = b, true
		}
	}
	if !placed {
		ms = append([]member{{key: "on", val: b}}, ms...)
	}
	return compactObject(ms)
}

// flagJSON writes a flag the shortest way it can be read back.
func flagJSON(f sites.Flag) (json.RawMessage, error) {
	if f.Description == "" && len(f.Paths) == 0 {
		return json.Marshal(f.On)
	}
	return json.Marshal(f)
}

func sameFlag(a, b sites.Flag) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

func isBool(v json.RawMessage) bool {
	s := string(bytes.TrimSpace(v))
	return s == "true" || s == "false"
}

// compactObject is marshalObject without the indentation, for an object nested
// in a config (marshalObject indents the whole file in the end).
func compactObject(ms []member) (json.RawMessage, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range ms {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(m.key)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(m.val)
	}
	b.WriteByte('}')
	var out bytes.Buffer
	if err := json.Compact(&out, b.Bytes()); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
