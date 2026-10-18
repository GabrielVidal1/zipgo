// Package gate is the Caddy handler behind a .zipgoconfig.json "password": one
// shared password in front of a whole site, with a real login page instead of
// the browser's basic-auth dialog.
//
//	no valid session cookie → 401 + the login page, on every path (the
//	                          index, every asset, sub-domains-meta): nothing
//	                          of the site is served
//	POST <prefix>/__zipgo/login with the right password
//	                        → a signed session cookie, 303 back to the page
//	                          that was asked for
//	<prefix>/__zipgo/logout → the cookie is cleared
//	GET any path?__zipgo_pwd=<pw>
//	                        → checked like the form; right: the session
//	                          cookie and a 303 to the same URL without the
//	                          param (a link that skips the page, e.g. a
//	                          kiosk or a Steam shortcut); wrong: the page
//
// Sessions are stateless. A cookie is "<expiry>.<mac>", the MAC keyed by
// HMAC(server key, host, password hash): the server key (Caddy's data dir) makes
// a cookie unforgeable even by someone who has read the config, the host keeps
// it from working on another site, and the hash means rotating the password —
// which always mints a new bcrypt salt — logs everyone out.
//
// zipgo answers by Host header whatever the TLS SNI, and this handler runs
// inside the host's own route, so a domain-fronted request (SNI of a public
// site, Host of a locked one) meets the same page.
package gate

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"golang.org/x/crypto/bcrypt"
)

// HandlerName is the handler name the builder emits (module
// http.handlers.zipgo_password).
const HandlerName = "zipgo_password"

// LoginPath and LogoutPath are the gate's own endpoints, relative to the site
// root. The double underscore keeps them out of any real site's way.
const (
	LoginPath  = "/__zipgo/login"
	LogoutPath = "/__zipgo/logout"
)

// UnlockParam is the query parameter that carries the password in a link that
// skips the login page (/levels?__zipgo_pwd=…). The gate always consumes it:
// it never reaches the site, and the redirect drops it from the address bar.
// The double underscore keeps it out of any real site's way, as for LoginPath.
const UnlockParam = "__zipgo_pwd"

// Tunables. Variables rather than constants so tests can shrink them.
var (
	// failWindow is the sliding window wrong passwords are counted over.
	failWindow = time.Minute
	// maxFailsPerIP wrong passwords from one address within failWindow pause
	// that address's logins until the window slides past them.
	maxFailsPerIP = 10
	// maxFailsPerSite wrong passwords on one site within failWindow pause
	// every login on it: the ceiling for a guesser spread over many addresses
	// (or for every visitor of a host reached through an HTTP proxy, where
	// they all share the proxy's address).
	maxFailsPerSite = 60
	// renewAfter: a session older than this is re-issued with a fresh expiry
	// on the next request, so someone who keeps coming back never meets the
	// login page again (until the password changes).
	renewAfter = 24 * time.Hour
)

// maxFormBytes caps the login POST body: a password and a path, nothing more.
const maxFormBytes = 4096

func init() {
	caddy.RegisterModule(Gate{})
}

// Gate is the http.handlers.zipgo_password module.
type Gate struct {
	// Host is the site's host. It scopes the session key and the throttle.
	Host string `json:"host"`
	// Hash is the bcrypt hash of the password.
	Hash string `json:"hash"`
	// Title heads the login page; empty means Host.
	Title string `json:"title,omitempty"`
	// SessionDays is the session lifetime (the builder has already applied
	// the default and the clamp).
	SessionDays int `json:"session_days,omitempty"`
	// Prefix is the site's path prefix in localhost mode ("" for a host).
	Prefix string `json:"prefix,omitempty"`
	// Insecure is set in localhost mode, which is plain HTTP: the cookie then
	// can't be Secure, so it can't carry the __Host- prefix either.
	Insecure bool `json:"insecure,omitempty"`

	key      []byte
	lifetime time.Duration
	hashOK   bool
	lim      *limiter
	now      func() time.Time
}

// CaddyModule returns the Caddy module information.
func (Gate) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers." + HandlerName,
		New: func() caddy.Module { return new(Gate) },
	}
}

// Provision derives the site's session key. It never fails: an unusable hash
// is logged and turns the site into one nobody can enter, rather than an error
// that would make Caddy reject the config of every site at once.
func (g *Gate) Provision(caddy.Context) error {
	g.setup(serverKey())
	return nil
}

// setup does everything Provision does, given the server key (tests pass
// their own).
func (g *Gate) setup(server []byte) {
	g.key = siteKey(server, g.Host, g.Hash)
	days := g.SessionDays
	if days <= 0 {
		days = 7
	}
	g.lifetime = time.Duration(days) * 24 * time.Hour
	_, err := bcrypt.Cost([]byte(g.Hash))
	g.hashOK = err == nil
	if !g.hashOK {
		log.Printf("🔒  %s: the password hash is not a bcrypt hash — nobody can log in (zipgo password set %s)", g.Host, g.Host)
	}
	if g.Title == "" {
		g.Title = g.Host
	}
	g.lim = limiterFor(g.Host)
	if g.now == nil {
		g.now = time.Now
	}
}

// siteKey is the MAC key for one site's session cookies.
func siteKey(server []byte, host, hash string) []byte {
	m := hmac.New(sha256.New, server)
	m.Write([]byte("zipgo-password-v1\x00" + host + "\x00" + hash))
	return m.Sum(nil)
}

// ServeHTTP lets a request with a valid session through to the site, answers
// the login and logout endpoints, and shows the login page to everything else.
func (g *Gate) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	switch r.URL.Path {
	case g.Prefix + LoginPath:
		if r.Method == http.MethodPost {
			g.login(w, r)
			return nil
		}
		if _, ok := g.session(r); ok {
			g.redirect(w, r, g.home())
			return nil
		}
		g.page(w, r, http.StatusUnauthorized, g.home(), "")
		return nil
	case g.Prefix + LogoutPath:
		http.SetCookie(w, g.expired())
		g.redirect(w, r, g.home())
		return nil
	}

	if pw, ok := unlockPassword(r); ok {
		g.unlock(w, r, pw)
		return nil
	}

	expiry, ok := g.session(r)
	if !ok {
		g.page(w, r, http.StatusUnauthorized, g.safeNext(r.URL.RequestURI()), "")
		return nil
	}
	if g.lifetime > renewAfter && time.Unix(expiry, 0).Sub(g.now()) < g.lifetime-renewAfter {
		http.SetCookie(w, g.cookie())
	}
	return next.ServeHTTP(w, r)
}

// login checks a posted password.
func (g *Gate) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		g.page(w, r, http.StatusBadRequest, g.home(), "That didn't work. Try again.")
		return
	}
	next := g.safeNext(r.PostForm.Get("next"))
	ip := clientIP(r)
	now := g.now()
	if g.lim.blocked(ip, now) {
		g.page(w, r, http.StatusTooManyRequests, next, "Too many attempts. Try again in a minute.")
		return
	}
	if g.check(r.PostForm.Get("password")) {
		log.Printf("🔓  %s: new session from %s", g.Host, ip)
		http.SetCookie(w, g.cookie())
		g.redirect(w, r, next)
		return
	}
	if g.lim.fail(ip, now) {
		log.Printf("🚫  %s: too many wrong passwords (last from %s) — logins paused for up to %s",
			g.Host, ip, failWindow)
	}
	g.page(w, r, http.StatusUnauthorized, next, "Wrong password.")
}

// unlock answers a link carrying the password. An existing session needs no
// check: the visitor just goes on to the clean URL.
func (g *Gate) unlock(w http.ResponseWriter, r *http.Request, pw string) {
	next := g.safeNext(withoutParam(r.URL, UnlockParam))
	if _, ok := g.session(r); ok {
		g.redirect(w, r, next)
		return
	}
	ip := clientIP(r)
	now := g.now()
	if g.lim.blocked(ip, now) {
		g.page(w, r, http.StatusTooManyRequests, next, "Too many attempts. Try again in a minute.")
		return
	}
	if g.check(pw) {
		log.Printf("🔓  %s: new session from %s (link)", g.Host, ip)
		http.SetCookie(w, g.cookie())
		g.redirect(w, r, next)
		return
	}
	if g.lim.fail(ip, now) {
		log.Printf("🚫  %s: too many wrong passwords (last from %s) — logins paused for up to %s",
			g.Host, ip, failWindow)
	}
	g.page(w, r, http.StatusUnauthorized, next, "Wrong password.")
}

// unlockPassword is the password a GET or HEAD carries in UnlockParam.
func unlockPassword(r *http.Request) (string, bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return "", false
	}
	q := r.URL.Query()
	if !q.Has(UnlockParam) {
		return "", false
	}
	return q.Get(UnlockParam), true
}

// withoutParam is u's request URI without any occurrence of the query
// parameter key; the other parameters keep their order and spelling.
func withoutParam(u *url.URL, key string) string {
	var kept []string
	for _, part := range strings.Split(u.RawQuery, "&") {
		k, _, _ := strings.Cut(part, "=")
		if dk, err := url.QueryUnescape(k); part == "" || (err == nil && dk == key) || k == key {
			continue
		}
		kept = append(kept, part)
	}
	out := u.EscapedPath()
	if out == "" {
		out = "/"
	}
	if len(kept) > 0 {
		out += "?" + strings.Join(kept, "&")
	}
	return out
}

// check compares a password with the hash. bcrypt's comparison takes the same
// time however close the guess was.
func (g *Gate) check(password string) bool {
	if !g.hashOK || password == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(g.Hash), []byte(password)) == nil
}

// page answers with the login page.
func (g *Gate) page(w http.ResponseWriter, r *http.Request, status int, next, msg string) {
	body := render(pageData{Title: g.Title, Action: g.Prefix + LoginPath, Next: next, Error: msg})
	h := w.Header()
	for k, v := range pageHeaders {
		h.Set(k, v)
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		w.Write(body)
	}
}

// redirect sends a 303 (a POST becomes a GET) that no cache keeps.
func (g *Gate) redirect(w http.ResponseWriter, r *http.Request, to string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", to)
	w.WriteHeader(http.StatusSeeOther)
}

// home is the site's root.
func (g *Gate) home() string { return g.Prefix + "/" }

// cookieName is __Host-zipgo_session over HTTPS: the __Host- prefix makes the
// browser refuse the cookie unless it is Secure, host-only and Path=/, so no
// sibling subdomain can plant or shadow it.
func (g *Gate) cookieName() string {
	if g.Insecure {
		return "zipgo_session"
	}
	return "__Host-zipgo_session"
}

func (g *Gate) cookiePath() string {
	if g.Prefix == "" {
		return "/"
	}
	return g.Prefix
}

// cookie mints a session that expires one lifetime from now.
func (g *Gate) cookie() *http.Cookie {
	exp := g.now().Add(g.lifetime).Unix()
	return &http.Cookie{
		Name:     g.cookieName(),
		Value:    strconv.FormatInt(exp, 10) + "." + g.sign(exp),
		Path:     g.cookiePath(),
		MaxAge:   int(g.lifetime / time.Second),
		Secure:   !g.Insecure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

// expired is the cookie that deletes the session.
func (g *Gate) expired() *http.Cookie {
	return &http.Cookie{
		Name:     g.cookieName(),
		Value:    "",
		Path:     g.cookiePath(),
		MaxAge:   -1,
		Secure:   !g.Insecure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func (g *Gate) sign(exp int64) string {
	m := hmac.New(sha256.New, g.key)
	m.Write([]byte("v1." + strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// session returns the expiry of the request's valid session cookie. Every
// cookie of the right name is tried: in localhost mode a nested site's path
// sits under its parent's, so the browser may send both.
func (g *Gate) session(r *http.Request) (int64, bool) {
	if !g.hashOK {
		return 0, false
	}
	name := g.cookieName()
	now := g.now().Unix()
	for _, c := range r.Cookies() {
		if c.Name != name {
			continue
		}
		expS, sig, ok := strings.Cut(c.Value, ".")
		if !ok {
			continue
		}
		exp, err := strconv.ParseInt(expS, 10, 64)
		if err != nil || exp <= now {
			continue
		}
		if hmac.Equal([]byte(sig), []byte(g.sign(exp))) {
			return exp, true
		}
	}
	return 0, false
}

// safeNext keeps the post-login redirect on this site: a path (in localhost
// mode, one under the site's prefix), never a scheme, a //host or a /\host
// that a browser would read as another origin, and never the gate's own
// endpoints. Anything else goes home.
func (g *Gate) safeNext(raw string) string {
	home := g.home()
	if raw == "" || len(raw) > 2048 || !strings.HasPrefix(raw, "/") ||
		strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, "/\\") {
		return home
	}
	for _, c := range raw {
		if c < 0x20 || c == 0x7f {
			return home
		}
	}
	if g.Prefix != "" && raw != g.Prefix && !strings.HasPrefix(raw, g.Prefix+"/") {
		return home
	}
	p, _, _ := strings.Cut(raw, "?")
	if strings.HasPrefix(p, g.Prefix+"/__zipgo/") {
		return home
	}
	return raw
}

// clientIP is the visitor's address as Caddy resolved it (the PROXY protocol
// header, when zipgo sits behind a TCP proxy that sends one).
func clientIP(r *http.Request) string {
	if ip, ok := caddyhttp.GetVar(r.Context(), caddyhttp.ClientIPVarKey).(string); ok && ip != "" {
		return ip
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// ---- server key --------------------------------------------------------------

var (
	keyOnce sync.Once
	srvKey  []byte
)

// KeyFile is where the server key lives: Caddy's data dir, next to the
// certificates, so it survives restarts wherever those do.
func KeyFile() string {
	return filepath.Join(caddy.AppDataDir(), "zipgo", "session.key")
}

// serverKey loads (or creates) the key every site's session key derives from.
// Should the data dir be unwritable, a random key still works — sessions then
// last until zipgo restarts.
func serverKey() []byte {
	keyOnce.Do(func() {
		k, err := loadKey(KeyFile())
		if err != nil {
			log.Printf("⚠️  password sessions: %v — sessions will not survive a restart", err)
			k = make([]byte, 32)
			if _, err := rand.Read(k); err != nil {
				panic(err)
			}
		}
		srvKey = k
	})
	return srvKey
}

// loadKey reads a key file, creating it (32 random bytes, mode 0600) when it
// does not exist.
func loadKey(path string) ([]byte, error) {
	k, err := os.ReadFile(path)
	if err == nil {
		if len(k) < 32 {
			return nil, fmt.Errorf("%s is too short to be a session key", path)
		}
		return k, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	k = make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, k, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	return k, nil
}

// ---- throttling ----------------------------------------------------------------

// limiter counts one site's wrong passwords. It lives outside the handler, in
// a registry keyed by host, because Caddy re-provisions every handler on each
// config reload — and zipgo reloads on every deploy.
type limiter struct {
	mu   sync.Mutex
	site []time.Time
	byIP map[string][]time.Time
}

var limiters sync.Map // host → *limiter

func limiterFor(host string) *limiter {
	l, _ := limiters.LoadOrStore(host, &limiter{byIP: map[string][]time.Time{}})
	return l.(*limiter)
}

// prune drops failures older than the window. The caller holds mu.
func (l *limiter) prune(now time.Time) {
	cut := now.Add(-failWindow)
	l.site = after(l.site, cut)
	for ip, ts := range l.byIP {
		if ts = after(ts, cut); len(ts) == 0 {
			delete(l.byIP, ip)
		} else {
			l.byIP[ip] = ts
		}
	}
}

func after(ts []time.Time, cut time.Time) []time.Time {
	i := 0
	for i < len(ts) && !ts[i].After(cut) {
		i++
	}
	return ts[i:]
}

// blocked reports whether a login attempt from ip must be refused unchecked.
func (l *limiter) blocked(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	return len(l.byIP[ip]) >= maxFailsPerIP || len(l.site) >= maxFailsPerSite
}

// fail records a wrong password and reports whether it is the one that tripped
// a limit (worth a log line, once).
func (l *limiter) fail(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	l.byIP[ip] = append(l.byIP[ip], now)
	l.site = append(l.site, now)
	return len(l.byIP[ip]) == maxFailsPerIP || len(l.site) == maxFailsPerSite
}

// Interface guards
var (
	_ caddy.Provisioner           = (*Gate)(nil)
	_ caddyhttp.MiddlewareHandler = (*Gate)(nil)
)
