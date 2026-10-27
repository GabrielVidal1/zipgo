package deploy

// switch.go implements `zipgo off` / `zipgo on`: switching a deployed site off —
// every request answers 410 Gone, nested subdomains included — or back on, by
// editing the "enable" key of its remote .zipgoconfig.json over SSH. It lives in
// deploy because a deploy has to honour the same switch: redeploying a site that
// is off keeps it off (see Run).

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"

	"zipgo/internal/sites"
)

// remoteSpec is the rsync destination for a folder on host; tests point it at a
// plain local path.
var remoteSpec = func(host, dir string) string { return host + ":" + dir }

// runRemote runs a shell script on host over ssh, feeding it stdin, and returns
// its stdout. It is a variable so tests can run the very same scripts against a
// local folder with `sh -c` instead of a real SSH hop.
var runRemote = func(host, script string, stdin []byte) (string, error) {
	cmd := exec.Command("ssh", host, script)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	return string(out), err
}

// shQuote wraps s for safe interpolation into a remote /bin/sh command line.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// folder is what the remote reports about one site folder.
type folder struct {
	Host   string
	Dir    string
	Exists bool
	Index  bool   // it holds an index.html
	Config []byte // raw .zipgoconfig.json; nil when the folder has none
	Own    sites.Config
	Err    error // the config is unreadable or malformed
}

// ownOff reports whether the folder's own config switches it off.
func (f folder) ownOff() bool { return f.Err == nil && !f.Own.Enabled() }

// hasContent mirrors the builder's rule: something to serve of its own (an
// index.html, a rewrite or a redirect). Without it, the site answers nothing —
// on or off — and only holds subdomains.
func (f folder) hasContent() bool {
	return f.Index || f.Own.Rewrite != "" || f.Own.Redirect != ""
}

// siteState is the on/off picture of one host on the remote: its own folder,
// the parent sites above it (outermost first) and, when asked for, the nested
// subdomain sites below it.
type siteState struct {
	Self    folder
	Parents []folder
	Nested  []folder
}

// offBy lists the parent sites that switch this one off through the cascade.
func (s siteState) offBy() []string {
	var hosts []string
	for _, p := range s.Parents {
		if p.ownOff() {
			hosts = append(hosts, p.Host)
		}
	}
	return hosts
}

// ParentDirs returns the site folders enclosing dir, outermost (the apex) first.
// Only folders that map back to a host count — the base itself does not.
func ParentDirs(baseDir, dir string) []string {
	var out []string
	for d := path.Dir(path.Clean(dir)); ; d = path.Dir(d) {
		if _, ok := HostFromRemote(baseDir, d); !ok {
			break
		}
		out = append([]string{d}, out...)
	}
	return out
}

// stateScript prints one tab-separated record per site folder — role (up, self,
// sub), dir, folder exists?, index.html?, config exists?, base64 config — for dir's parents,
// dir itself and, when nested is set, every trailing-dot folder under dir. With
// mkdir the folder tree is created first (what a deploy needs anyway), so the
// state costs no extra SSH round trip.
func stateScript(baseDir, dir string, mkdir, nested bool) string {
	var b strings.Builder
	if mkdir {
		b.WriteString("mkdir -p " + shQuote(dir) + " || exit 1\n")
	}
	b.WriteString(`show() { e=0; [ -d "$2" ] && e=1; ix=0; [ -f "$2/index.html" ] && ix=1; c=0; cf=; if [ -f "$2/` + sites.ConfigFileName + `" ]; then c=1; cf=$(base64 "$2/` + sites.ConfigFileName + `" | tr -d '\n'); fi; printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$1" "$2" "$e" "$ix" "$c" "$cf"; }` + "\n")
	for _, p := range ParentDirs(baseDir, dir) {
		b.WriteString("show up " + shQuote(p) + "\n")
	}
	b.WriteString("show self " + shQuote(dir) + "\n")
	if nested {
		b.WriteString("[ -d " + shQuote(dir) + " ] && find " + shQuote(dir) +
			` -mindepth 1 -maxdepth 6 -type d -name '*.' 2>/dev/null | sort | while IFS= read -r s; do show sub "$s"; done` + "\n")
	}
	b.WriteString("exit 0\n")
	return b.String()
}

// parseState turns stateScript's output into a siteState. Nested folders that
// are not site folders (a trailing-dot name inside ordinary content) are dropped
// the same way `zipgo ls` drops them.
func parseState(baseDir, out string) (siteState, error) {
	var st siteState
	seenSelf := false
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 6 {
			continue
		}
		host, ok := HostFromRemote(baseDir, f[1])
		if !ok {
			continue
		}
		fo := folder{Host: host, Dir: f[1], Exists: f[2] == "1", Index: f[3] == "1"}
		if f[4] == "1" {
			raw, err := base64.StdEncoding.DecodeString(f[5])
			if err != nil {
				fo.Err = fmt.Errorf("unreadable %s", sites.ConfigFileName)
			} else {
				fo.Config = raw
				if len(bytes.TrimSpace(raw)) > 0 {
					if fo.Own, err = sites.ParseConfig(raw); err != nil {
						fo.Err = fmt.Errorf("malformed %s: %v", sites.ConfigFileName, err)
					}
				}
			}
		}
		switch f[0] {
		case "up":
			st.Parents = append(st.Parents, fo)
		case "self":
			st.Self, seenSelf = fo, true
		case "sub":
			st.Nested = append(st.Nested, fo)
		}
	}
	if !seenSelf {
		return st, fmt.Errorf("no answer from the remote about the site folder")
	}
	return st, nil
}

// inspect reads the on/off state of host on the target (see stateScript).
func inspect(tgt Target, host string, mkdir, nested bool) (siteState, error) {
	dir, err := RemoteDir(tgt.BaseDir, host)
	if err != nil {
		return siteState{}, err
	}
	out, err := runRemote(tgt.Host, stateScript(tgt.BaseDir, dir, mkdir, nested), nil)
	if err != nil {
		return siteState{}, fmt.Errorf("reading %s on %s: %w", host, tgt.Host, err)
	}
	return parseState(tgt.BaseDir, out)
}

// WriteConfig is writeConfig for other packages (`zipgo password`).
func WriteConfig(tgt Target, dir string, content []byte) error { return writeConfig(tgt, dir, content) }

// ReadConfig returns the raw .zipgoconfig.json of host's folder on the target,
// and whether the file exists. A failed SSH round trip is an error, never "no
// file" — the caller would read that as "not locked".
func ReadConfig(tgt Target, host string) ([]byte, bool, error) {
	st, err := inspect(tgt, host, false, false)
	if err != nil {
		return nil, false, err
	}
	return st.Self.Config, st.Self.Config != nil, nil
}

// RunRemote runs a script on the target host and returns its stdout.
func RunRemote(host, script string) (string, error) { return runRemote(host, script, nil) }

// Quote wraps s for safe interpolation into a remote /bin/sh command line.
func Quote(s string) string { return shQuote(s) }

// writeConfig replaces dir's .zipgoconfig.json with content, or removes it when
// content is nil, creating dir if needed (a site can be locked before its first
// deploy). The new file is written in the domains root — the same filesystem,
// but outside every site, so the half-written file (a password hash, maybe) is
// never served — and renamed over the old one, so the server's watcher never
// reloads a half-written config (a malformed one aborts the whole reload).
func writeConfig(tgt Target, dir string, content []byte) error {
	file := shQuote(path.Join(dir, sites.ConfigFileName))
	script := "rm -f " + file
	if content != nil {
		script = `mkdir -p ` + shQuote(dir) + ` && t=$(mktemp ` + shQuote(path.Join(tgt.BaseDir, ".zipgo-tmp.XXXXXX")) + `) || exit 1; ` +
			`if cat > "$t" && chmod 644 "$t" && mv -f "$t" ` + file + `; then exit 0; fi; rm -f "$t"; exit 1`
	}
	if _, err := runRemote(tgt.Host, script, content); err != nil {
		return fmt.Errorf("writing %s/%s: %w", dir, sites.ConfigFileName, err)
	}
	return nil
}

// member is one key of a JSON object, kept in file order.
type member struct {
	key string
	val json.RawMessage
}

// parseObject decodes a JSON object into its members, in the order they appear,
// so rewriting a hand-edited config keeps its keys where its author put them.
func parseObject(raw []byte) ([]member, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("not a JSON object")
	}
	var ms []member
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		ms = append(ms, member{key: key, val: v})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected data after the JSON object")
	}
	return ms, nil
}

// marshalObject is parseObject's inverse, indented the way the README writes
// configs and ending in a newline.
func marshalObject(ms []member) ([]byte, error) {
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
	if err := json.Indent(&out, b.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// SetEnable returns raw (a .zipgoconfig.json body, possibly empty) with the
// site switched off or back on, every other key left as it was:
//   - off writes "enable": false — in place of an existing "enable", otherwise
//     as the first key, where it is the first thing a reader sees;
//   - on removes an "enable": false; when that leaves nothing, out is nil and
//     the file should be deleted, so a site that had no config gets none back.
//
// changed is false when the config already says what was asked (an explicit
// "enable": true counts as on). A body the server would reject is an error:
// rewriting it would hide the problem.
func SetEnable(raw []byte, on bool) (out []byte, changed bool, err error) {
	var cfg sites.Config
	var ms []member
	if len(bytes.TrimSpace(raw)) > 0 {
		if cfg, err = sites.ParseConfig(raw); err != nil {
			return nil, false, err
		}
		if ms, err = parseObject(raw); err != nil {
			return nil, false, err
		}
	}
	if cfg.Enabled() == on {
		return raw, false, nil
	}

	off := member{key: "enable", val: json.RawMessage("false")}
	kept := make([]member, 0, len(ms)+1)
	placed := false
	for _, m := range ms {
		if m.key != "enable" {
			kept = append(kept, m)
			continue
		}
		// Off: overwrite the existing key where it stands (once — a duplicate
		// key is dropped). On: drop it, the default is on.
		if !on && !placed {
			kept = append(kept, off)
			placed = true
		}
	}
	if !on && !placed {
		kept = append([]member{off}, kept...)
	}
	if len(kept) == 0 {
		return nil, true, nil
	}
	out, err = marshalObject(kept)
	return out, err == nil, err
}

// SwitchOptions is a parsed `zipgo off` / `zipgo on` invocation.
type SwitchOptions struct {
	Hosts  []string
	SSH    string // --ssh/--target override; empty = the .zipgo.json target
	DryRun bool   // say what would change, touch nothing
}

// ParseSwitchArgs parses the arguments of `zipgo off` / `zipgo on`: one or more
// hosts, an optional --ssh/--target and -n/--dry-run.
func ParseSwitchArgs(args []string) (SwitchOptions, error) {
	var o SwitchOptions
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--ssh" || a == "--target":
			i++
			if i >= len(args) {
				return o, fmt.Errorf("%s requires a value", a)
			}
			o.SSH = args[i]
		case strings.HasPrefix(a, "--ssh="):
			o.SSH = a[len("--ssh="):]
		case strings.HasPrefix(a, "--target="):
			o.SSH = a[len("--target="):]
		case a == "-n" || a == "--dry-run":
			o.DryRun = true
		case strings.HasPrefix(a, "-"):
			return o, fmt.Errorf("unknown flag %q", a)
		default:
			o.Hosts = append(o.Hosts, a)
		}
	}
	if len(o.Hosts) == 0 {
		return o, fmt.Errorf("which site? pass at least one host")
	}
	return o, nil
}

// Switch turns each host off (on=false) or back on, editing its remote
// .zipgoconfig.json, and reports on w what changed — including the nested sites
// that go down or come back with it, and any parent that keeps it off anyway.
func Switch(w io.Writer, tgt Target, o SwitchOptions, on bool) error {
	for _, host := range o.Hosts {
		if err := switchOne(w, tgt, host, on, o.DryRun); err != nil {
			return err
		}
	}
	return nil
}

func switchOne(w io.Writer, tgt Target, host string, on, dryRun bool) error {
	st, err := inspect(tgt, host, false, true)
	if err != nil {
		return err
	}
	if !st.Self.Exists {
		return fmt.Errorf("%s is not deployed (no folder %s:%s)", host, tgt.Host, st.Self.Dir)
	}
	if st.Self.Err != nil {
		return fmt.Errorf("%s: %v — fix it by hand first, zipgo won't rewrite a config the server rejects", host, st.Self.Err)
	}
	out, changed, err := SetEnable(st.Self.Config, on)
	if err != nil {
		return fmt.Errorf("%s: %w", host, err)
	}
	// isNow/would pick the tense: a dry run reports what would happen.
	isNow, is := "is now", "is"
	if dryRun {
		isNow, is = "would be", "would be"
	}
	parents := st.offBy()

	if !on {
		if !changed {
			fmt.Fprintf(w, "⏸  %s is already off\n", host)
			return nil
		}
		if !dryRun {
			if err := writeConfig(tgt, st.Self.Dir, out); err != nil {
				return err
			}
		}
		live := liveNested(st)
		if st.Self.hasContent() {
			fmt.Fprintf(w, "⏸  %s %s off — it answers 410 Gone\n", host, isNow)
		} else {
			// Mirrors builder.Gone: a holder of subdomains has no page of its
			// own to replace, so only its nested sites change.
			fmt.Fprintf(w, "⏸  %s %s off — it serves nothing itself, so only its nested sites answer 410 Gone\n", host, isNow)
		}
		if len(live) > 0 {
			fmt.Fprintf(w, "   and %d nested site%s with it: %s\n", len(live), plural(len(live)), strings.Join(live, ", "))
		}
		return nil
	}

	if changed && !dryRun {
		if err := writeConfig(tgt, st.Self.Dir, out); err != nil {
			return err
		}
	}
	switch {
	case changed && len(parents) == 0:
		fmt.Fprintf(w, "▶  %s %s back on\n", host, is)
	case changed:
		fmt.Fprintf(w, "▶  %s %s switched on, but stays off through %s\n", host, is, strings.Join(parents, ", "))
	case len(parents) > 0:
		fmt.Fprintf(w, "⏸  %s is off through %s\n", host, strings.Join(parents, ", "))
	default:
		fmt.Fprintf(w, "▶  %s is already on\n", host)
	}
	if len(parents) > 0 {
		fmt.Fprintf(w, "   run `zipgo on %s` to bring it back\n", parents[len(parents)-1])
		return nil
	}
	if own := ownOffNested(st); len(own) > 0 {
		fmt.Fprintf(w, "   still off on their own: %s\n", strings.Join(own, ", "))
	}
	return nil
}

// liveNested lists the nested sites that are on right now apart from the
// cascade — the ones a `zipgo off` of their parent actually takes down.
func liveNested(st siteState) []string {
	var hosts []string
	for _, n := range st.Nested {
		// A back-office isn't taken down by its parent (sites.Discover).
		if !offBelow(st, n) && !n.Own.IsAdmin() {
			hosts = append(hosts, n.Host)
		}
	}
	return hosts
}

// ownOffNested lists the nested sites that stay off after their parent comes
// back on, because they (or a site between them and it) are off themselves.
func ownOffNested(st siteState) []string {
	var hosts []string
	for _, n := range st.Nested {
		if offBelow(st, n) {
			hosts = append(hosts, n.Host)
		}
	}
	return hosts
}

// offBelow reports whether nested folder n is switched off by its own config or
// by a nested folder between it and st.Self.
func offBelow(st siteState, n folder) bool {
	for _, m := range st.Nested {
		if m.ownOff() && (m.Dir == n.Dir || strings.HasPrefix(n.Dir, m.Dir+"/")) {
			return true
		}
	}
	return false
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
