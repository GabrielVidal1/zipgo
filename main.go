package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/fileserver"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/rewrite"
	_ "github.com/caddyserver/caddy/v2/modules/caddytls"
	_ "github.com/caddyserver/caddy/v2/modules/caddytls/standardstek"

	"zipgo/internal/backoffice"
	"zipgo/internal/builder"
	"zipgo/internal/config"
	"zipgo/internal/sites"
)

const backofficeInternalPort = "9876"

func main() {
	domainsDir := "domains"
	if len(os.Args) > 1 {
		domainsDir = os.Args[1]
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

	// ---- credentials ----
	boUser := envOr("ZIPGO_USER", "admin")
	boPass := envOr("ZIPGO_PASS", "")
	if boPass == "" {
		boPass, err = generatePassword(16)
		if err != nil {
			log.Fatalf("❌  Could not generate admin password: %v\n", err)
		}
		fmt.Printf("🔑  Generated admin password: %s\n    (set ZIPGO_PASS to set it instead)\n\n", boPass)
	}

	backofficeAddr := "127.0.0.1:" + backofficeInternalPort

	// ---- vince analytics sidecar ----
	vinceURL := ""
	if !isLocalhost && len(domains) > 0 {
		vinceURL = "https://analytics." + domains[0]
	} else {
		vinceURL = "http://localhost:8898"
	}
	vinceCmd := startVinceSidecar(vinceURL)

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
			cfg, err = builder.BuildLocalhostConfig(domainSites, backofficeAddr)
		} else {
			cfg, err = builder.BuildConfig(domainSites, backofficeAddr)
		}
		if err != nil {
			return err
		}
		return caddy.Run(cfg)
	}

	// ---- urlFor: compute public URL for a site name on demand ----
	urlFor := func(domain, name string) string {
		if isLocalhost {
			prefix := fmt.Sprintf("http://localhost:%d/%s", builder.LocalhostStartPort, domain)
			if name != "root" {
				prefix += "/" + name
			}
			return prefix
		}
		s := sites.Site{Name: name}
		return "https://" + s.Host(domain)
	}

	// ---- start backoffice HTTP server on loopback ----
	boHandler := backoffice.Handler(domainsDir, boUser, boPass, reload, urlFor, vinceURL)
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
				path := "/" + ds.Domain
				if s.Name != "root" {
					path += "/" + s.Name
				}
				kind := "static"
				if s.IsSPA {
					kind = "spa   "
				}
				fmt.Printf("   [%s]  http://localhost:%d%s\n", kind, builder.LocalhostStartPort, path)
			}
		}
		fmt.Printf("   [ui    ]  http://localhost:%d  →  backoffice\n", builder.BackofficeLocalhostPort)
		if vinceCmd != nil {
			fmt.Printf("   [anly  ]  http://localhost:8898   →  analytics (vince)\n")
		}
		fmt.Println()
		cfg, err = builder.BuildLocalhostConfig(domainSites, backofficeAddr)
	} else {
		for _, ds := range domainSites {
			fmt.Printf("🌐  Domain : %s (%d sites)\n", ds.Domain, len(ds.Sites))
			if !builder.HasRootSite(ds.Sites) {
				fmt.Printf("   [land  ]  https://%s  →  (landing page)\n", ds.Domain)
			}
			for _, s := range ds.Sites {
				kind := "static"
				if s.IsSPA {
					kind = "spa   "
				}
				fmt.Printf("   [%s]  https://%s\n", kind, s.Host(ds.Domain))
			}
			fmt.Printf("   [ui    ]  https://%s  (backoffice)\n", builder.BackofficeHost(ds.Domain))
			if vinceCmd != nil {
				fmt.Printf("   [anly  ]  https://%s  (analytics)\n", builder.VinceHost(ds.Domain))
			}
		}
		fmt.Println()
		cfg, err = builder.BuildConfig(domainSites, backofficeAddr)
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

	// ---- wait for signal ----
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	fmt.Println("\n🛑  Shutting down...")
	boServer.Shutdown(context.Background())
	caddy.Stop()
	if vinceCmd != nil {
		vinceCmd.Process.Signal(os.Interrupt)
		vinceCmd.Wait()
	}
}

// startVinceSidecar launches the vince binary as a background subprocess if:
//   - the vince binary exists in the same directory as the zipgo executable, AND
//   - VINCE_MANAGED is not set to "1" (which install.sh sets on the service
//     unit so vince isn't double-started when a system manager owns it).
//
// It passes --url so Vince knows its own public address for link generation.
// Returns the running *exec.Cmd, or nil if Vince was not started.
func startVinceSidecar(vinceURL string) *exec.Cmd {
	// Service manager already owns vince — don't double-start.
	// if os.Getenv("VINCE_MANAGED") == "1" {
	// 	return nil
	// }

	self, err := os.Executable()
	if err != nil {
		return nil
	}
	vinceExe := filepath.Join(filepath.Dir(self), "vince")
	if _, err := os.Stat(vinceExe); os.IsNotExist(err) {
		return nil // vince binary not present — skip silently
	}

	vinceData := filepath.Join(filepath.Dir(self), "vince-data")

	cmd := exec.Command(vinceExe, "serve",
		"--data", vinceData,
		"--listen", "127.0.0.1:8899",
		"--url", vinceURL,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		log.Printf("⚠️  Could not start Vince sidecar: %v\n", err)
		return nil
	}
	fmt.Printf("📊  Vince analytics sidecar started (pid %d)\n", cmd.Process.Pid)
	return cmd
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// generatePassword returns a cryptographically random URL-safe password of the
// requested byte length (the base64 output will be slightly longer).
func generatePassword(byteLen int) (string, error) {
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}