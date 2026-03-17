package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/fileserver"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/rewrite"
	_ "github.com/caddyserver/caddy/v2/modules/caddytls"
	_ "github.com/caddyserver/caddy/v2/modules/caddytls/standardstek"

	"sitehost/internal/backoffice"
	"sitehost/internal/builder"
	"sitehost/internal/config"
	"sitehost/internal/sites"
)

const backofficeInternalPort = "9876"

func main() {
	appsDir := "apps"
	if len(os.Args) > 1 {
		appsDir = os.Args[1]
	}

	// ---- root domain (empty = localhost mode) ----
	rootDomain, err := config.ReadRootDomain(appsDir)
	if err != nil {
		log.Fatalf("❌  %v\n", err)
	}

	// ---- credentials ----
	boUser := envOr("SITEHOST_USER", "admin")
	boPass := envOr("SITEHOST_PASS", "")
	if boPass == "" {
		log.Fatal("❌  Set SITEHOST_PASS before starting.\n    Example: export SITEHOST_PASS=changeme")
	}

	backofficeAddr := "127.0.0.1:" + backofficeInternalPort

	// ---- reload: re-discover apps and push new config to Caddy ----
	reload := func() error {
		disc, err := sites.Discover(appsDir)
		if err != nil {
			return err
		}
		var cfg *caddy.Config
		if builder.IsLocalhost(rootDomain) {
			cfg, err = builder.BuildLocalhostConfig(disc, backofficeAddr)
		} else {
			cfg, err = builder.BuildConfig(rootDomain, disc, backofficeAddr)
		}
		if err != nil {
			return err
		}
		return caddy.Run(cfg)
	}

	// ---- urlFor: compute public URL for a site name on demand ----
	urlFor := func(name string) string {
		disc, _ := sites.Discover(appsDir)
		if builder.IsLocalhost(rootDomain) {
			// port 9000 = root, real sites start at 9001
			for i, s := range disc {
				if s.Name == name {
					return fmt.Sprintf("http://localhost:%d", builder.LocalhostStartPort+1+i)
				}
			}
			return ""
		}
		for _, s := range disc {
			if s.Name == name {
				return "https://" + s.Host(rootDomain)
			}
		}
		return ""
	}

	// ---- start backoffice HTTP server on loopback ----
	boHandler := backoffice.Handler(appsDir, boUser, boPass, reload, urlFor)
	boListener, err := net.Listen("tcp", backofficeAddr)
	if err != nil {
		log.Fatalf("❌  Could not bind internal port %s: %v\n", backofficeInternalPort, err)
	}
	boServer := &http.Server{Handler: boHandler}
	go func() {
		if err := boServer.Serve(boListener); err != nil && err != http.ErrServerClosed {
			log.Fatalf("❌  Backoffice server error: %v\n", err)
		}
	}()

	// ---- discover sites ----
	discovered, err := sites.Discover(appsDir)
	if err != nil {
		log.Fatalf("❌  %v\n", err)
	}

	// ---- build config and print summary ----
	var cfg *caddy.Config
	hasRoot := builder.HasRootSite(discovered)

	if builder.IsLocalhost(rootDomain) {
		fmt.Println("🖥️   Localhost mode (no apps/root.txt found)")
		fmt.Printf("📁  Sites found : %d\n\n", len(discovered))

		// Port 9000 is always root
		if hasRoot {
			fmt.Printf("   [static]  http://localhost:%-5d  →  root\n", builder.LocalhostStartPort)
		} else {
			fmt.Printf("   [land ]  http://localhost:%-5d  →  (landing page)\n", builder.LocalhostStartPort)
		}

		// Real sites start at 9001
		for i, s := range discovered {
			if s.Name == "root" {
				continue
			}
			port := builder.LocalhostStartPort + 1 + i
			kind := "static"
			if s.IsSPA {
				kind = "spa"
			}
			fmt.Printf("   [%s]  http://localhost:%-5d  →  %s\n", kind, port, s.Name)
		}
		fmt.Printf("   [ui  ]  http://localhost:%d    →  backoffice\n\n", builder.BackofficeLocalhostPort)

		cfg, err = builder.BuildLocalhostConfig(discovered, backofficeAddr)
	} else {
		fmt.Printf("🌐  Root domain : %s\n", rootDomain)
		fmt.Printf("📁  Sites found : %d\n\n", len(discovered))

		if !hasRoot {
			fmt.Printf("   [land ]  https://%s  →  (landing page)\n", rootDomain)
		}
		for _, s := range discovered {
			kind := "static"
			if s.IsSPA {
				kind = "spa"
			}
			fmt.Printf("   [%s]  https://%s\n", kind, s.Host(rootDomain))
		}
		fmt.Printf("   [ui  ]  https://%s  (backoffice)\n\n", builder.BackofficeHost(rootDomain))

		cfg, err = builder.BuildConfig(rootDomain, discovered, backofficeAddr)
	}
	if err != nil {
		log.Fatalf("❌  Could not build config: %v\n", err)
	}

	// ---- start Caddy ----
	if builder.IsLocalhost(rootDomain) {
		fmt.Println("🚀  Starting server (HTTP, no TLS)...")
	} else {
		fmt.Println("🚀  Starting Caddy (HTTPS via Let's Encrypt)...")
	}
	if err := caddy.Run(cfg); err != nil {
		log.Fatalf("❌  %v\n", err)
	}
	fmt.Println("✅  Live. Ctrl+C to stop.")

	// ---- wait for signal ----
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	fmt.Println("\n🛑  Shutting down...")
	boServer.Shutdown(context.Background())
	caddy.Stop()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
