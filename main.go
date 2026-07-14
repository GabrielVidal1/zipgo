package main

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/fileserver"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/rewrite"
	_ "github.com/caddyserver/caddy/v2/modules/caddytls"
	_ "github.com/caddyserver/caddy/v2/modules/caddytls/standardstek"
	_ "github.com/caddyserver/caddy/v2/modules/metrics"
	"github.com/fsnotify/fsnotify"

	"zipgo/internal/builder"
	"zipgo/internal/config"
	"zipgo/internal/deploy"
	"zipgo/internal/doctor"
	"zipgo/internal/remote"
	"zipgo/internal/service"
	"zipgo/internal/sites"
	"zipgo/internal/zipconfig"
)

const deployUsage = `Usage: zipgo deploy [dir] [-d <host>] [--ssh user@host:/base/path]

Recursively creates the domain/subdomain folder tree (zipgo's trailing-dot
convention) on the remote host and rsyncs the source contents into it.

The target and the per-host source folders can come from config, so a fully
configured project deploys with just 'zipgo deploy':
  - root .zipgo.json (found by ascending from the cwd) provides the default
    target, e.g. {"target": "user@host:/base/domains"}
  - a project's package.json "zipgo" field maps hosts to source folders, e.g.
    {"zipgo": {"deploy": {"app.dev.gabvdl.xyz": "dist"}}}

  [dir]          explicit source dir (overrides the package.json mapping)
  -d, --domain   target host, e.g. love-letters.game.gabvdl.xyz (repeatable;
                 default: every host in the package.json deploy map)
      --ssh, --target  remote destination (or a name from .zipgo.json "targets");
                 default: project/root config target
      --exclude  rsync exclude pattern (repeatable)
      --no-delete  do not mirror (keep remote files missing from the source)
      --include-subdomains  let the mirror also delete nested subdomain
                 folders (by default trailing-dot subdomain dirs are kept)
  -n, --dry-run  show what rsync would do; skip remote mkdir

Examples:
  zipgo deploy                                 # all hosts from package.json
  zipgo deploy -d app.dev.gabvdl.xyz           # one host, source from config
  zipgo deploy dist/ -d love-letters.game.gabvdl.xyz \
      --ssh gabrielvidal@100.74.118.12:/home/gabrielvidal/services/domains
  # -> /home/gabrielvidal/services/domains/gabvdl.xyz/game./love-letters.
  #    served at https://love-letters.game.gabvdl.xyz
`

const manageUsage = `Usage: zipgo ls [host] [--json] [--ssh user@host:/base/path]
       zipgo info <host> [--json] [--ssh user@host:/base/path]

Inspect what is deployed under the remote zipgo domains folder (the target is
read from .zipgo.json unless --ssh/--target is given).

  zipgo ls                 list every deployed site, grouped by domain
  zipgo ls <host>          list the contents of one site's remote folder
  zipgo info <host>        show a site's remote path, kind, size, files, mtime
  --json                   machine-readable output:
                             ls          → array of sites (host, path, type,
                                           proxy, enabled, sizeBytes, files,
                                           modified)
                             ls <host>   → array of files in the site's folder
                             info <host> → that one site as an object
`

const doctorUsage = `Usage: zipgo doctor [domains-folder] [--strict]

Check the local domains folder and report, per site, everything that would stop
it serving: a missing index.html, a malformed or misspelt .zipgoconfig.json, a
folder that isn't a valid domain, a subdomain folder that forgot its trailing
dot, an unusable "rewrite" upstream or "redirect" target, two folders claiming
the same host.

  [domains-folder]  folder to check (default: $ZIPGO_DOMAINS_FOLDER, or .zipgo)
      --strict      exit 1 on warnings too, not just errors

Exits 1 when a site is broken, so it can gate a deploy:
  zipgo doctor && zipgo deploy
`

func main() {
	sub := ""
	if len(os.Args) > 1 {
		sub = os.Args[1]
	}

	switch sub {
	case "enable":
		if err := service.Enable(); err != nil {
			log.Fatalf("❌  %v\n", err)
		}
		return
	case "disable":
		if err := service.Disable(); err != nil {
			log.Fatalf("❌  %v\n", err)
		}
		return
	case "status":
		if err := service.Status(); err != nil {
			log.Fatalf("❌  %v\n", err)
		}
		return
	case "deploy":
		opts, err := deploy.ParseArgs(os.Args[2:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌  %v\n\n%s", err, deployUsage)
			os.Exit(2)
		}
		proj, root := loadConfigs()
		if err := deploy.Resolve(&opts, proj, root); err != nil {
			fmt.Fprintf(os.Stderr, "❌  %v\n\n%s", err, deployUsage)
			os.Exit(2)
		}
		if err := deploy.Run(opts); err != nil {
			log.Fatalf("❌  %v\n", err)
		}
		return
	case "ls", "info":
		host, ssh, asJSON, err := parseManageArgs(os.Args[2:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌  %v\n\n%s", err, manageUsage)
			os.Exit(2)
		}
		target, err := resolveTarget(ssh)
		if err != nil {
			log.Fatalf("❌  %v\n", err)
		}
		if sub == "info" {
			if host == "" {
				fmt.Fprintf(os.Stderr, "❌  info requires a host\n\n%s", manageUsage)
				os.Exit(2)
			}
			if err := remote.Info(target, host, asJSON); err != nil {
				log.Fatalf("❌  %v\n", err)
			}
		} else {
			if err := remote.List(target, host, asJSON); err != nil {
				log.Fatalf("❌  %v\n", err)
			}
		}
		return
	case "doctor":
		dir, strict, err := parseDoctorArgs(os.Args[2:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌  %v\n\n%s", err, doctorUsage)
			os.Exit(2)
		}
		if dir == "" {
			dir = domainsFolder()
		}
		rep, err := doctor.Check(dir)
		if err != nil {
			log.Fatalf("❌  %v\n", err)
		}
		doctor.Print(os.Stdout, rep, dir)
		if !rep.OK(strict) {
			os.Exit(1)
		}
		return
	case "help", "--help", "-h":
		fmt.Println("Usage: zipgo [command]")
		fmt.Println()
		fmt.Println("Commands:")
		fmt.Println("  serve    Start the server (default)")
		fmt.Println("  deploy   rsync a local dir to a remote zipgo host over SSH")
		fmt.Println("  ls       list sites deployed on the remote target")
		fmt.Println("  info     show a deployed site's remote path, size and mtime")
		fmt.Println("  doctor   check the local domains folder for broken sites")
		fmt.Println("  enable   Install and start the systemd user service")
		fmt.Println("  disable  Stop and remove the systemd user service")
		fmt.Println("  status   Show service status")
		fmt.Println()
		fmt.Print(deployUsage)
		fmt.Println()
		fmt.Print(manageUsage)
		fmt.Println()
		fmt.Print(doctorUsage)
		return
	case "serve", "":
		// fall through to server startup
	default:
		log.Fatalf("❌  Unknown command %q. Run 'zipgo help' for usage.\n", sub)
	}

	domainsDir := domainsFolder()

	// ---- discover domains ----
	domains, err := config.ReadDomains(domainsDir)
	if err != nil {
		log.Fatalf("❌  %v\n", err)
	}

	// isLocalhost is true when no domains are configured, or when the
	// ZIPGO_LOCALHOST env var is set (useful for make run-local with real
	// domain folders — serves on a single port with path routing).
	isLocalhost := len(domains) == 0 || os.Getenv("ZIPGO_LOCALHOST") == "1"

	// metricsAddr is "" unless ZIPGO_METRICS is set, in which case a Prometheus
	// /metrics endpoint is served on this (loopback by default) address.
	metricsAddr := builder.MetricsAddr()

	// ---- discoverAll: build []DomainSites for all configured domains ----
	discoverAll := func() ([]builder.DomainSites, error) {
		var result []builder.DomainSites
		for _, d := range domains {
			disc, err := sites.Discover(filepath.Join(domainsDir, d))
			if err != nil {
				return nil, err
			}
			result = append(result, builder.DomainSites{Domain: d, Sites: disc})
		}
		return result, nil
	}

	// ---- reload: re-discover and push new config to Caddy ----
	reload := func() error {
		domainSites, err := discoverAll()
		if err != nil {
			return err
		}
		var cfg *caddy.Config
		if isLocalhost {
			cfg, err = builder.BuildLocalhostConfig(domainSites, metricsAddr)
		} else {
			cfg, err = builder.BuildConfig(domainSites, metricsAddr)
		}
		if err != nil {
			return err
		}
		return caddy.Run(cfg)
	}

	// ---- discover sites and build config ----
	domainSites, err := discoverAll()
	if err != nil {
		log.Fatalf("❌  %v\n", err)
	}

	var cfg *caddy.Config

	if isLocalhost {
		totalSites := 0
		for _, ds := range domainSites {
			totalSites += len(ds.Sites)
		}
		fmt.Println("🖥️   Localhost mode")
		fmt.Printf("📁  Domains: %d  Sites: %d\n\n", len(domainSites), totalSites)
		for _, ds := range domainSites {
			for _, s := range ds.Sites {
				if !s.Config.Enabled() {
					continue
				}
				kind := siteKind(s)
				fmt.Printf("   [%s]  http://localhost:%d%s\n", kind, builder.LocalhostStartPort, s.LocalhostPath(ds.Domain))
			}
		}
		fmt.Println()
		cfg, err = builder.BuildLocalhostConfig(domainSites, metricsAddr)
	} else {
		for _, ds := range domainSites {
			fmt.Printf("🌐  Domain : %s (%d sites)\n", ds.Domain, len(ds.Sites))
			for _, s := range ds.Sites {
				if !s.Config.Enabled() {
					continue
				}
				kind := siteKind(s)
				scheme := "https"
				if s.Config.HTTPAllowed() {
					scheme = "http(s)"
				}
				fmt.Printf("   [%s]  %s://%s\n", kind, scheme, s.Host(ds.Domain))
			}
		}
		fmt.Println()
		cfg, err = builder.BuildConfig(domainSites, metricsAddr)
	}
	if err != nil {
		log.Fatalf("❌  Could not build config: %v\n", err)
	}

	// ---- start Caddy ----
	if isLocalhost {
		fmt.Println("🚀  Starting server (HTTP, no TLS)...")
	} else {
		fmt.Println("🚀  Starting Caddy (HTTPS via Let's Encrypt)...")
	}
	if err := caddy.Run(cfg); err != nil {
		log.Fatalf("❌  %v\n", err)
	}
	fmt.Println("✅  Live. Ctrl+C to stop.")
	if metricsAddr != "" {
		fmt.Printf("📊  Prometheus metrics: http://%s/metrics\n", metricsAddr)
	}

	// ---- watch domains dir for changes and auto-reload ----
	go watchAndReload(domainsDir, reload)

	// ---- wait for signal ----
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	fmt.Println("\n🛑  Shutting down...")
	caddy.Stop()
}

// siteKind is the padded label printed next to a site in the startup listing,
// mirroring how the builder routes it: a redirect target wins over a rewrite
// upstream, which wins over the file server (SPA or plain static).
func siteKind(s sites.Site) string {
	switch {
	case s.Config.Redirect != "":
		return "redir "
	case s.Config.Rewrite != "":
		return "proxy "
	case s.IsSPA:
		return "spa   "
	default:
		return "static"
	}
}

// loadConfigs discovers the project (package.json "zipgo") and root
// (.zipgo.json) configs by ascending from the current directory. Missing or
// unreadable configs degrade to zero values (a hard parse error aborts).
func loadConfigs() (zipconfig.ProjectConfig, zipconfig.RootConfig) {
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("❌  %v\n", err)
	}
	_, proj, _, err := zipconfig.FindProject(cwd)
	if err != nil {
		log.Fatalf("❌  %v\n", err)
	}
	_, root, _, err := zipconfig.FindRoot(cwd)
	if err != nil {
		log.Fatalf("❌  %v\n", err)
	}
	return proj, root
}

// resolveTarget builds the deploy.Target for the management subcommands from an
// explicit --ssh spec (which may be a name in .zipgo.json "targets"), falling
// back to the root config's default target.
func resolveTarget(ssh string) (deploy.Target, error) {
	_, root, _, err := zipconfig.FindRoot(mustCwd())
	if err != nil {
		return deploy.Target{}, err
	}
	spec := ssh
	if spec == "" {
		spec = root.Target
	}
	if named, ok := root.Targets[spec]; ok {
		spec = named
	}
	if spec == "" {
		return deploy.Target{}, fmt.Errorf("no target: pass --ssh user@host:/base or add a target to .zipgo.json")
	}
	return deploy.ParseTarget(spec)
}

func mustCwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("❌  %v\n", err)
	}
	return cwd
}

// domainsFolder is the folder scanned for domain subfolders, from
// ZIPGO_DOMAINS_FOLDER with the .zipgo default.
func domainsFolder() string {
	if dir := os.Getenv("ZIPGO_DOMAINS_FOLDER"); dir != "" {
		return dir
	}
	return ".zipgo"
}

// parseDoctorArgs parses the args for `doctor`: an optional positional domains
// folder and an optional --strict.
func parseDoctorArgs(args []string) (dir string, strict bool, err error) {
	for _, a := range args {
		switch {
		case a == "--strict":
			strict = true
		case strings.HasPrefix(a, "-"):
			return "", false, fmt.Errorf("unknown flag %q", a)
		default:
			if dir != "" {
				return "", false, fmt.Errorf("unexpected argument %q", a)
			}
			dir = a
		}
	}
	return dir, strict, nil
}

// parseManageArgs parses the args for `ls`/`info`: an optional positional host,
// an optional --ssh/--target override and an optional --json.
func parseManageArgs(args []string) (host, ssh string, asJSON bool, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--ssh" || a == "--target":
			i++
			if i >= len(args) {
				return "", "", false, fmt.Errorf("%s requires a value", a)
			}
			ssh = args[i]
		case strings.HasPrefix(a, "--ssh="):
			ssh = a[len("--ssh="):]
		case strings.HasPrefix(a, "--target="):
			ssh = a[len("--target="):]
		case a == "--json":
			asJSON = true
		case strings.HasPrefix(a, "-"):
			return "", "", false, fmt.Errorf("unknown flag %q", a)
		default:
			if host != "" {
				return "", "", false, fmt.Errorf("unexpected argument %q", a)
			}
			host = a
		}
	}
	return host, ssh, asJSON, nil
}

// watchAndReload watches domainsDir for filesystem changes and calls reload()
// with a 500 ms debounce so that bursts of writes (e.g. a ZIP extract) only
// trigger a single reload.
func watchAndReload(domainsDir string, reload func() error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("file watcher: failed to create: %v", err)
		return
	}

	// Recursively add all existing directories under domainsDir.
	_ = filepath.WalkDir(domainsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		return watcher.Add(path)
	})

	var (
		mu       sync.Mutex
		debounce *time.Timer
	)
	const delay = 500 * time.Millisecond

	go func() {
		defer watcher.Close()
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				// Watch newly created subdirectories so new sites are picked up.
				if event.Has(fsnotify.Create) {
					if info, statErr := os.Stat(event.Name); statErr == nil && info.IsDir() {
						_ = watcher.Add(event.Name)
					}
				}
				mu.Lock()
				if debounce != nil {
					debounce.Stop()
				}
				debounce = time.AfterFunc(delay, func() {
					log.Printf("file watcher: change detected, reloading")
					if reloadErr := reload(); reloadErr != nil {
						log.Printf("file watcher: reload error: %v", reloadErr)
					}
				})
				mu.Unlock()
			case watchErr, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("file watcher: %v", watchErr)
			}
		}
	}()
}
