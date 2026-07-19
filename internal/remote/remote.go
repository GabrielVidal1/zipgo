// Package remote implements the read-only `zipgo ls` and `zipgo info`
// subcommands: a thin SSH client for inspecting which sites are deployed under a
// remote zipgo domains folder, run against the default target from .zipgo.json.
package remote

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"zipgo/internal/deploy"
	"zipgo/internal/sites"
)

// Site is the machine-readable record for one deployed site, emitted by
// `zipgo ls --json` and `zipgo info --json`. Size and Files count the site's own
// content: nested subdomain folders are sites of their own and are excluded, so
// an apex is not reported as the sum of its whole domain.
type Site struct {
	Host string `json:"host"`
	URL  string `json:"url"`
	Path string `json:"path"`
	// Type is "static", "spa" (index.html + a bundle folder), "proxy"
	// (.zipgoconfig.json rewrite) or "redirect" (.zipgoconfig.json redirect),
	// mirroring how the server routes the site.
	Type string `json:"type"`
	// Proxy is the rewrite upstream; set only when Type is "proxy".
	Proxy string `json:"proxy,omitempty"`
	// Redirect is the redirect target and RedirectStatus the status code it is
	// sent with; both set only when Type is "redirect".
	Redirect       string `json:"redirect,omitempty"`
	RedirectStatus int    `json:"redirectStatus,omitempty"`
	// Enabled is false when .zipgoconfig.json sets "enable": false — the site is
	// deployed but deliberately not served.
	Enabled bool `json:"enabled"`
	// Protected is true when .zipgoconfig.json sets "basicAuth" — the site is
	// served, but only to a caller with credentials. The hashes themselves are
	// deliberately not exposed here.
	Protected bool   `json:"protected"`
	Deployed  bool   `json:"deployed"`
	Size      int64  `json:"sizeBytes"`
	Files     int    `json:"files"`
	Modified  string `json:"modified,omitempty"` // RFC3339, UTC
	// ConfigError reports an unreadable .zipgoconfig.json — the site's own
	// folder exists but the server will refuse to build a config from it.
	ConfigError string `json:"configError,omitempty"`
}

// Entry is one file or folder inside a site, emitted by `zipgo ls <host> --json`.
type Entry struct {
	Name     string `json:"name"`
	Dir      bool   `json:"dir"`
	Size     int64  `json:"sizeBytes"`
	Modified string `json:"modified,omitempty"` // RFC3339, UTC
}

// site type names, mirroring how internal/builder routes a site.
const (
	typeStatic   = "static"
	typeSPA      = "spa"
	typeProxy    = "proxy"
	typeRedirect = "redirect"
)

// bundleDirs are the folders that, next to an index.html, mark a site as an SPA.
// Kept in sync with internal/sites (Vite, CRA, Next.js, generic).
var bundleDirs = []string{"assets", "static", "_next", "dist"}

// runSSH runs a command on the target host and returns its combined output.
func runSSH(target deploy.Target, remoteCmd string) (string, error) {
	cmd := exec.Command("ssh", target.Host, remoteCmd)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	return string(out), err
}

// quote wraps s for safe interpolation into a remote /bin/sh command line.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// hostDir pairs a deployed host with the remote folder that backs it.
type hostDir struct {
	host string
	dir  string
}

// siteDirs walks the base folder and reconstructs every site folder back into a
// hostname, sorted by apex domain then by host.
func siteDirs(target deploy.Target) ([]hostDir, error) {
	// find every directory under the base; HostFromRemote keeps only real site
	// folders (apex + trailing-dot subdomain folders), dropping content dirs.
	out, err := runSSH(target, "find "+quote(target.BaseDir)+" -mindepth 1 -maxdepth 6 -type d 2>/dev/null")
	if err != nil {
		return nil, fmt.Errorf("scanning %q: %w", target.BaseDir, err)
	}

	found := make([]hostDir, 0)
	for _, line := range strings.Split(out, "\n") {
		dir := strings.TrimRight(line, "\r")
		if dir == "" {
			continue
		}
		if h, ok := deploy.HostFromRemote(target.BaseDir, dir); ok {
			found = append(found, hostDir{host: h, dir: dir})
		}
	}
	// Sort by apex first so each domain groups under a single header, then by
	// full host within the group.
	sort.Slice(found, func(i, j int) bool {
		ai, aj := apexOf(found[i].host), apexOf(found[j].host)
		if ai != aj {
			return ai < aj
		}
		return found[i].host < found[j].host
	})
	return found, nil
}

// statScript builds a shell script that prints one tab-separated record per
// folder: dir, exists, bytes, files, mtime (epoch), index.html?, bundle folder?,
// base64 .zipgoconfig.json. The two finds prune nested subdomain folders and the
// deploy-history folder (.zipgo-versions) so each site is measured on its own
// current content only.
func statScript(dirs []string) string {
	// prune drops both trailing-dot subdomain folders (separate sites) and the
	// deploy-history snapshots from the size/file counts.
	prune := "\\( -name '*.' -o -name " + quote(sites.VersionsDirName) + " \\) -type d -prune"
	var b strings.Builder
	b.WriteString("for d in")
	for _, d := range dirs {
		b.WriteString(" " + quote(d))
	}
	b.WriteString("; do\n")
	b.WriteString("  e=0; [ -d \"$d\" ] && e=1\n")
	b.WriteString("  sz=$(find \"$d\" -mindepth 1 " + prune + " -o -type f -printf '%s\\n' 2>/dev/null | awk '{s+=$1} END{print s+0}')\n")
	b.WriteString("  nf=$(find \"$d\" -mindepth 1 " + prune + " -o -type f -print 2>/dev/null | wc -l | tr -d ' ')\n")
	b.WriteString("  mt=$(date -r \"$d\" '+%s' 2>/dev/null)\n")
	b.WriteString("  ix=0; [ -f \"$d/index.html\" ] && ix=1\n")
	b.WriteString("  bd=0; for a in " + strings.Join(bundleDirs, " ") + "; do [ -d \"$d/$a\" ] && bd=1; done\n")
	b.WriteString("  cf=; [ -f \"$d/.zipgoconfig.json\" ] && cf=$(base64 \"$d/.zipgoconfig.json\" 2>/dev/null | tr -d '\\n')\n")
	b.WriteString("  printf '%s\\t%s\\t%s\\t%s\\t%s\\t%s\\t%s\\t%s\\n' \"$d\" \"$e\" \"$sz\" \"$nf\" \"$mt\" \"$ix\" \"$bd\" \"$cf\"\n")
	b.WriteString("done\n")
	return b.String()
}

// parseSites turns statScript's output into a Site per requested folder, keeping
// the input order. Folders the script said nothing about come back as
// not-deployed rather than being dropped.
func parseSites(want []hostDir, out string) []Site {
	byDir := make(map[string]Site, len(want))
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 8 {
			continue
		}
		s := Site{
			Path:     f[0],
			Deployed: f[1] == "1",
			Enabled:  true,
			Type:     typeStatic,
		}
		s.Size, _ = strconv.ParseInt(f[2], 10, 64)
		s.Files, _ = strconv.Atoi(f[3])
		s.Modified = unixToRFC3339(f[4])
		if f[5] == "1" && f[6] == "1" {
			s.Type = typeSPA
		}
		if cfg, err := decodeConfig(f[7]); err != nil {
			s.ConfigError = err.Error()
		} else {
			s.Enabled = cfg.Enabled()
			s.Protected = cfg.Protected()
			if cfg.Rewrite != "" {
				s.Type = typeProxy
				s.Proxy = cfg.Rewrite
			}
			// A redirect replaces the file server *and* any rewrite, the way
			// the builder routes it — so it decides the type last.
			if cfg.Redirect != "" {
				s.Type = typeRedirect
				s.Redirect = cfg.Redirect
				s.RedirectStatus = cfg.RedirectCode()
			}
		}
		byDir[f[0]] = s
	}

	list := make([]Site, 0, len(want))
	for _, hd := range want {
		s, ok := byDir[hd.dir]
		if !ok {
			s = Site{Path: hd.dir, Enabled: true, Type: typeStatic}
		}
		s.Host = hd.host
		s.URL = "https://" + hd.host
		list = append(list, s)
	}
	return list
}

// decodeConfig decodes a base64 .zipgoconfig.json as the server reads it. An
// empty payload is the zero config (the file is absent).
func decodeConfig(b64 string) (sites.Config, error) {
	var cfg sites.Config
	if b64 == "" {
		return cfg, nil
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return cfg, fmt.Errorf("unreadable .zipgoconfig.json")
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("malformed .zipgoconfig.json: %v", err)
	}
	return cfg, nil
}

// unixToRFC3339 formats an epoch-seconds string as RFC3339 UTC; "" when unset.
func unixToRFC3339(s string) string {
	sec, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || sec <= 0 {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}

// stat runs the stat script for the given folders and returns their records.
func stat(target deploy.Target, want []hostDir) ([]Site, error) {
	if len(want) == 0 {
		return nil, nil
	}
	dirs := make([]string, len(want))
	for i, hd := range want {
		dirs[i] = hd.dir
	}
	out, err := runSSH(target, statScript(dirs))
	if err != nil {
		return nil, fmt.Errorf("inspecting %s: %w", target.Host, err)
	}
	return parseSites(want, out), nil
}

// Sites returns a record for every site deployed under the target.
func Sites(target deploy.Target) ([]Site, error) {
	found, err := siteDirs(target)
	if err != nil {
		return nil, err
	}
	return stat(target, found)
}

// SiteFor returns the record for a single host, deployed or not.
func SiteFor(target deploy.Target, host string) (Site, error) {
	remoteDir, err := deploy.RemoteDir(target.BaseDir, host)
	if err != nil {
		return Site{}, err
	}
	list, err := stat(target, []hostDir{{host: host, dir: remoteDir}})
	if err != nil {
		return Site{}, err
	}
	return list[0], nil
}

// List prints the sites deployed under the target. With an empty host it lists
// every site (grouped by apex domain); with a host it lists that site folder's
// contents. asJSON swaps both for machine-readable output.
func List(target deploy.Target, host string, asJSON bool) error {
	if host != "" {
		remoteDir, err := deploy.RemoteDir(target.BaseDir, host)
		if err != nil {
			return err
		}
		if asJSON {
			entries, err := entries(target, remoteDir)
			if err != nil {
				return err
			}
			return printJSON(entries)
		}
		fmt.Printf("🌐  %s\n   %s:%s\n\n", host, target.Host, remoteDir)
		out, err := runSSH(target, "ls -lhA --time-style=long-iso "+quote(remoteDir))
		if err != nil {
			return fmt.Errorf("listing %q: %w", remoteDir, err)
		}
		fmt.Print(out)
		return nil
	}

	found, err := siteDirs(target)
	if err != nil {
		return err
	}
	if asJSON {
		list, err := stat(target, found)
		if err != nil {
			return err
		}
		if list == nil {
			list = []Site{}
		}
		return printJSON(list)
	}
	if len(found) == 0 {
		fmt.Printf("No sites found under %s:%s\n", target.Host, target.BaseDir)
		return nil
	}

	// Group by apex (the last two labels) for readable output.
	fmt.Printf("📁  %s:%s\n\n", target.Host, target.BaseDir)
	var apex string
	for _, hd := range found {
		a := apexOf(hd.host)
		if a != apex {
			apex = a
			fmt.Printf("%s\n", apex)
		}
		fmt.Printf("   https://%s\n", hd.host)
	}
	return nil
}

// Info prints the remote folder path, kind, on-disk size, file count, last
// modified time and live URL for a single deployed host.
func Info(target deploy.Target, host string, asJSON bool) error {
	s, err := SiteFor(target, host)
	if err != nil {
		return err
	}
	if asJSON {
		return printJSON(s)
	}

	fmt.Printf("🌐  %s\n", s.Host)
	fmt.Printf("   URL    : %s\n", s.URL)
	fmt.Printf("   Remote : %s:%s\n", target.Host, s.Path)
	if !s.Deployed {
		fmt.Printf("   Status : not deployed (folder does not exist)\n")
		return nil
	}
	kind := s.Type
	if s.Type == typeProxy {
		kind = fmt.Sprintf("%s → %s", typeProxy, s.Proxy)
	}
	if s.Type == typeRedirect {
		kind = fmt.Sprintf("%s → %s (%d)", typeRedirect, s.Redirect, s.RedirectStatus)
	}
	if !s.Enabled {
		kind += " (disabled: enable=false)"
	}
	if s.Protected {
		kind += " 🔒 basic-auth"
	}
	fmt.Printf("   Kind   : %s\n", kind)
	fmt.Printf("   Size   : %s\n", humanSize(s.Size))
	fmt.Printf("   Files  : %d\n", s.Files)
	if s.Modified != "" {
		fmt.Printf("   Updated: %s\n", s.Modified)
	}
	if s.ConfigError != "" {
		fmt.Printf("   ⚠️  %s\n", s.ConfigError)
	}
	return nil
}

// entries lists one site folder's direct children.
func entries(target deploy.Target, dir string) ([]Entry, error) {
	out, err := runSSH(target, "find "+quote(dir)+" -mindepth 1 -maxdepth 1 -printf '%y\\t%s\\t%T@\\t%f\\n' 2>/dev/null")
	if err != nil {
		return nil, fmt.Errorf("listing %q: %w", dir, err)
	}
	list := make([]Entry, 0)
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimRight(line, "\r"), "\t", 4)
		if len(f) < 4 {
			continue
		}
		e := Entry{Name: f[3], Dir: f[0] == "d"}
		e.Size, _ = strconv.ParseInt(f[1], 10, 64)
		e.Modified = unixToRFC3339(strings.SplitN(f[2], ".", 2)[0])
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

// printJSON writes v as indented JSON on stdout.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// humanSize renders a byte count the way `du -h` would.
func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(b)/float64(div), "KMGT"[exp])
}

// apexOf returns the registrable apex (last two labels) of a host.
func apexOf(host string) string {
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	if len(labels) < 2 {
		return host
	}
	return strings.Join(labels[len(labels)-2:], ".")
}
