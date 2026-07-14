// Package doctor inspects a domains folder and reports the problems that stop a
// site from serving — before (or instead of) reading Caddy's logs.
//
// It deliberately re-walks the tree instead of calling sites.Discover: Discover
// is a hard-fail parser (one malformed .zipgoconfig.json aborts the whole
// reload), whereas doctor's job is to keep going and report *every* problem in
// one pass, which is the only way to answer "why isn't my site serving?".
package doctor

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"zipgo/internal/sites"

	"golang.org/x/crypto/bcrypt"
)

// Level ranks a finding. Error means the site is broken (it will not serve, or
// it takes the whole config down with it); Warn means it serves, but almost
// certainly not the way the author intended.
type Level int

const (
	Warn Level = iota
	Error
)

func (l Level) String() string {
	if l == Error {
		return "error"
	}
	return "warning"
}

// Icon is the glyph used in the CLI output.
func (l Level) Icon() string {
	if l == Error {
		return "❌"
	}
	return "⚠️ "
}

// Finding is one problem found in the domains folder.
type Finding struct {
	Level Level
	// Host is the site the finding concerns ("api.docs.example.com"). It is
	// empty when the problem is with a folder that maps to no host at all.
	Host string
	// Path is the folder or file the finding points at.
	Path string
	// Msg states what is wrong; Hint (optional) says what to do about it.
	Msg  string
	Hint string
}

// Report is the result of a Check run.
type Report struct {
	// Findings, ordered by the folder they were found in.
	Findings []Finding
	// Sites is the number of enabled sites that were checked, Disabled the
	// number skipped because of "enable": false.
	Sites    int
	Disabled int
	Domains  int
}

// Errors reports whether any finding is fatal.
func (r Report) Errors() int { return r.count(Error) }

// Warnings reports how many non-fatal findings were raised.
func (r Report) Warnings() int { return r.count(Warn) }

func (r Report) count(l Level) int {
	n := 0
	for _, f := range r.Findings {
		if f.Level == l {
			n++
		}
	}
	return n
}

// OK reports whether the tree is clean at the given strictness: always false if
// there is an error; false on warnings too when strict.
func (r Report) OK(strict bool) bool {
	if r.Errors() > 0 {
		return false
	}
	return !strict || r.Warnings() == 0
}

// knownConfigKeys are the keys .zipgoconfig.json understands. Anything else is
// silently ignored by the real parser, which makes a typo ("enabled" instead of
// "enable") look like zipgo is broken — so doctor calls it out.
var knownConfigKeys = map[string]bool{
	"enable":         true,
	"rewrite":        true,
	"redirect":       true,
	"redirectStatus": true,
	"allowHttp":      true,
	"basicAuth":      true,
}

// knownKeyList is the human list of keys printed as a hint next to an unknown
// key, in a stable order.
const knownKeyList = "enable, rewrite, redirect, redirectStatus, allowHttp, basicAuth"

// redirectStatuses are the status codes "redirectStatus" accepts: 301/308
// permanent, 302/307 temporary.
var redirectStatuses = map[int]bool{301: true, 302: true, 307: true, 308: true}

// Check walks domainsDir and returns everything wrong with it. The returned
// error is only non-nil when the folder itself cannot be read — per-site
// problems come back as findings.
func Check(domainsDir string) (Report, error) {
	var rep Report

	entries, err := os.ReadDir(domainsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return rep, fmt.Errorf("domains folder %q does not exist", domainsDir)
		}
		return rep, fmt.Errorf("reading %s: %w", domainsDir, err)
	}

	// hosts maps a fully-qualified host to the folders that claim it, so two
	// folders resolving to the same host (e.g. "a.b." and "b./a.") are caught.
	hosts := map[string][]string{}

	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !e.IsDir() {
			continue
		}
		if !strings.Contains(name, ".") {
			rep.add(Finding{
				Level: Warn,
				Path:  filepath.Join(domainsDir, name),
				Msg:   fmt.Sprintf("folder %q is not a domain (no dot) and is ignored", name),
				Hint:  "a domain folder's name is the domain itself, e.g. \"example.com\"",
			})
			continue
		}
		if bad := invalidDomain(name); bad != "" {
			rep.add(Finding{
				Level: Error,
				Path:  filepath.Join(domainsDir, name),
				Msg:   fmt.Sprintf("folder %q is not a valid domain: %s", name, bad),
				Hint:  "rename the folder to a hostname made of letters, digits and hyphens",
			})
			continue
		}
		rep.Domains++
		rep.walk(filepath.Join(domainsDir, name), name, nil, hosts)
	}

	// Duplicate hosts: two different folders that would both answer for one host.
	for host, paths := range hosts {
		if len(paths) < 2 {
			continue
		}
		sort.Strings(paths)
		rep.add(Finding{
			Level: Error,
			Host:  host,
			Path:  paths[0],
			Msg: fmt.Sprintf("host is claimed by %d folders: %s",
				len(paths), strings.Join(paths, ", ")),
			Hint: "only one folder may map to a host — remove or rename one",
		})
	}

	sort.SliceStable(rep.Findings, func(i, j int) bool {
		return rep.Findings[i].Path < rep.Findings[j].Path
	})
	return rep, nil
}

func (r *Report) add(f Finding) { r.Findings = append(r.Findings, f) }

// walk checks one site folder and recurses into its trailing-dot subdomains,
// mirroring sites.Discover's traversal but without ever bailing out.
func (r *Report) walk(dir, domain string, labels []string, hosts map[string][]string) {
	site := sites.Site{Labels: labels, Path: dir}
	host := site.Host(domain)
	hosts[host] = append(hosts[host], dir)

	cfg, enabled := r.checkConfig(dir, host)

	if !enabled {
		r.Disabled++
	} else {
		r.Sites++
		r.checkContent(dir, host, cfg)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		r.add(Finding{Level: Error, Host: host, Path: dir,
			Msg: fmt.Sprintf("cannot read folder: %v", err)})
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if !strings.HasSuffix(name, ".") {
			// A plain folder is ordinary content — unless its name looks like a
			// hostname, in which case the author almost certainly meant it to be
			// a subdomain and forgot the trailing dot.
			if looksLikeSubdomain(name, domain) {
				r.add(Finding{
					Level: Warn,
					Host:  host,
					Path:  filepath.Join(dir, name),
					Msg: fmt.Sprintf("folder %q looks like a subdomain but has no trailing dot, "+
						"so it is served as the path /%s", name, name),
					Hint: fmt.Sprintf("rename it to %q to serve it at %s",
						subdomainFolder(name, domain), suggestedHost(name, domain, host)),
				})
			}
			continue
		}
		label := strings.TrimSuffix(name, ".")
		if label == "" {
			continue
		}
		if bad := invalidLabels(label); bad != "" {
			r.add(Finding{
				Level: Error,
				Path:  filepath.Join(dir, name),
				Msg:   fmt.Sprintf("subdomain folder %q is not a valid hostname: %s", name, bad),
				Hint:  "subdomain labels may only contain letters, digits and hyphens",
			})
			continue
		}
		r.walk(filepath.Join(dir, name), domain, append([]string{label}, labels...), hosts)
	}
}

// checkConfig reads .zipgoconfig.json defensively and reports what is wrong with
// it. It returns the parsed config and whether the site is enabled (a site whose
// config cannot be parsed is treated as enabled so its other problems still get
// reported).
func (r *Report) checkConfig(dir, host string) (sites.Config, bool) {
	path := filepath.Join(dir, sites.ConfigFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			r.add(Finding{Level: Error, Host: host, Path: path,
				Msg: fmt.Sprintf("cannot read %s: %v", sites.ConfigFileName, err)})
		}
		return sites.Config{}, true
	}

	var cfg sites.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		r.add(Finding{
			Level: Error, Host: host, Path: path,
			Msg:  fmt.Sprintf("%s is not valid JSON: %v", sites.ConfigFileName, err),
			Hint: "zipgo refuses to reload while this file is malformed — every site stays on the last good config",
		})
		return sites.Config{}, true
	}

	// Unknown keys are dropped on the floor by the real parser, so a typo is
	// invisible: "enabled": false leaves the site very much enabled.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err == nil {
		var unknown []string
		for k := range raw {
			if !knownConfigKeys[k] {
				unknown = append(unknown, k)
			}
		}
		sort.Strings(unknown)
		for _, k := range unknown {
			f := Finding{
				Level: Warn, Host: host, Path: path,
				Msg: fmt.Sprintf("unknown key %q in %s is ignored", k, sites.ConfigFileName),
			}
			if guess := nearestKey(k); guess != "" {
				f.Hint = fmt.Sprintf("did you mean %q?", guess)
			} else {
				f.Hint = "known keys: " + knownKeyList
			}
			r.add(f)
		}
	}

	if cfg.Rewrite != "" {
		if bad := invalidUpstream(cfg.Rewrite); bad != "" {
			r.add(Finding{
				Level: Error, Host: host, Path: path,
				Msg:  fmt.Sprintf("rewrite upstream %q is not usable: %s", cfg.Rewrite, bad),
				Hint: "use \"host:port\" (e.g. localhost:8080) or a URL (e.g. https://api.example.com)",
			})
		}
	}

	r.checkRedirect(dir, path, host, cfg)
	r.checkBasicAuth(cfg, path, host)

	return cfg, cfg.Enabled()
}

// checkRedirect validates the "redirect"/"redirectStatus" pair: the target must
// be an absolute http(s) URL, the status must be a real redirect code, and the
// key must not be combined with settings it silently overrides.
func (r *Report) checkRedirect(dir, path, host string, cfg sites.Config) {
	if cfg.Redirect == "" {
		if cfg.RedirectStatus != 0 {
			r.add(Finding{
				Level: Warn, Host: host, Path: path,
				Msg:  fmt.Sprintf("\"redirectStatus\": %d has no effect without a \"redirect\" target", cfg.RedirectStatus),
				Hint: "add \"redirect\": \"https://elsewhere.example\", or drop the key",
			})
		}
		return
	}

	if bad := invalidRedirect(cfg.Redirect); bad != "" {
		r.add(Finding{
			Level: Error, Host: host, Path: path,
			Msg:  fmt.Sprintf("redirect target %q is not usable: %s", cfg.Redirect, bad),
			Hint: "use an absolute URL, e.g. \"https://elsewhere.example\" (the path and query are kept) or \"https://elsewhere.example/moved\"",
		})
	}

	if cfg.RedirectStatus != 0 && !redirectStatuses[cfg.RedirectStatus] {
		r.add(Finding{
			Level: Error, Host: host, Path: path,
			Msg:  fmt.Sprintf("redirectStatus %d is not a redirect status code", cfg.RedirectStatus),
			Hint: fmt.Sprintf("use 301 or 308 (permanent), 302 or 307 (temporary); the default is %d", sites.DefaultRedirectStatus),
		})
	}

	if cfg.Rewrite != "" {
		r.add(Finding{
			Level: Error, Host: host, Path: path,
			Msg:  "\"redirect\" and \"rewrite\" are both set — the site redirects and the proxy upstream is never used",
			Hint: "keep one: \"redirect\" to send visitors elsewhere, \"rewrite\" to proxy the site",
		})
	}

	if hasIndex(dir) {
		r.add(Finding{
			Level: Warn, Host: host, Path: path,
			Msg:  "the folder has an index.html but the site redirects — its content is never served",
			Hint: "delete the leftover files, or drop \"redirect\" to serve them again",
		})
	}
}

// checkBasicAuth validates the basicAuth block. The dangerous mistake is a
// plaintext password: Caddy compares it as a bcrypt hash, so the site locks
// *everyone* out (including the author) while the secret sits in a file that
// gets rsynced to the server — worth an Error, not a Warn.
func (r *Report) checkBasicAuth(cfg sites.Config, path, host string) {
	if cfg.BasicAuth == nil {
		return
	}
	if len(cfg.BasicAuth) == 0 {
		r.add(Finding{
			Level: Warn, Host: host, Path: path,
			Msg:  "\"basicAuth\" is empty — the site is served without a password",
			Hint: "add \"user\": \"<bcrypt hash>\", or drop the key",
		})
		return
	}

	users := make([]string, 0, len(cfg.BasicAuth))
	for u := range cfg.BasicAuth {
		users = append(users, u)
	}
	sort.Strings(users)

	for _, u := range users {
		if strings.TrimSpace(u) == "" {
			r.add(Finding{
				Level: Error, Host: host, Path: path,
				Msg:  "\"basicAuth\" has an empty username",
				Hint: "keys are usernames: {\"alice\": \"<bcrypt hash>\"}",
			})
			continue
		}
		if bad := invalidBcrypt(cfg.BasicAuth[u]); bad != "" {
			r.add(Finding{
				Level: Error, Host: host, Path: path,
				Msg:  fmt.Sprintf("basicAuth password for %q is not usable: %s", u, bad),
				Hint: "hash it: `caddy hash-password` or `htpasswd -nbB " + u + " <password>` (the $2a$… string)",
			})
		}
	}
}

// invalidBcrypt reports why s is not a bcrypt hash Caddy can compare against,
// or "" when it is one. Passwords are never compared in plaintext, so anything
// that isn't a hash is a site nobody can log into.
func invalidBcrypt(s string) string {
	if strings.TrimSpace(s) == "" {
		return "it is empty"
	}
	if !strings.HasPrefix(s, "$2") {
		return "it looks like a plaintext password, not a bcrypt hash"
	}
	if _, err := bcrypt.Cost([]byte(s)); err != nil {
		return fmt.Sprintf("not a valid bcrypt hash (%v)", err)
	}
	return ""
}

// checkContent verifies an enabled site has something to serve.
func (r *Report) checkContent(dir, host string, cfg sites.Config) {
	if cfg.Rewrite != "" || cfg.Redirect != "" {
		// A proxied or redirected site serves nothing from disk, so index.html
		// is irrelevant.
		return
	}
	if hasIndex(dir) {
		return
	}
	// A site folder that only exists to hold subdomains is a legitimate,
	// intentional shape (e.g. gabvdl.xyz/game./ holding game subdomains), so it
	// is a warning rather than an error — but it still means the host itself has
	// nothing to serve.
	level, hint := Error, "add an index.html, or set \"rewrite\" (proxy) or \"redirect\" in "+sites.ConfigFileName
	if hasSubdomainChild(dir) {
		level = Warn
		hint = "this folder only holds subdomains; set \"enable\": false in " +
			sites.ConfigFileName + " if the host itself should not be served"
	}
	r.add(Finding{
		Level: level, Host: host, Path: dir,
		Msg:  "no index.html — the host has nothing to serve",
		Hint: hint,
	})
}

// hasIndex reports whether dir holds an index.html (case-insensitively, matching
// the SPA detector).
func hasIndex(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(e.Name(), "index.html") {
			return true
		}
	}
	return false
}

// hasSubdomainChild reports whether dir contains at least one trailing-dot child.
func hasSubdomainChild(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") &&
			strings.HasSuffix(e.Name(), ".") && e.Name() != "." {
			return true
		}
	}
	return false
}

// looksLikeSubdomain reports whether a dot-less-suffix folder name reads like a
// hostname the author meant to expose as a subdomain: it contains a dot and is
// otherwise a valid hostname ("docs.example.com", "api.v2"). Ordinary content
// folders ("assets", "images") contain no dot and never match; neither do files
// with an extension-looking name, since we only ever call this for directories.
func looksLikeSubdomain(name, domain string) bool {
	if !strings.Contains(name, ".") {
		return false
	}
	if invalidLabels(name) != "" {
		return false
	}
	// A folder named after a *file* extension convention (e.g. "v1.2") is
	// ambiguous; requiring a non-numeric last label keeps "v1.2"/"1.0" out.
	parts := strings.Split(name, ".")
	last := parts[len(parts)-1]
	if last == "" || isAllDigits(last) {
		return false
	}
	_ = domain
	return true
}

// subdomainFolder is the folder name the author probably wanted: the fully
// qualified name with the root domain (and any trailing dot) stripped, plus the
// trailing dot. "docs.example.com" under example.com → "docs."
func subdomainFolder(name, domain string) string {
	trimmed := strings.TrimSuffix(name, "."+domain)
	return trimmed + "."
}

// suggestedHost is the host the renamed folder would serve, given the host of
// the folder it sits in.
func suggestedHost(name, domain, parentHost string) string {
	label := strings.TrimSuffix(subdomainFolder(name, domain), ".")
	return label + "." + parentHost
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

// invalidDomain returns a reason string when name is not a usable domain folder,
// or "" when it is fine. A domain must have at least two labels.
func invalidDomain(name string) string {
	if !strings.Contains(name, ".") {
		return "a domain needs at least one dot"
	}
	return invalidLabels(name)
}

// invalidLabels validates a dotted hostname fragment label by label, returning a
// human reason or "".
func invalidLabels(name string) string {
	if name == "" {
		return "empty name"
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" {
			return fmt.Sprintf("%q has an empty label (consecutive or leading dots)", name)
		}
		if len(label) > 63 {
			return fmt.Sprintf("label %q is longer than 63 characters", label)
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return fmt.Sprintf("label %q starts or ends with a hyphen", label)
		}
		for _, r := range label {
			ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '-'
			if !ok {
				return fmt.Sprintf("label %q contains %q, which is not a letter, digit or hyphen", label, r)
			}
		}
	}
	return ""
}

// invalidUpstream sanity-checks a "rewrite" value the way the builder will read
// it: a bare host:port or a scheme URL. It cannot tell whether the upstream is
// actually up — only whether zipgo can turn it into a dial address.
func invalidUpstream(u string) string {
	if strings.TrimSpace(u) != u || u == "" {
		return "leading or trailing whitespace"
	}
	if strings.ContainsAny(u, " \t") {
		return "contains a space"
	}
	if strings.Contains(u, "://") {
		scheme := strings.SplitN(u, "://", 2)[0]
		if scheme != "http" && scheme != "https" {
			return fmt.Sprintf("unsupported scheme %q (use http or https)", scheme)
		}
		if strings.SplitN(u, "://", 2)[1] == "" {
			return "no host after the scheme"
		}
		return ""
	}
	if strings.HasPrefix(u, "/") {
		return "looks like a path, not an upstream address"
	}
	return ""
}

// invalidRedirect sanity-checks a "redirect" value the way the builder will read
// it: an absolute http(s) URL with a host. A path ("/elsewhere") is rejected on
// purpose — a redirect replaces the site's file server, so a same-site path
// would redirect to itself forever.
func invalidRedirect(u string) string {
	if u == "" {
		return "empty"
	}
	if strings.TrimSpace(u) != u {
		return "leading or trailing whitespace"
	}
	if strings.ContainsAny(u, " \t") {
		return "contains a space"
	}
	if strings.HasPrefix(u, "/") {
		return "a path redirects the site to itself (an infinite loop) — use an absolute URL"
	}
	if !strings.Contains(u, "://") {
		return "no scheme — use \"https://…\""
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return fmt.Sprintf("not a URL: %v", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Sprintf("unsupported scheme %q (use http or https)", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "no host after the scheme"
	}
	return ""
}

// nearestKey guesses which known key a typo meant, by case-insensitive prefix
// (covers "enabled"/"Enable"/"rewrites"). Returns "" when nothing is close.
func nearestKey(k string) string {
	lower := strings.ToLower(k)
	keys := make([]string, 0, len(knownConfigKeys))
	for key := range knownConfigKeys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		lk := strings.ToLower(key)
		if strings.HasPrefix(lower, lk) || strings.HasPrefix(lk, lower) {
			return key
		}
	}
	return ""
}
