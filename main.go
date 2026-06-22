package main

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
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
	"zipgo/internal/service"
	"zipgo/internal/sites"
)

const deployUsage = `Usage: zipgo deploy <dir> -d <subdomains>.<domain> [-d ...] --ssh user@host:/base/path

Recursively creates the domain/subdomain folder tree (zipgo's trailing-dot
convention) on the remote host and rsyncs <dir>'s contents into it.

  -d, --domain   target host, e.g. love-letters.game.gabvdl.xyz (repeatable)
      --ssh      remote destination: user@host:/base/domains/path
      --exclude  rsync exclude pattern (repeatable)
      --no-delete  do not mirror (keep remote files missing from <dir>)
  -n, --dry-run  show what rsync would do; skip remote mkdir

Example:
  zipgo deploy dist/ -d love-letters.game.gabvdl.xyz \
      --ssh gabrielvidal@100.74.118.12:/home/gabrielvidal/services/domains
  # -> /home/gabrielvidal/services/domains/gabvdl.xyz/game./love-letters.
  #    served at https://love-letters.game.gabvdl.xyz
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
		if err := deploy.Run(opts); err != nil {
			log.Fatalf("❌  %v\n", err)
		}
		return
	case "help", "--help", "-h":
		fmt.Println("Usage: zipgo [command]")
		fmt.Println()
		fmt.Println("Commands:")
		fmt.Println("  serve    Start the server (default)")
		fmt.Println("  deploy   rsync a local dir to a remote zipgo host over SSH")
		fmt.Println("  enable   Install and start the systemd user service")
		fmt.Println("  disable  Stop and remove the systemd user service")
		fmt.Println("  status   Show service status")
		fmt.Println()
		fmt.Print(deployUsage)
		return
	case "serve", "":
		// fall through to server startup
	default:
		log.Fatalf("❌  Unknown command %q. Run 'zipgo help' for usage.\n", sub)
	}

	domainsDir := os.Getenv("ZIPGO_DOMAINS_FOLDER")
	if domainsDir == "" {
		domainsDir = ".zipgo"
	}

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
				kind := "static"
				if s.IsSPA {
					kind = "spa   "
				}
				fmt.Printf("   [%s]  http://localhost:%d%s\n", kind, builder.LocalhostStartPort, s.LocalhostPath(ds.Domain))
			}
		}
		fmt.Println()
		cfg, err = builder.BuildLocalhostConfig(domainSites, metricsAddr)
	} else {
		for _, ds := range domainSites {
			fmt.Printf("🌐  Domain : %s (%d sites)\n", ds.Domain, len(ds.Sites))
			for _, s := range ds.Sites {
				kind := "static"
				if s.IsSPA {
					kind = "spa   "
				}
				fmt.Printf("   [%s]  https://%s\n", kind, s.Host(ds.Domain))
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
