package password

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"zipgo/internal/sites"
)

func TestParseAdminArgs(t *testing.T) {
	if o, err := ParseAdminArgs(strings.Fields("set admin.example.com --generate --days 30")); err != nil || o.Verb != "set" || !o.Generate || o.Days != 30 {
		t.Errorf("set: %+v %v", o, err)
	}
	for _, args := range []string{"status", "hash", "set", "frob admin.example.com"} {
		if _, err := ParseAdminArgs(strings.Fields(args)); err == nil {
			t.Errorf("%q should fail", args)
		}
	}
}

func TestRunAdmin(t *testing.T) {
	base := t.TempDir()
	store := Local{Base: base}
	file := filepath.Join(base, "example.com", "admin.", sites.ConfigFileName)
	var out bytes.Buffer

	o, _ := ParseAdminArgs(strings.Fields("set admin.example.com --stdin --days 30"))
	if err := RunAdmin(o, store, strings.NewReader("s3cret-pass\n"), &out); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := sites.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Admin || cfg.Password == nil || cfg.Password.SessionDays != 30 ||
		bcrypt.CompareHashAndPassword([]byte(cfg.Password.Hash), []byte("s3cret-pass")) != nil {
		t.Fatalf("config = %s", raw)
	}
	if !strings.Contains(out.String(), "back-office of example.com") {
		t.Errorf("output = %q", out.String())
	}

	o, _ = ParseAdminArgs(strings.Fields("remove admin.example.com"))
	if err := RunAdmin(o, store, nil, &out); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("remove without --yes: %v", err)
	}
	o, _ = ParseAdminArgs(strings.Fields("remove admin.example.com --yes"))
	if err := RunAdmin(o, store, nil, &out); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(file)
	if cfg, err := sites.ParseConfig(raw); err != nil || cfg.Admin || cfg.Password != nil {
		t.Errorf("after remove: %s", raw)
	}
}
