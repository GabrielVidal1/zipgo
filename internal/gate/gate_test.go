package gate

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"golang.org/x/crypto/bcrypt"
)

const password = "ilikeballance"

var testServerKey = []byte("0123456789abcdef0123456789abcdef")

func hashOf(t *testing.T, pw string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(h)
}

// clock is a settable time source.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// newGate builds a provisioned gate for a fresh host (the throttle registry is
// global, keyed by host, so every test gets its own).
func newGate(t *testing.T, prefix string) (*Gate, *clock) {
	t.Helper()
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	g := &Gate{
		Host:        strings.ToLower(strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())) + ".example.com",
		Hash:        hashOf(t, password),
		Title:       "Ballance",
		SessionDays: 7,
		Prefix:      prefix,
		Insecure:    prefix != "",
		now:         c.now,
	}
	g.setup(testServerKey)
	return g, c
}

// site is the protected content behind the gate.
var site = caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
	w.Write([]byte("SECRET CONTENT"))
	return nil
})

func serve(g *Gate, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	if err := g.ServeHTTP(w, r, site); err != nil {
		panic(err)
	}
	return w
}

func login(g *Gate, pw, next, ip string) *httptest.ResponseRecorder {
	form := url.Values{"password": {pw}, "next": {next}}
	r := httptest.NewRequest(http.MethodPost, g.Prefix+LoginPath, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = ip + ":40000"
	return serve(g, r)
}

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if strings.Contains(c.Name, "zipgo_session") {
			return c
		}
	}
	t.Fatalf("no session cookie in %v", w.Header())
	return nil
}

func TestAnonymousGetsTheLoginPageEverywhere(t *testing.T) {
	g, _ := newGate(t, "")
	for _, target := range []string{"/", "/index.html", "/assets/app.js", "/sub-domains-meta", "/missing?x=1"} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
			w := serve(g, httptest.NewRequest(method, target, nil))
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: status %d, want 401", method, target, w.Code)
			}
			body := w.Body.String()
			if strings.Contains(body, "SECRET") {
				t.Fatalf("%s %s leaked the site", method, target)
			}
			if method == http.MethodHead {
				continue
			}
			for _, want := range []string{"<title>Ballance</title>", `action="/__zipgo/login"`, `name="robots" content="noindex`} {
				if !strings.Contains(body, want) {
					t.Errorf("%s %s: page lacks %q", method, target, want)
				}
			}
			for k, v := range pageHeaders {
				if got := w.Header().Get(k); got != v {
					t.Errorf("%s %s: %s = %q, want %q", method, target, k, got, v)
				}
			}
		}
	}
}

func TestLoginFlow(t *testing.T) {
	g, clk := newGate(t, "")

	// The page remembers where the visitor was going.
	w := serve(g, httptest.NewRequest(http.MethodGet, "/levels/3?hard=1", nil))
	if !strings.Contains(w.Body.String(), `value="/levels/3?hard=1"`) {
		t.Fatalf("next not carried: %s", w.Body.String())
	}

	// Wrong password: same page, an error, no cookie.
	w = login(g, "nope", "/levels/3", "198.51.100.1")
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "Wrong password.") {
		t.Fatalf("wrong password: %d %s", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("wrong password set a cookie")
	}

	// Right password: a 303 back where they were going, with the session.
	w = login(g, password, "/levels/3", "198.51.100.1")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/levels/3" {
		t.Fatalf("login: %d → %q", w.Code, w.Header().Get("Location"))
	}
	c := sessionCookie(t, w)
	if c.Name != "__Host-zipgo_session" || !c.Secure || !c.HttpOnly || c.Path != "/" ||
		c.SameSite != http.SameSiteLaxMode || c.MaxAge != 7*86400 {
		t.Fatalf("cookie attributes: %+v", c)
	}

	// With it, the site.
	r := httptest.NewRequest(http.MethodGet, "/levels/3", nil)
	r.AddCookie(c)
	if w = serve(g, r); w.Code != http.StatusOK || w.Body.String() != "SECRET CONTENT" {
		t.Fatalf("with session: %d %q", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("a fresh session was renewed")
	}

	// Logout clears it.
	w = serve(g, httptest.NewRequest(http.MethodGet, LogoutPath, nil))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("logout: %d → %q", w.Code, w.Header().Get("Location"))
	}
	if out := sessionCookie(t, w); out.MaxAge >= 0 || out.Value != "" {
		t.Fatalf("logout cookie: %+v", out)
	}

	// The session ends after its lifetime.
	clk.t = clk.t.Add(7*24*time.Hour + time.Second)
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(c)
	if w = serve(g, r); w.Code != http.StatusUnauthorized {
		t.Fatalf("expired session: %d", w.Code)
	}
}

// A session in use is renewed once a day, so a regular visitor never meets the
// login page again; one that isn't used runs out.
func TestSessionRenewal(t *testing.T) {
	g, clk := newGate(t, "")
	c := sessionCookie(t, login(g, password, "/", "198.51.100.2"))

	clk.t = clk.t.Add(6 * 24 * time.Hour) // day 6 of 7
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(c)
	w := serve(g, r)
	if w.Code != http.StatusOK {
		t.Fatalf("day 6: %d", w.Code)
	}
	renewed := sessionCookie(t, w)

	clk.t = clk.t.Add(6 * 24 * time.Hour) // day 12: the old cookie is dead, the renewed one isn't
	for _, tc := range []struct {
		c    *http.Cookie
		want int
	}{{c, http.StatusUnauthorized}, {renewed, http.StatusOK}} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(tc.c)
		if got := serve(g, r).Code; got != tc.want {
			t.Errorf("day 12: %d, want %d", got, tc.want)
		}
	}
}

// A cookie only opens the site and the password it was issued for.
func TestCookieScope(t *testing.T) {
	g, _ := newGate(t, "")
	c := sessionCookie(t, login(g, password, "/", "198.51.100.3"))

	tests := []struct {
		name string
		gate func() *Gate
	}{
		{"another host", func() *Gate {
			o := &Gate{Host: "other.example.com", Hash: g.Hash, now: g.now}
			o.setup(testServerKey)
			return o
		}},
		{"rotated password", func() *Gate {
			o := &Gate{Host: g.Host, Hash: hashOf(t, password), now: g.now} // same password, new salt
			o.setup(testServerKey)
			return o
		}},
		{"another server key", func() *Gate {
			o := &Gate{Host: g.Host, Hash: g.Hash, now: g.now}
			o.setup([]byte("ffffffffffffffffffffffffffffffff"))
			return o
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.AddCookie(c)
			if got := serve(tc.gate(), r).Code; got != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401", got)
			}
		})
	}
}

func TestForgedCookies(t *testing.T) {
	g, clk := newGate(t, "")
	future := clk.t.Add(time.Hour).Unix()
	for _, v := range []string{
		"", "garbage", "1.2", strings.Repeat("9", 30) + ".x",
		"9999999999." + g.sign(future), // a MAC for another expiry
		"9999999999.",
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: "__Host-zipgo_session", Value: v})
		if got := serve(g, r).Code; got != http.StatusUnauthorized {
			t.Errorf("cookie %q: status %d, want 401", v, got)
		}
	}
}

// An unusable hash locks the site shut — never open.
func TestBadHashLocksEveryoneOut(t *testing.T) {
	g := &Gate{Host: "bad-hash.example.com", Hash: password} // plaintext, not a hash
	g.setup(testServerKey)
	if w := login(g, password, "/", "198.51.100.4"); w.Code != http.StatusUnauthorized {
		t.Fatalf("login with a plaintext 'hash': %d", w.Code)
	}
	if w := serve(g, httptest.NewRequest(http.MethodGet, "/", nil)); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", w.Code)
	}
}

func TestThrottle(t *testing.T) {
	defer func(ip, site int) { maxFailsPerIP, maxFailsPerSite = ip, site }(maxFailsPerIP, maxFailsPerSite)
	maxFailsPerIP, maxFailsPerSite = 3, 5
	g, clk := newGate(t, "")

	for i := 0; i < 3; i++ {
		login(g, "nope", "/", "198.51.100.10")
	}
	// The guesser is paused, even with the right password...
	if w := login(g, password, "/", "198.51.100.10"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("guesser after 3 failures: %d, want 429", w.Code)
	}
	// ...everyone else is not.
	if w := login(g, password, "/", "198.51.100.11"); w.Code != http.StatusSeeOther {
		t.Fatalf("another visitor: %d, want 303", w.Code)
	}
	// A guesser spread over addresses hits the site ceiling.
	login(g, "nope", "/", "198.51.100.12")
	login(g, "nope", "/", "198.51.100.13")
	if w := login(g, password, "/", "198.51.100.14"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("site ceiling: %d, want 429", w.Code)
	}
	// It all slides away after the window.
	clk.t = clk.t.Add(failWindow + time.Second)
	if w := login(g, password, "/", "198.51.100.10"); w.Code != http.StatusSeeOther {
		t.Fatalf("after the window: %d, want 303", w.Code)
	}
}

// The throttle outlives a config reload, which re-provisions every handler.
func TestThrottleSurvivesReload(t *testing.T) {
	defer func(ip int) { maxFailsPerIP = ip }(maxFailsPerIP)
	maxFailsPerIP = 2
	g, clk := newGate(t, "")
	login(g, "nope", "/", "198.51.100.20")
	login(g, "nope", "/", "198.51.100.20")
	again := &Gate{Host: g.Host, Hash: g.Hash, now: clk.now}
	again.setup(testServerKey)
	if w := login(again, password, "/", "198.51.100.20"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("after reload: %d, want 429", w.Code)
	}
}

func TestSafeNext(t *testing.T) {
	tests := []struct {
		prefix, in, want string
	}{
		{"", "/levels/3?hard=1", "/levels/3?hard=1"},
		{"", "/", "/"},
		{"", "", "/"},
		{"", "https://evil.example/", "/"},
		{"", "//evil.example/", "/"},
		{"", "/\\evil.example/", "/"},
		{"", "evil", "/"},
		{"", "/a\r\nSet-Cookie: x", "/"},
		{"", LoginPath, "/"},
		{"", LogoutPath + "?x", "/"},
		{"", "/" + strings.Repeat("a", 3000), "/"},
		{"/example.com/docs", "/example.com/docs/page", "/example.com/docs/page"},
		{"/example.com/docs", "/example.com/docs", "/example.com/docs"},
		{"/example.com/docs", "/example.com/other", "/example.com/docs/"},
		{"/example.com/docs", "/example.com/docsx", "/example.com/docs/"},
		{"/example.com/docs", "/example.com/docs" + LoginPath, "/example.com/docs/"},
	}
	for _, tc := range tests {
		g := &Gate{Prefix: tc.prefix}
		if got := g.safeNext(tc.in); got != tc.want {
			t.Errorf("safeNext(%q, prefix %q) = %q, want %q", tc.in, tc.prefix, got, tc.want)
		}
	}
}

// In localhost mode the gate lives under the site's path prefix, over HTTP.
func TestLocalhostPrefix(t *testing.T) {
	const prefix = "/example.com/docs"
	g, _ := newGate(t, prefix)
	w := serve(g, httptest.NewRequest(http.MethodGet, prefix+"/page", nil))
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `action="`+prefix+LoginPath+`"`) {
		t.Fatalf("page: %d %s", w.Code, w.Body.String())
	}
	w = login(g, password, prefix+"/page", "127.0.0.1")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != prefix+"/page" {
		t.Fatalf("login: %d → %q", w.Code, w.Header().Get("Location"))
	}
	c := sessionCookie(t, w)
	if c.Name != "zipgo_session" || c.Secure || c.Path != prefix {
		t.Fatalf("localhost cookie: %+v", c)
	}
	r := httptest.NewRequest(http.MethodGet, prefix+"/page", nil)
	r.AddCookie(c)
	if got := serve(g, r).Code; got != http.StatusOK {
		t.Fatalf("with session: %d", got)
	}
}

func TestTitleDefaultsToHost(t *testing.T) {
	g := &Gate{Host: "secret.example.com", Hash: "x"}
	g.setup(testServerKey)
	body := serve(g, httptest.NewRequest(http.MethodGet, "/", nil)).Body.String()
	if !strings.Contains(body, "<title>secret.example.com</title>") {
		t.Fatalf("title: %s", body)
	}
	// And a title is escaped, never markup.
	g = &Gate{Host: "x.example.com", Hash: "x", Title: `<script>alert(1)</script>`}
	g.setup(testServerKey)
	if body := serve(g, httptest.NewRequest(http.MethodGet, "/", nil)).Body.String(); strings.Contains(body, "<script>") {
		t.Fatalf("title not escaped: %s", body)
	}
}

func TestLoadKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zipgo", "session.key")
	k1, err := loadKey(path)
	if err != nil || len(k1) != 32 {
		t.Fatalf("create: %v (%d bytes)", err, len(k1))
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode: %v %v", fi.Mode(), err)
	}
	k2, err := loadKey(path)
	if err != nil || string(k1) != string(k2) {
		t.Fatal("the key changed between two loads")
	}
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadKey(path); err == nil {
		t.Fatal("a truncated key file was accepted")
	}
}
