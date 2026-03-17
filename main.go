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
	// vinceURL is the public-facing URL of the Vince instance, used both to
	// configure the Caddy reverse-proxy route and to inject the <script> tag
	// into uploaded HTML files.
	vinceURL := ""
	if rootDomain != "" {
		vinceURL = "https://analytics." + rootDomain
	} else {
		vinceURL = "http://localhost:8898"
	}

	// Start vince as a subprocess unless a service manager already owns it
	// (VINCE_MANAGED=1, set by the systemd/launchd units from install.sh).
	vinceCmd := startVinceSidecar(rootDomain, vinceURL)

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
	boHandler := backoffice.Handler(appsDir, boUser, boPass, reload, urlFor, vinceURL, rootDomain)
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

		if hasRoot {
			fmt.Printf("   [static]  http://localhost:%-5d  →  root\n", builder.LocalhostStartPort)
		} else {
			fmt.Printf("   [land ]   http://localhost:%-5d  →  (landing page)\n", builder.LocalhostStartPort)
		}
		for i, s := range discovered {
			if s.Name == "root" {
				continue
			}
			port := builder.LocalhostStartPort + 1 + i
			kind := "static"
			if s.IsSPA {
				kind = "spa"
			}
			fmt.Printf("   [%s]   http://localhost:%-5d  →  %s\n", kind, port, s.Name)
		}
		fmt.Printf("   [ui  ]   http://localhost:%d    →  backoffice\n", builder.BackofficeLocalhostPort)
		if vinceCmd != nil {
			fmt.Printf("   [anly]   http://localhost:8898    →  analytics (vince)\n")
		}
		fmt.Println()

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
		fmt.Printf("   [ui  ]  https://%s  (backoffice)\n", builder.BackofficeHost(rootDomain))
		if vinceCmd != nil {
			fmt.Printf("   [anly]  https://%s  (analytics)\n", builder.VinceHost(rootDomain))
		}
		fmt.Println()

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
func startVinceSidecar(rootDomain, vinceURL string) *exec.Cmd {
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