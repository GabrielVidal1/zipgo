// Package remote implements the read-only `zipgo ls` and `zipgo info`
// subcommands: a thin SSH client for inspecting which sites are deployed under a
// remote zipgo domains folder, run against the default target from .zipgo.json.
package remote

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"zipgo/internal/deploy"
)

// runSSH runs a command on the target host and returns its combined output.
func runSSH(target deploy.Target, remoteCmd string) (string, error) {
	cmd := exec.Command("ssh", target.Host, remoteCmd)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	return string(out), err
}

// List prints the sites deployed under the target. With an empty host it walks
// the whole base folder and reconstructs each site folder back into a hostname
// (grouped by apex domain). With a host it lists that site folder's contents.
func List(target deploy.Target, host string) error {
	if host != "" {
		remoteDir, err := deploy.RemoteDir(target.BaseDir, host)
		if err != nil {
			return err
		}
		fmt.Printf("🌐  %s\n   %s:%s\n\n", host, target.Host, remoteDir)
		out, err := runSSH(target, "ls -lhA --time-style=long-iso '"+remoteDir+"'")
		if err != nil {
			return fmt.Errorf("listing %q: %w", remoteDir, err)
		}
		fmt.Print(out)
		return nil
	}

	// find every directory under the base; HostFromRemote keeps only real site
	// folders (apex + trailing-dot subdomain folders), dropping content dirs.
	out, err := runSSH(target, "find '"+target.BaseDir+"' -mindepth 1 -maxdepth 6 -type d 2>/dev/null")
	if err != nil {
		return fmt.Errorf("scanning %q: %w", target.BaseDir, err)
	}

	hosts := make([]string, 0)
	for _, line := range strings.Split(out, "\n") {
		dir := strings.TrimRight(line, "\r")
		if dir == "" {
			continue
		}
		if h, ok := deploy.HostFromRemote(target.BaseDir, dir); ok {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		fmt.Printf("No sites found under %s:%s\n", target.Host, target.BaseDir)
		return nil
	}
	// Sort by apex first so each domain groups under a single header, then by
	// full host within the group.
	sort.Slice(hosts, func(i, j int) bool {
		ai, aj := apexOf(hosts[i]), apexOf(hosts[j])
		if ai != aj {
			return ai < aj
		}
		return hosts[i] < hosts[j]
	})

	// Group by apex (the last two labels) for readable output.
	fmt.Printf("📁  %s:%s\n\n", target.Host, target.BaseDir)
	var apex string
	for _, h := range hosts {
		a := apexOf(h)
		if a != apex {
			apex = a
			fmt.Printf("%s\n", apex)
		}
		fmt.Printf("   https://%s\n", h)
	}
	return nil
}

// Info prints the remote folder path, on-disk size, last-modified time and live
// URL for a single deployed host.
func Info(target deploy.Target, host string) error {
	remoteDir, err := deploy.RemoteDir(target.BaseDir, host)
	if err != nil {
		return err
	}
	// One round-trip: existence + size + mtime.
	script := "d='" + remoteDir + "'; " +
		"if [ ! -d \"$d\" ]; then echo MISSING; exit 0; fi; " +
		"echo SIZE $(du -sh \"$d\" 2>/dev/null | cut -f1); " +
		"echo MTIME $(date -r \"$d\" '+%Y-%m-%d %H:%M:%S' 2>/dev/null); " +
		"echo FILES $(find \"$d\" -type f 2>/dev/null | wc -l | tr -d ' ')"
	out, err := runSSH(target, script)
	if err != nil {
		return fmt.Errorf("inspecting %q: %w", remoteDir, err)
	}

	fmt.Printf("🌐  %s\n", host)
	fmt.Printf("   URL    : https://%s\n", host)
	fmt.Printf("   Remote : %s:%s\n", target.Host, remoteDir)
	if strings.Contains(out, "MISSING") {
		fmt.Printf("   Status : not deployed (folder does not exist)\n")
		return nil
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		switch {
		case strings.HasPrefix(line, "SIZE "):
			fmt.Printf("   Size   : %s\n", strings.TrimPrefix(line, "SIZE "))
		case strings.HasPrefix(line, "MTIME "):
			fmt.Printf("   Updated: %s\n", strings.TrimPrefix(line, "MTIME "))
		case strings.HasPrefix(line, "FILES "):
			fmt.Printf("   Files  : %s\n", strings.TrimPrefix(line, "FILES "))
		}
	}
	return nil
}

// apexOf returns the registrable apex (last two labels) of a host.
func apexOf(host string) string {
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	if len(labels) < 2 {
		return host
	}
	return strings.Join(labels[len(labels)-2:], ".")
}
