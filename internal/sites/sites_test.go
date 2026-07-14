package sites

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadConfig(t *testing.T) {
	dir := t.TempDir()

	// Missing file → zero-value config, no error, defaults applied.
	c, err := readConfig(dir)
	if err != nil {
		t.Fatalf("missing config: unexpected error %v", err)
	}
	if !c.Enabled() || c.HTTPAllowed() || c.Rewrite != "" {
		t.Fatalf("missing config: want defaults, got %+v", c)
	}

	// Present file → parsed.
	write(t, dir, `{"enable": false, "rewrite": "localhost:8080", "allowHttp": true}`)
	c, err = readConfig(dir)
	if err != nil {
		t.Fatalf("present config: unexpected error %v", err)
	}
	if c.Enabled() {
		t.Error("enable:false should disable the site")
	}
	if !c.HTTPAllowed() {
		t.Error("allowHttp:true should allow HTTP")
	}
	if c.Rewrite != "localhost:8080" {
		t.Errorf("rewrite: got %q", c.Rewrite)
	}

	// Malformed JSON → error surfaced.
	write(t, dir, `{not json`)
	if _, err := readConfig(dir); err == nil {
		t.Fatal("malformed config should return an error")
	}
}

func TestRedirectConfig(t *testing.T) {
	dir := t.TempDir()

	// No redirect → empty target, and the default status is still reported.
	c, err := readConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Redirect != "" {
		t.Errorf("redirect: want empty, got %q", c.Redirect)
	}

	// redirect without redirectStatus → DefaultRedirectStatus (302).
	write(t, dir, `{"redirect": "https://elsewhere.example"}`)
	c, err = readConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Redirect != "https://elsewhere.example" {
		t.Errorf("redirect: got %q", c.Redirect)
	}
	if got := c.RedirectCode(); got != DefaultRedirectStatus {
		t.Errorf("RedirectCode: want %d, got %d", DefaultRedirectStatus, got)
	}

	// An explicit redirectStatus wins.
	write(t, dir, `{"redirect": "https://elsewhere.example", "redirectStatus": 301}`)
	c, err = readConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.RedirectCode(); got != 301 {
		t.Errorf("RedirectCode: want 301, got %d", got)
	}
}

func TestReadConfigHeaders(t *testing.T) {
	dir := t.TempDir()

	// No headers key → nil map, and the site is otherwise untouched.
	write(t, dir, `{"enable": true}`)
	c, err := readConfig(dir)
	if err != nil {
		t.Fatalf("no headers: unexpected error %v", err)
	}
	if len(c.Headers) != 0 {
		t.Fatalf("no headers: want empty, got %v", c.Headers)
	}

	// Headers map → parsed verbatim (canonicalisation happens in the builder).
	write(t, dir, `{"headers": {"Cache-Control": "public, max-age=3600", "x-hi": "there"}}`)
	c, err = readConfig(dir)
	if err != nil {
		t.Fatalf("headers: unexpected error %v", err)
	}
	if c.Headers["Cache-Control"] != "public, max-age=3600" || c.Headers["x-hi"] != "there" {
		t.Fatalf("headers not parsed: %v", c.Headers)
	}

	// Wrong shape (not an object of strings) → hard error, like any malformed
	// config: the typo surfaces instead of being silently dropped.
	for _, body := range []string{
		`{"headers": "no"}`,
		`{"headers": {"Cache-Control": 3600}}`,
		`{"headers": {"Bad Name": "x"}}`,
		`{"headers": {"X-Inject": "a\nb"}}`,
	} {
		write(t, dir, body)
		if _, err := readConfig(dir); err == nil {
			t.Errorf("%s should be rejected", body)
		}
	}
}

func TestValidateHeaders(t *testing.T) {
	if err := ValidateHeaders(map[string]string{"Cache-Control": "no-store", "X-Empty": ""}); err != nil {
		t.Errorf("valid headers rejected: %v", err)
	}
	for name, h := range map[string]map[string]string{
		"space in name": {"Cache Control": "no-store"},
		"colon in name": {"X-A:B": "v"},
		"empty name":    {"": "v"},
		"newline value": {"X-A": "a\r\nSet-Cookie: x"},
	} {
		if err := ValidateHeaders(h); err == nil {
			t.Errorf("%s: want error, got nil", name)
		}
	}
}

func TestDiscoverReadsConfig(t *testing.T) {
	dir := t.TempDir()
	domain := filepath.Join(dir, "example.com")
	sub := filepath.Join(domain, "api.")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, sub, `{"rewrite": "localhost:9999"}`)

	found, err := Discover(domain)
	if err != nil {
		t.Fatal(err)
	}
	var got *Site
	for i := range found {
		if len(found[i].Labels) == 1 && found[i].Labels[0] == "api" {
			got = &found[i]
		}
	}
	if got == nil {
		t.Fatal("api. subdomain not discovered")
	}
	if got.Config.Rewrite != "localhost:9999" {
		t.Fatalf("config not loaded during discovery: %+v", got.Config)
	}
}

func write(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
