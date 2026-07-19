package deploy

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"zipgo/internal/sites"
)

// DefaultKeep is how many past deploys `zipgo deploy` retains under a site's
// .zipgo-versions folder when --keep is not given. Older snapshots are pruned.
const DefaultKeep = 5

// versionsExclude is the rsync pattern that protects the deploy-history folder
// from the --delete mirror: a redeploy must never wipe the snapshots it just
// took (or took on an earlier deploy). Matches the dir at the site root only.
const versionsExclude = "/" + sites.VersionsDirName + "/"

// snapshotScript builds a shell script, run over SSH on the remote before the
// rsync overwrite, that snapshots the site's *current* content into
// remoteDir/.zipgo-versions/<stamp>/ and then prunes the folder to the newest
// `keep` snapshots. It is a no-op (prints nothing, exits 0) when there is
// nothing worth snapshotting — the folder doesn't exist yet (first deploy) or
// holds no index.html (an empty tree or a pure-subdomain parent), so a broken /
// half-written deploy is never captured as a "good" version to roll back to.
//
// The snapshot copies the site's own files only: nested trailing-dot subdomain
// folders are separate sites (with their own history) and .zipgo-versions is the
// history itself, so both are excluded — the same content boundary rsync's
// deploy mirror and `remote` sizing use. rsync is assumed on the remote (it is
// zipgo deploy's transport, so it is already required there).
func snapshotScript(remoteDir string, keep int) string {
	v := remoteDir + "/" + sites.VersionsDirName
	q := shQuote
	var b strings.Builder
	// Guard: only snapshot a real, non-empty prior deploy.
	b.WriteString("d=" + q(remoteDir) + "\n")
	b.WriteString("[ -d \"$d\" ] || exit 0\n")
	b.WriteString("[ -f \"$d/index.html\" ] || exit 0\n")
	b.WriteString("v=" + q(v) + "\n")
	b.WriteString("mkdir -p \"$v\" || exit 1\n")
	// Timestamped snapshot dir. date is GNU/coreutils on the remote (as the
	// existing `date -r`/`find -printf` calls already assume).
	b.WriteString("stamp=$(date -u '+%Y%m%dT%H%M%SZ')\n")
	// Avoid clobbering a same-second snapshot from a retried deploy.
	b.WriteString("s=\"$v/$stamp\"; n=1; while [ -e \"$s\" ]; do s=\"$v/$stamp-$n\"; n=$((n+1)); done\n")
	b.WriteString("mkdir -p \"$s\" || exit 1\n")
	// Copy the site's own content, excluding the history folder and nested
	// subdomain sites. A trailing slash on the source syncs its contents.
	b.WriteString("rsync -a --exclude=" + q(sites.VersionsDirName) + " --exclude=" + q(subdomainExclude) +
		" \"$d/\" \"$s/\" || { rm -rf \"$s\"; exit 1; }\n")
	// Prune: keep the newest `keep` snapshot dirs (lexical == chronological for
	// the UTC stamp), remove the rest.
	fmt.Fprintf(&b, "ls -1 \"$v\" 2>/dev/null | sort | head -n -%d | while IFS= read -r old; do rm -rf \"$v/$old\"; done\n", keep)
	b.WriteString("echo \"$s\"\n")
	return b.String()
}

// shQuote wraps s for safe interpolation into a remote /bin/sh command line.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// snapshot runs snapshotScript on the remote for one deploy job, before the
// rsync overwrite. A snapshot failure is surfaced to the caller so a deploy that
// can't preserve history fails loudly rather than silently losing the ability to
// roll back. keep <= 0 disables snapshotting entirely (the caller skips it).
func snapshot(tgt Target, remoteDir string, keep int) error {
	cmd := exec.Command("ssh", tgt.Host, snapshotScript(remoteDir, keep))
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("snapshotting %q: %w", remoteDir, err)
	}
	if snap := strings.TrimSpace(string(out)); snap != "" {
		fmt.Printf("   ⏱  snapshot → %s\n", strings.TrimPrefix(snap, remoteDir+"/"))
	}
	return nil
}
