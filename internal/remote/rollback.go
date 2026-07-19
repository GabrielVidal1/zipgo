package remote

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"zipgo/internal/deploy"
	"zipgo/internal/sites"
)

// Version is one snapshot under a site's .zipgo-versions folder, taken by a
// past `zipgo deploy` before it overwrote the site.
type Version struct {
	Name     string `json:"name"`               // the snapshot folder name (a UTC timestamp)
	Modified string `json:"modified,omitempty"` // RFC3339, UTC — when the snapshot was taken
	Size     int64  `json:"sizeBytes"`
	Files    int    `json:"files"`
}

// Versions lists the deploy-history snapshots for a host, newest first. An empty
// list (no history yet) is not an error.
func Versions(target deploy.Target, host string) ([]Version, error) {
	dir, err := deploy.RemoteDir(target.BaseDir, host)
	if err != nil {
		return nil, err
	}
	vdir := dir + "/" + sites.VersionsDirName
	// One record per snapshot: name, mtime (epoch), bytes, file count.
	script := "for s in " + quote(vdir) + "/*/; do\n" +
		"  [ -d \"$s\" ] || continue\n" +
		"  n=$(basename \"$s\")\n" +
		"  mt=$(date -r \"$s\" '+%s' 2>/dev/null)\n" +
		"  sz=$(find \"$s\" -type f -printf '%s\\n' 2>/dev/null | awk '{s+=$1} END{print s+0}')\n" +
		"  nf=$(find \"$s\" -type f 2>/dev/null | wc -l | tr -d ' ')\n" +
		"  printf '%s\\t%s\\t%s\\t%s\\n' \"$n\" \"$mt\" \"$sz\" \"$nf\"\n" +
		"done\n"
	out, err := runSSH(target, script)
	if err != nil {
		return nil, fmt.Errorf("listing versions for %q: %w", host, err)
	}
	return parseVersions(out), nil
}

// parseVersions turns the versions script's tab-separated output into a
// newest-first []Version. It is pure (table-tested), like parseSites.
func parseVersions(out string) []Version {
	list := make([]Version, 0)
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 4 || f[0] == "" || strings.HasPrefix(f[0], "*") {
			continue // the "*/" glob is left literal when the folder is empty
		}
		v := Version{Name: f[0], Modified: unixToRFC3339(f[1])}
		v.Size, _ = strconv.ParseInt(f[2], 10, 64)
		v.Files, _ = strconv.Atoi(f[3])
		list = append(list, v)
	}
	// Newest first (the name is a lexically-sortable UTC stamp).
	sort.Slice(list, func(i, j int) bool { return list[i].Name > list[j].Name })
	return list
}

// ListVersions prints the deploy history of a host (newest first). asJSON emits
// the []Version array instead.
func ListVersions(target deploy.Target, host string, asJSON bool) error {
	versions, err := Versions(target, host)
	if err != nil {
		return err
	}
	if asJSON {
		return printJSON(versions)
	}
	dir, _ := deploy.RemoteDir(target.BaseDir, host)
	fmt.Printf("🕑  %s\n   %s:%s\n\n", host, target.Host, dir+"/"+sites.VersionsDirName)
	if len(versions) == 0 {
		fmt.Println("   No deploy history yet (deploy again to start snapshotting).")
		return nil
	}
	for i, v := range versions {
		tag := ""
		if i == 0 {
			tag = "  (current backup)"
		} else if i == 1 {
			tag = "  ← rollback target"
		}
		when := v.Modified
		if when == "" {
			when = v.Name
		}
		fmt.Printf("   %-22s  %-20s  %s  %d files%s\n", v.Name, when, humanSize(v.Size), v.Files, tag)
	}
	return nil
}

// Rollback swaps a past snapshot back into the live site folder. version is the
// snapshot name; when empty, the most recent snapshot is used (the previous
// deploy — the common "undo my last deploy" case). Before restoring, the current
// live content is itself snapshotted, so a rollback is reversible and never
// loses the state it replaced.
func Rollback(target deploy.Target, host, version string) error {
	dir, err := deploy.RemoteDir(target.BaseDir, host)
	if err != nil {
		return err
	}
	versions, err := Versions(target, host)
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		return fmt.Errorf("no deploy history for %q: nothing to roll back to", host)
	}
	if version == "" {
		version = versions[0].Name // newest snapshot = the previous deploy
	} else if !hasVersion(versions, version) {
		return fmt.Errorf("no snapshot %q for %q (see `zipgo rollback %s --list`)", version, host, host)
	}

	vdir := dir + "/" + sites.VersionsDirName
	src := vdir + "/" + version
	// The restore mirrors the snapshot over the live folder, but must never
	// touch the history folder itself or nested subdomain sites — the same
	// content boundary deploy uses. It snapshots the current live content first
	// (so the rollback can itself be undone), then rsyncs the chosen snapshot in.
	script := "d=" + quote(dir) + "; v=" + quote(vdir) + "; src=" + quote(src) + "\n" +
		"[ -d \"$src\" ] || { echo \"snapshot missing: $src\" >&2; exit 1; }\n" +
		// Back up current content before overwriting it.
		"if [ -f \"$d/index.html\" ]; then\n" +
		"  stamp=$(date -u '+%Y%m%dT%H%M%SZ'); s=\"$v/$stamp\"; n=1\n" +
		"  while [ -e \"$s\" ]; do s=\"$v/$stamp-$n\"; n=$((n+1)); done\n" +
		"  mkdir -p \"$s\" && rsync -a --exclude=" + quote(sites.VersionsDirName) +
		" --exclude=" + quote("*./") + " \"$d/\" \"$s/\" || exit 1\n" +
		"fi\n" +
		// Restore the chosen snapshot over the live folder.
		"rsync -a --delete --exclude=" + quote(sites.VersionsDirName) +
		" --exclude=" + quote("*./") + " \"$src/\" \"$d/\" || exit 1\n"
	out, err := runSSH(target, script)
	if err != nil {
		return fmt.Errorf("rolling back %q: %w", host, err)
	}
	if s := strings.TrimSpace(out); s != "" {
		fmt.Print(s + "\n")
	}
	fmt.Printf("↩️   %s rolled back to %s\n", host, version)
	fmt.Printf("   ✓ https://%s\n", host)
	return nil
}

// hasVersion reports whether name is one of the listed snapshots.
func hasVersion(versions []Version, name string) bool {
	for _, v := range versions {
		if v.Name == name {
			return true
		}
	}
	return false
}
