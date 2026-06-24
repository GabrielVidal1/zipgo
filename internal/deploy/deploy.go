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
	"strings"
)

// Options is the parsed `zipgo deploy` invocation.
type Options struct {
	Src      string   // local source directory (its CONTENTS are synced)
	Hosts    []string // one or more fully-qualified hosts (from -d)
	SSH      string   // raw --ssh spec: user@host:/base/path
	Delete   bool     // mirror the destination (rsync --delete); default true
	Excludes []string // rsync --exclude patterns
	DryRun   bool     // rsync --dry-run; also skips remote mkdir

	// IncludeSubdomains, when true, lets the --delete mirror also delete nested
	// subdomain folders (zipgo's trailing-dot directories, e.g. `pb.`/`demo.`).
	// It defaults to false: nested subdomains are auto-excluded so deploying a
	// parent host never wipes a child subdomain published under it.
	IncludeSubdomains bool
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

// Run executes the deploy: for each host it creates the remote folder tree and
// rsyncs Src into it.
func Run(o Options) error {
	info, err := os.Stat(o.Src)
	if err != nil {
		return fmt.Errorf("source %q: %w", o.Src, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source %q is not a directory", o.Src)
	}
	// Trailing slash => sync the directory's CONTENTS, not the dir itself.
	src := strings.TrimRight(o.Src, "/") + "/"

	tgt, err := ParseTarget(o.SSH)
	if err != nil {
		return err
	}

	for _, host := range o.Hosts {
		remoteDir, err := RemoteDir(tgt.BaseDir, host)
		if err != nil {
			return err
		}
		fmt.Printf("🌐  %s\n", host)
		fmt.Printf("   → %s:%s\n", tgt.Host, remoteDir)

		if !o.DryRun {
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
	o := Options{Delete: true}

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
		case a == "--ssh":
			v, err := next(&i, a)
			if err != nil {
				return o, err
			}
			o.SSH = v
		case strings.HasPrefix(a, "--ssh="):
			o.SSH = a[len("--ssh="):]
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

	if o.Src == "" {
		return o, fmt.Errorf("missing source directory")
	}
	if len(o.Hosts) == 0 {
		return o, fmt.Errorf("missing -d <subdomains>.<domain>")
	}
	if o.SSH == "" {
		return o, fmt.Errorf("missing --ssh user@host:/base/path")
	}
	return o, nil
}
