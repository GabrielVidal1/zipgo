package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tree builds a domains folder from a map of relative path → file contents. A
// path ending in "/" is created as an empty directory.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, content := range files {
		full := filepath.Join(root, p)
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// testHash is a real bcrypt hash (`htpasswd -nbB claude s3cret`), so the tests
// exercise the same validation Caddy's comparison would.
const testHash = "$2y$05$lxJfaQMx3WaZsB7K5ZvLz.5Sw1mdcAx2fwJycu2oQJfxqLSySlTK."

// find returns the first finding whose message contains sub.
func find(rep Report, sub string) (Finding, bool) {
	for _, f := range rep.Findings {
		if strings.Contains(f.Msg, sub) {
			return f, true
		}
	}
	return Finding{}, false
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name string
		// files is the scratch domains folder.
		files map[string]string
		// wantMsg is a substring the report must contain ("" = no findings).
		wantMsg string
		// wantLevel is the level of that finding.
		wantLevel Level
		// wantHost, when set, is the host the finding must be attributed to.
		wantHost string
	}{
		{
			name: "clean tree has no findings",
			files: map[string]string{
				"example.com/index.html":            "<html></html>",
				"example.com/docs./index.html":      "<html></html>",
				"example.com/docs./api./index.html": "<html></html>",
			},
		},
		{
			name: "missing index.html on a leaf site is an error",
			files: map[string]string{
				"example.com/index.html":      "<html></html>",
				"example.com/docs./style.css": "body{}",
			},
			wantMsg:   "no index.html",
			wantLevel: Error,
			wantHost:  "docs.example.com",
		},
		{
			name: "a folder that only holds subdomains warns, not errors",
			files: map[string]string{
				"example.com/index.html":               "<html></html>",
				"example.com/game./tetris./index.html": "<html></html>",
			},
			wantMsg:   "no index.html",
			wantLevel: Warn,
			wantHost:  "game.example.com",
		},
		{
			name: "malformed .zipgoconfig.json is an error",
			files: map[string]string{
				"example.com/index.html":        "<html></html>",
				"example.com/.zipgoconfig.json": `{"enable": tru}`,
			},
			wantMsg:   "not valid JSON",
			wantLevel: Error,
			wantHost:  "example.com",
		},
		{
			name: "unknown config key warns with a suggestion",
			files: map[string]string{
				"example.com/index.html":        "<html></html>",
				"example.com/.zipgoconfig.json": `{"enabled": false}`,
			},
			wantMsg:   `unknown key "enabled"`,
			wantLevel: Warn,
		},
		{
			name: "a valid headers map is accepted",
			files: map[string]string{
				"example.com/index.html": "<html></html>",
				"example.com/.zipgoconfig.json": `{"headers": {"Cache-Control": "public, max-age=3600",
					"Access-Control-Allow-Origin": "*", "X-Drop-Me": ""}}`,
			},
		},
		{
			name: "headers that is not an object is an error",
			files: map[string]string{
				"example.com/index.html":        "<html></html>",
				"example.com/.zipgoconfig.json": `{"headers": "Cache-Control: no-store"}`,
			},
			wantMsg:   `"headers" must be an object`,
			wantLevel: Error,
			wantHost:  "example.com",
		},
		{
			name: "a non-string header value is an error",
			files: map[string]string{
				"example.com/index.html":        "<html></html>",
				"example.com/.zipgoconfig.json": `{"headers": {"Cache-Control": 3600}}`,
			},
			wantMsg:   `"headers" must be an object`,
			wantLevel: Error,
		},
		{
			name: "an unusable header name is an error",
			files: map[string]string{
				"example.com/index.html":        "<html></html>",
				"example.com/.zipgoconfig.json": `{"headers": {"Cache Control": "no-store"}}`,
			},
			wantMsg:   `header name "Cache Control" is not usable`,
			wantLevel: Error,
			wantHost:  "example.com",
		},
		{
			name: "a newline in a header value is an error",
			files: map[string]string{
				"example.com/index.html":        "<html></html>",
				"example.com/.zipgoconfig.json": `{"headers": {"X-A": "a\nSet-Cookie: b=1"}}`,
			},
			wantMsg:   `value of header "X-A" is not usable`,
			wantLevel: Error,
		},
		{
			name: "overriding a server-computed header warns",
			files: map[string]string{
				"example.com/index.html":        "<html></html>",
				"example.com/.zipgoconfig.json": `{"headers": {"content-length": "10"}}`,
			},
			wantMsg:   `is computed by the server`,
			wantLevel: Warn,
		},
		{
			name: "domain folder without a dot is ignored, with a warning",
			files: map[string]string{
				"example.com/index.html": "<html></html>",
				"mysite/index.html":      "<html></html>",
			},
			wantMsg:   `folder "mysite" is not a domain`,
			wantLevel: Warn,
		},
		{
			name: "invalid characters in a domain folder are an error",
			files: map[string]string{
				"my site.com/index.html": "<html></html>",
			},
			wantMsg:   "not a valid domain",
			wantLevel: Error,
		},
		{
			name: "subdomain folder missing its trailing dot warns",
			files: map[string]string{
				"example.com/index.html":                  "<html></html>",
				"example.com/docs.example.com/index.html": "<html></html>",
			},
			wantMsg:   "looks like a subdomain but has no trailing dot",
			wantLevel: Warn,
		},
		{
			name: "ordinary content folders do not warn",
			files: map[string]string{
				"example.com/index.html":      "<html></html>",
				"example.com/assets/app.js":   "1",
				"example.com/images/logo.png": "x",
			},
		},
		{
			name: "version-ish folder names do not warn",
			files: map[string]string{
				"example.com/index.html": "<html></html>",
				"example.com/v1.2/a.txt": "x",
			},
		},
		{
			name: "disabled site is not checked for content",
			files: map[string]string{
				"example.com/index.html":             "<html></html>",
				"example.com/wip./.zipgoconfig.json": `{"enable": false}`,
			},
		},
		{
			name: "a proxied site needs no index.html",
			files: map[string]string{
				"example.com/index.html":             "<html></html>",
				"example.com/api./.zipgoconfig.json": `{"rewrite": "localhost:8080"}`,
			},
		},
		{
			name: "an unusable rewrite upstream is an error",
			files: map[string]string{
				"example.com/index.html":             "<html></html>",
				"example.com/api./.zipgoconfig.json": `{"rewrite": "ftp://box"}`,
			},
			wantMsg:   "not usable",
			wantLevel: Error,
			wantHost:  "api.example.com",
		},
		{
			name: "a redirecting site needs no index.html",
			files: map[string]string{
				"example.com/index.html":             "<html></html>",
				"example.com/old./.zipgoconfig.json": `{"redirect": "https://elsewhere.example"}`,
			},
		},
		{
			name: "a redirect with an explicit status is clean",
			files: map[string]string{
				"example.com/index.html":             "<html></html>",
				"example.com/old./.zipgoconfig.json": `{"redirect": "https://elsewhere.example/moved", "redirectStatus": 301}`,
			},
		},
		{
			name: "a redirect target without a scheme is an error",
			files: map[string]string{
				"example.com/index.html":             "<html></html>",
				"example.com/old./.zipgoconfig.json": `{"redirect": "elsewhere.example"}`,
			},
			wantMsg:   "redirect target",
			wantLevel: Error,
			wantHost:  "old.example.com",
		},
		{
			name: "a redirect to a path would loop and is an error",
			files: map[string]string{
				"example.com/index.html":             "<html></html>",
				"example.com/old./.zipgoconfig.json": `{"redirect": "/new"}`,
			},
			wantMsg:   "infinite loop",
			wantLevel: Error,
			wantHost:  "old.example.com",
		},
		{
			name: "an unsupported redirect scheme is an error",
			files: map[string]string{
				"example.com/index.html":             "<html></html>",
				"example.com/old./.zipgoconfig.json": `{"redirect": "ftp://elsewhere.example"}`,
			},
			wantMsg:   "unsupported scheme",
			wantLevel: Error,
			wantHost:  "old.example.com",
		},
		{
			name: "a non-redirect status code is an error",
			files: map[string]string{
				"example.com/index.html":             "<html></html>",
				"example.com/old./.zipgoconfig.json": `{"redirect": "https://elsewhere.example", "redirectStatus": 200}`,
			},
			wantMsg:   "redirectStatus 200 is not a redirect status code",
			wantLevel: Error,
			wantHost:  "old.example.com",
		},
		{
			name: "redirect combined with rewrite is an error",
			files: map[string]string{
				"example.com/index.html": "<html></html>",
				"example.com/old./.zipgoconfig.json": `{"redirect": "https://elsewhere.example",` +
					`"rewrite": "localhost:8080"}`,
			},
			wantMsg:   `"redirect" and "rewrite" are both set`,
			wantLevel: Error,
			wantHost:  "old.example.com",
		},
		{
			name: "redirect over a folder that still has content warns",
			files: map[string]string{
				"example.com/index.html":             "<html></html>",
				"example.com/old./index.html":        "<html>old site</html>",
				"example.com/old./.zipgoconfig.json": `{"redirect": "https://elsewhere.example"}`,
			},
			wantMsg:   "its content is never served",
			wantLevel: Warn,
			wantHost:  "old.example.com",
		},
		{
			name: "redirectStatus without a redirect warns",
			files: map[string]string{
				"example.com/index.html":        "<html></html>",
				"example.com/.zipgoconfig.json": `{"redirectStatus": 301}`,
			},
			wantMsg:   "has no effect without a \"redirect\" target",
			wantLevel: Warn,
			wantHost:  "example.com",
		},
		{
			name: "redirect and redirectStatus are known keys",
			files: map[string]string{
				"example.com/index.html":             "<html></html>",
				"example.com/old./.zipgoconfig.json": `{"redirect": "https://elsewhere.example", "redirectStatus": 308}`,
			},
		},
		{
			name: "two folders claiming the same host is an error",
			files: map[string]string{
				"example.com/index.html":       "<html></html>",
				"example.com/a.b./index.html":  "<html></html>",
				"example.com/b./a./index.html": "<html></html>",
			},
			wantMsg:   "claimed by 2 folders",
			wantLevel: Error,
			wantHost:  "a.b.example.com",
		},
		{
			name: "a valid basicAuth hash is clean",
			files: map[string]string{
				"example.com/index.html":                 "<html></html>",
				"example.com/staging./index.html":        "<html></html>",
				"example.com/staging./.zipgoconfig.json": `{"basicAuth": {"claude": "` + testHash + `"}}`,
			},
		},
		{
			name: "a plaintext basicAuth password is an error",
			files: map[string]string{
				"example.com/index.html":                 "<html></html>",
				"example.com/staging./index.html":        "<html></html>",
				"example.com/staging./.zipgoconfig.json": `{"basicAuth": {"claude": "hunter2"}}`,
			},
			wantMsg:   "looks like a plaintext password",
			wantLevel: Error,
			wantHost:  "staging.example.com",
		},
		{
			name: "a truncated bcrypt hash is an error",
			files: map[string]string{
				"example.com/index.html":                 "<html></html>",
				"example.com/staging./index.html":        "<html></html>",
				"example.com/staging./.zipgoconfig.json": `{"basicAuth": {"claude": "$2y$05$tooshort"}}`,
			},
			wantMsg:   "not a valid bcrypt hash",
			wantLevel: Error,
			wantHost:  "staging.example.com",
		},
		{
			name: "an empty username is an error",
			files: map[string]string{
				"example.com/index.html":                 "<html></html>",
				"example.com/staging./index.html":        "<html></html>",
				"example.com/staging./.zipgoconfig.json": `{"basicAuth": {"": "` + testHash + `"}}`,
			},
			wantMsg:   "empty username",
			wantLevel: Error,
			wantHost:  "staging.example.com",
		},
		{
			name: "an empty basicAuth block warns that the site is public",
			files: map[string]string{
				"example.com/index.html":                 "<html></html>",
				"example.com/staging./index.html":        "<html></html>",
				"example.com/staging./.zipgoconfig.json": `{"basicAuth": {}}`,
			},
			wantMsg:   "is empty",
			wantLevel: Warn,
			wantHost:  "staging.example.com",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := tree(t, tc.files)
			rep, err := Check(root)
			if err != nil {
				t.Fatalf("Check: %v", err)
			}

			if tc.wantMsg == "" {
				if len(rep.Findings) != 0 {
					t.Fatalf("expected a clean report, got %d findings: %+v",
						len(rep.Findings), rep.Findings)
				}
				if !rep.OK(true) {
					t.Errorf("clean report should be OK even in strict mode")
				}
				return
			}

			f, ok := find(rep, tc.wantMsg)
			if !ok {
				t.Fatalf("no finding containing %q; got %+v", tc.wantMsg, rep.Findings)
			}
			if f.Level != tc.wantLevel {
				t.Errorf("level = %v, want %v (finding: %s)", f.Level, tc.wantLevel, f.Msg)
			}
			if tc.wantHost != "" && f.Host != tc.wantHost {
				t.Errorf("host = %q, want %q", f.Host, tc.wantHost)
			}
			if tc.wantLevel == Error && rep.OK(false) {
				t.Errorf("report with an error must not be OK")
			}
			if tc.wantLevel == Warn && !rep.OK(false) {
				t.Errorf("report with only warnings must be OK in non-strict mode")
			}
			if tc.wantLevel == Warn && rep.OK(true) {
				t.Errorf("report with warnings must not be OK in strict mode")
			}
		})
	}
}

func TestCheckMissingFolder(t *testing.T) {
	_, err := Check(filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("expected an error for a missing domains folder")
	}
}

func TestCheckCountsSites(t *testing.T) {
	root := tree(t, map[string]string{
		"example.com/index.html":             "<html></html>",
		"example.com/docs./index.html":       "<html></html>",
		"example.com/wip./.zipgoconfig.json": `{"enable": false}`,
		"other.org/index.html":               "<html></html>",
	})
	rep, err := Check(root)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Domains != 2 {
		t.Errorf("Domains = %d, want 2", rep.Domains)
	}
	if rep.Sites != 3 { // apex + docs + other.org apex
		t.Errorf("Sites = %d, want 3", rep.Sites)
	}
	if rep.Disabled != 1 {
		t.Errorf("Disabled = %d, want 1", rep.Disabled)
	}
}

func TestInvalidUpstream(t *testing.T) {
	ok := []string{"localhost:8080", "http://api.example.com", "https://api.example.com:8443", "box"}
	for _, u := range ok {
		if bad := invalidUpstream(u); bad != "" {
			t.Errorf("invalidUpstream(%q) = %q, want ok", u, bad)
		}
	}
	bad := []string{"", " localhost:8080", "ftp://box", "/var/www", "http://"}
	for _, u := range bad {
		if invalidUpstream(u) == "" {
			t.Errorf("invalidUpstream(%q) = ok, want a reason", u)
		}
	}
}

func TestInvalidBcrypt(t *testing.T) {
	tests := []struct {
		name string
		hash string
		ok   bool
	}{
		{"htpasswd -nbB hash ($2y$)", testHash, true},
		{"caddy hash-password hash ($2a$)", "$2a$14$Zkx19XLiW6VYouLHR5NmfOFU0z2GTNmpkT/5qqR7hx4IjWJPDhjvG", true},
		{"$2b$ variant", "$2b$12$k7t1FaIvJm0jRZDBqAY1yeSw.RVUeYZgtIB8QGDsi/pDPNQCoQ3RC", true},
		{"empty", "", false},
		{"plaintext password", "hunter2", false},
		{"plaintext that starts with a dollar", "$uper$ecret", false},
		{"truncated hash", "$2y$05$tooshort", false},
		{"bogus cost", "$2y$99$lxJfaQMx3WaZsB7K5ZvLz.5Sw1mdcAx2fwJycu2oQJfxqLSySlTK.", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bad := invalidBcrypt(tc.hash)
			if tc.ok && bad != "" {
				t.Errorf("invalidBcrypt(%q) = %q, want ok", tc.hash, bad)
			}
			if !tc.ok && bad == "" {
				t.Errorf("invalidBcrypt(%q) = ok, want a reason", tc.hash)
			}
		})
	}
}
