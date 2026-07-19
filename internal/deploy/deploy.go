// Package deploy implements the `zipgo deploy` subcommand: it rsyncs a local
// directory to a remote zipgo host over SSH, recursively creating the
// domain/subdomain folder tree (using zipgo's trailing-dot subdomain
// convention) on the way.
//
// Example:
//
//	zipgo deploy dist/ -d love-letters.game.gabvdl.xyz \
//	    --ssh gabrielvidal@100.74.118.12:/home/gabrielvidal/services/domains
//
// maps to the remote folder:
//
//	/home/gabrielvidal/services/domains/gabvdl.xyz/game./love-letters.
//
// and serves it at https://love-letters.game.gabvdl.xyz once zipgo hot-reloads.
package deploy

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"

	"zipgo/internal/zipconfig"
)

// parseKeep parses the --keep value: a non-negative integer number of deploys
// to retain (0 disables history).
func parseKeep(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid --keep %q: expected a non-negative integer", s)
	}
	return n, nil
}

// Options is the parsed `zipgo deploy` invocation.
type Options struct {
	Src      string   // explicit positional source dir (its CONTENTS are synced)
	Hosts    []string // explicit fully-qualified hosts (from -d), may be empty
	SSH      string   // raw --ssh/--target spec: user@host:/base/path or a name
	Delete   bool     // mirror the destination (rsync --delete); default true
	Excludes []string // rsync --exclude patterns
	DryRun   bool     // rsync --dry-run; also skips remote mkdir

	// IncludeSubdomains, when true, lets the --delete mirror also delete nested
	// subdomain folders (zipgo's trailing-dot directories, e.g. `pb.`/`demo.`).
	// It defaults to false: nested subdomains are auto-excluded so deploying a
	// parent host never wipes a child subdomain published under it.
	IncludeSubdomains bool

	// Keep is how many previous deploys to snapshot under each site's hidden
	// .zipgo-versions folder before overwriting it, so `zipgo rollback` can swap
	// an earlier one back in. Defaults to DefaultKeep; 0 disables history (no
	// snapshot is taken). A --dry-run never snapshots.
	Keep int

	// Jobs is the resolved list of (host, source) pairs to deploy. It is filled
	// by Resolve from the explicit flags and/or the project/root config, and is
	// what Run iterates.
	Jobs []Job
}

// Job is one resolved deploy: sync Src's contents to the folder for Host.
type Job struct {
	Host string
	Src  string
}

// subdomainExclude is the rsync pattern matching zipgo's nested-subdomain
// folders: every subdomain label is stored as a directory whose name ends in a
// trailing dot (e.g. `pb.`, `demo.`). The trailing `/` anchors the match to
// directories only.
const subdomainExclude = "*./"

// Target is a parsed --ssh destination.
type Target struct {
	Host    string // user@host (the ssh/rsync destination prefix)
	BaseDir string // remote domains base directory
}

// ParseTarget splits a "user@host:/base/path" spec into its host and base dir.
func ParseTarget(spec string) (Target, error) {
	idx := strings.Index(spec, ":")
	if idx <= 0 {
		return Target{}, fmt.Errorf("invalid --ssh %q: expected user@host:/base/path", spec)
	}
	host := spec[:idx]
	base := strings.TrimRight(spec[idx+1:], "/")
	if host == "" || base == "" {
		return Target{}, fmt.Errorf("invalid --ssh %q: expected user@host:/base/path", spec)
	}
	return Target{Host: host, BaseDir: base}, nil
}

// RemoteDir builds the remote folder path for a host under baseDir, following
// zipgo's convention: the registrable apex (last two labels) is a plain folder,
// and each subdomain label is a folder ending in a trailing dot, nested
// parent-first under the apex.
//
//	love-letters.game.gabvdl.xyz  ->  <base>/gabvdl.xyz/game./love-letters.
func RemoteDir(baseDir, host string) (string, error) {
	apex, subs, err := splitHost(host)
	if err != nil {
		return "", err
	}
	p := path.Join(baseDir, apex)
	// subs is leaf-first; nest parent-first so the apex is the outermost dir.
	for i := len(subs) - 1; i >= 0; i-- {
		p = path.Join(p, subs[i]+".")
	}
	return p, nil
}

// HostFromRemote is the inverse of RemoteDir: given the base dir and a remote
// folder path under it, it reconstructs the host the folder serves. ok is false
// when dir is not a valid site folder — e.g. an ordinary content directory
// whose name lacks zipgo's trailing-dot subdomain marker (assets/, install/…),
// or a path outside baseDir.
//
//	<base>/gabvdl.xyz/game./love-letters.  ->  love-letters.game.gabvdl.xyz
func HostFromRemote(baseDir, dir string) (host string, ok bool) {
	base := path.Clean(baseDir)
	d := path.Clean(dir)
	rel := strings.TrimPrefix(d, base+"/")
	if rel == d || rel == "" {
		return "", false // dir not under base, or equals base
	}
	parts := strings.Split(rel, "/")
	apex := parts[0]
	if !strings.Contains(apex, ".") {
		return "", false // first component must be a registrable domain folder
	}
	// Every component after the apex must be a trailing-dot subdomain folder;
	// anything else is ordinary site content, not a site of its own.
	subs := parts[1:] // parent-first
	labels := make([]string, 0, len(subs))
	for i := len(subs) - 1; i >= 0; i-- { // reverse -> leaf-first
		s := subs[i]
		if !strings.HasSuffix(s, ".") || s == "." {
			return "", false
		}
		labels = append(labels, strings.TrimSuffix(s, "."))
	}
	labels = append(labels, apex)
	return strings.Join(labels, "."), true
}

// splitHost separates a host into its apex domain (last two labels) and the
// subdomain labels (leaf-first, i.e. the order they appear in the host).
func splitHost(host string) (apex string, subs []string, err error) {
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	if host == "" {
		return "", nil, fmt.Errorf("empty domain")
	}
	labels := strings.Split(host, ".")
	for _, l := range labels {
		if l == "" {
			return "", nil, fmt.Errorf("invalid domain %q: empty label", host)
		}
	}
	if len(labels) < 2 {
		return "", nil, fmt.Errorf("invalid domain %q: need at least apex.tld", host)
	}
	apex = strings.Join(labels[len(labels)-2:], ".")
	subs = labels[:len(labels)-2]
	return apex, subs, nil
}

// Resolve fills in o.SSH and o.Jobs from the project/root config wherever they
// were not given explicitly on the command line, then validates the result. It
// is pure — the caller loads proj and root (via zipconfig) and passes them in,
// so the resolution logic is unit-testable without touching the filesystem.
//
// Precedence, each field independently:
//   - target: --ssh flag > project "target" > root "target" (a value matching a
//     root "targets" name is expanded to its spec)
//   - hosts:  -d flags > every key of the project deploy map
//   - source: positional dir > the project deploy map entry for the host
func Resolve(o *Options, proj zipconfig.ProjectConfig, root zipconfig.RootConfig) error {
	// ---- target (--ssh) ----
	spec := o.SSH
	if spec == "" {
		spec = proj.Target
	}
	if spec == "" {
		spec = root.Target
	}
	if named, ok := root.Targets[spec]; ok {
		spec = named
	}
	if spec == "" {
		return fmt.Errorf("no deploy target: pass --ssh, set \"target\" in package.json's zipgo, or add a target to .zipgo.json")
	}
	o.SSH = spec

	// ---- hosts ----
	hosts := o.Hosts
	fromMap := false
	if len(hosts) == 0 {
		if len(proj.Deploy) == 0 {
			return fmt.Errorf("no deploy hosts: pass -d <host> or add a zipgo.deploy map to package.json")
		}
		for h := range proj.Deploy {
			hosts = append(hosts, h)
		}
		sort.Strings(hosts)
		fromMap = true
	}

	// ---- source per host ----
	if o.Src != "" && fromMap && len(hosts) > 1 {
		return fmt.Errorf("a positional source dir is ambiguous with %d mapped hosts; pass -d to pick one", len(hosts))
	}
	o.Jobs = nil
	for _, h := range hosts {
		src := o.Src
		if src == "" {
			src = proj.Deploy[h]
		}
		if src == "" {
			return fmt.Errorf("no source folder for %q: pass a dir argument or add it to package.json zipgo.deploy", h)
		}
		o.Jobs = append(o.Jobs, Job{Host: h, Src: src})
	}
	return nil
}

// Run executes the deploy: for each resolved job it creates the remote folder
// tree and rsyncs the job's source into it. Call Resolve first to populate
// o.Jobs and o.SSH.
func Run(o Options) error {
	tgt, err := ParseTarget(o.SSH)
	if err != nil {
		return err
	}
	if len(o.Jobs) == 0 {
		return fmt.Errorf("nothing to deploy (call Resolve first)")
	}

	for _, job := range o.Jobs {
		host := job.Host
		info, err := os.Stat(job.Src)
		if err != nil {
			return fmt.Errorf("source %q: %w", job.Src, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("source %q is not a directory", job.Src)
		}
		// Trailing slash => sync the directory's CONTENTS, not the dir itself.
		src := strings.TrimRight(job.Src, "/") + "/"

		remoteDir, err := RemoteDir(tgt.BaseDir, host)
		if err != nil {
			return err
		}
		fmt.Printf("🌐  %s\n", host)
		fmt.Printf("   → %s:%s\n", tgt.Host, remoteDir)

		if !o.DryRun {
			// Snapshot the site's current content into .zipgo-versions before the
			// rsync overwrites it, so a bad deploy can be rolled back. A no-op on
			// a first deploy (nothing there yet) or when history is disabled.
			if o.Keep > 0 {
				if err := snapshot(tgt, remoteDir, o.Keep); err != nil {
					return err
				}
			}
			// Recursively create the subdomain folder tree (trailing dots and
			// all). Quote the path so the remote shell keeps it intact.
			mkdir := exec.Command("ssh", tgt.Host, "mkdir -p '"+remoteDir+"'")
			mkdir.Stdout, mkdir.Stderr = os.Stdout, os.Stderr
			if err := mkdir.Run(); err != nil {
				return fmt.Errorf("creating remote dir %q: %w", remoteDir, err)
			}
		}

		args := []string{"-avz"}
		if o.Delete {
			args = append(args, "--delete")
			// Always protect the deploy-history folder from the mirror: the
			// snapshots are not part of the build, and --delete would otherwise
			// wipe the history on the very next deploy (even with
			// --include-subdomains).
			args = append(args, "--exclude="+versionsExclude)
			// Auto-protect nested subdomain folders from the mirror: their
			// trailing-dot directories are separate sites, not part of this
			// build, so deploying a parent host must never delete a child.
			if !o.IncludeSubdomains {
				args = append(args, "--exclude="+subdomainExclude)
			}
		}
		for _, ex := range o.Excludes {
			args = append(args, "--exclude="+ex)
		}
		if o.DryRun {
			args = append(args, "--dry-run")
		}
		args = append(args, src, tgt.Host+":"+remoteDir+"/")

		rsync := exec.Command("rsync", args...)
		rsync.Stdout, rsync.Stderr = os.Stdout, os.Stderr
		if err := rsync.Run(); err != nil {
			return fmt.Errorf("rsync to %q: %w", host, err)
		}
		fmt.Printf("   ✓ https://%s\n", host)
	}
	return nil
}

// ParseArgs parses the `zipgo deploy` argument list (everything after the
// `deploy` subcommand). The local source directory is positional; -d may be
// repeated to deploy the same build to several hosts.
func ParseArgs(args []string) (Options, error) {
	o := Options{Delete: true, Keep: DefaultKeep}

	next := func(i *int, flag string) (string, error) {
		*i++
		if *i >= len(args) {
			return "", fmt.Errorf("%s requires a value", flag)
		}
		return args[*i], nil
	}

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-d" || a == "--domain":
			v, err := next(&i, a)
			if err != nil {
				return o, err
			}
			o.Hosts = append(o.Hosts, v)
		case strings.HasPrefix(a, "-d="):
			o.Hosts = append(o.Hosts, a[len("-d="):])
		case strings.HasPrefix(a, "--domain="):
			o.Hosts = append(o.Hosts, a[len("--domain="):])
		case a == "--ssh" || a == "--target":
			v, err := next(&i, a)
			if err != nil {
				return o, err
			}
			o.SSH = v
		case strings.HasPrefix(a, "--ssh="):
			o.SSH = a[len("--ssh="):]
		case strings.HasPrefix(a, "--target="):
			o.SSH = a[len("--target="):]
		case a == "--exclude":
			v, err := next(&i, a)
			if err != nil {
				return o, err
			}
			o.Excludes = append(o.Excludes, v)
		case strings.HasPrefix(a, "--exclude="):
			o.Excludes = append(o.Excludes, a[len("--exclude="):])
		case a == "--no-delete":
			o.Delete = false
		case a == "--delete":
			o.Delete = true
		case a == "--include-subdomains":
			o.IncludeSubdomains = true
		case a == "--keep":
			v, err := next(&i, a)
			if err != nil {
				return o, err
			}
			if o.Keep, err = parseKeep(v); err != nil {
				return o, err
			}
		case strings.HasPrefix(a, "--keep="):
			var err error
			if o.Keep, err = parseKeep(a[len("--keep="):]); err != nil {
				return o, err
			}
		case a == "--no-history":
			o.Keep = 0
		case a == "--dry-run" || a == "-n":
			o.DryRun = true
		case strings.HasPrefix(a, "-"):
			return o, fmt.Errorf("unknown flag %q", a)
		default:
			if o.Src != "" {
				return o, fmt.Errorf("unexpected argument %q (source already set to %q)", a, o.Src)
			}
			o.Src = a
		}
	}

	// Note: missing src/-d/--ssh are NOT errors here — they may be supplied by
	// the project package.json (zipgo.deploy) and root .zipgo.json (target).
	// Resolve performs final validation once those configs are loaded.
	return o, nil
}
