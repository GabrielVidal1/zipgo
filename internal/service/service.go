package service

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"
	"time"
)

const unitName = "zipgo.service"

var unitTmpl = template.Must(template.New("unit").Parse(`[Unit]
Description=zipgo static site host
After=network-online.target

[Service]
ExecStart={{.Exec}} serve
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`))

func unitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", unitName), nil
}

func Enable() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot determine executable path: %w", err)
	}

	path, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating systemd user dir: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("writing unit file: %w", err)
	}
	defer f.Close()
	if err := unitTmpl.Execute(f, struct{ Exec string }{exe}); err != nil {
		return err
	}

	if err := run("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "--user", "enable", "--now", "zipgo"); err != nil {
		return err
	}
	fmt.Println("✅  zipgo service enabled and started.")
	fmt.Printf("    Unit: %s\n", path)
	fmt.Println("    Logs: journalctl --user -u zipgo -f")
	return nil
}

func Disable() error {
	if err := run("systemctl", "--user", "disable", "--now", "zipgo"); err != nil {
		return err
	}
	path, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing unit file: %w", err)
	}
	if err := run("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	fmt.Println("✅  zipgo service disabled and stopped.")
	return nil
}

func Status() error {
	fmt.Println("── systemd unit ─────────────────────────────")
	_ = run("systemctl", "--user", "status", "--no-pager", "zipgo")

	fmt.Println("\n── server reachability ──────────────────────")
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:9876/healthz")
	if err != nil {
		fmt.Printf("   backoffice: unreachable (%v)\n", err)
	} else {
		resp.Body.Close()
		fmt.Printf("   backoffice: reachable (HTTP %d)\n", resp.StatusCode)
	}
	return nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
