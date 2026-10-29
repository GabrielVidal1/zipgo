// Package admin is the back-office a domain can opt into: a folder whose
// .zipgoconfig.json says "admin": true (and carries a "password") serves a page
// that lists the site it is nested in and every subdomain under it, with an
// on/off switch and a password lock each. "admin": "all" lists every domain in
// the domains folder instead, each one a root of the tree: one back-office for
// the whole server.
//
// It is a face for the files, not a second source of truth: a switch writes the
// same "enable" key `zipgo off` writes, a lock the same "password" block
// `zipgo password set` writes, and the server's watcher hot-reloads them as it
// would any other edit. There is no state of its own, so the page, the CLI and
// `zipgo ls` can never disagree.
//
// The handler only ever runs behind the site's login page (internal/gate): the
// builder puts the gate in front of it, and serves a refusal instead when the
// folder has no password.
package admin

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"

	"zipgo/internal/config"
)

// HandlerName is the handler name the builder emits (module
// http.handlers.zipgo_admin).
const HandlerName = "zipgo_admin"

//go:embed ui
var ui embed.FS

// maxBodyBytes caps an API request: an id, a flag and maybe a password.
const maxBodyBytes = 4096

// ScopeAll is the Scope of a back-office of every domain ("admin": "all").
const ScopeAll = "all"

func init() {
	caddy.RegisterModule(Admin{})
}

// Admin is the http.handlers.zipgo_admin module.
type Admin struct {
	// Scope is ScopeAll for a back-office of every domain in DomainsDir, ""
	// for one of the site at Root.
	Scope      string `json:"scope,omitempty"`
	DomainsDir string `json:"domains_dir,omitempty"`
	// Root is the folder of the site the back-office manages (the folder its
	// own folder is nested in), RootHost that site's host.
	Root     string `json:"root,omitempty"`
	RootHost string `json:"root_host,omitempty"`
	// Self is the back-office's own folder: listed, never switched.
	Self string `json:"self"`
	// TmpDir is where new configs are written before being renamed into place:
	// the domains folder (same filesystem, outside every site).
	TmpDir string `json:"tmp_dir"`
	// Prefix is the page's path prefix in localhost mode ("" for a host), and
	// RootPath the managed site's: links then point at paths, not hosts.
	Prefix   string `json:"prefix,omitempty"`
	RootPath string `json:"root_path,omitempty"`
}

// CaddyModule returns the Caddy module information.
func (Admin) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers." + HandlerName,
		New: func() caddy.Module { return new(Admin) },
	}
}

// pageHeaders go on every response. The page loads its own script and styles
// and talks to its own API, nothing else; no-store keeps the site list out of
// every cache, and no-referrer keeps the back-office's host out of the logs of
// every site it links to.
var pageHeaders = map[string]string{
	"Cache-Control":           "no-store",
	"X-Robots-Tag":            "noindex, nofollow",
	"Referrer-Policy":         "no-referrer",
	"X-Content-Type-Options":  "nosniff",
	"X-Frame-Options":         "DENY",
	"Content-Security-Policy": "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; manifest-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'",
}

// ServeHTTP answers the page, its assets and its API. It never calls next: the
// back-office is the whole site.
func (a *Admin) ServeHTTP(w http.ResponseWriter, r *http.Request, _ caddyhttp.Handler) error {
	for k, v := range pageHeaders {
		w.Header().Set(k, v)
	}
	p := r.URL.Path
	if a.Prefix != "" {
		if p == a.Prefix {
			// Relative URLs in the page need the trailing slash.
			http.Redirect(w, r, a.Prefix+"/", http.StatusSeeOther)
			return nil
		}
		p = strings.TrimPrefix(p, a.Prefix)
	}
	switch p {
	case "/api/sites":
		if !allow(w, r, http.MethodGet) {
			return nil
		}
		a.sites(w)
	case "/api/sizes":
		if !allow(w, r, http.MethodGet) {
			return nil
		}
		sizes, err := a.sizes()
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return nil
		}
		reply(w, map[string]any{"sizes": sizes})
	case "/api/switch":
		if !allow(w, r, http.MethodPost) || !sameOrigin(w, r) {
			return nil
		}
		var req struct {
			ID string `json:"id"`
			On bool   `json:"on"`
		}
		if !decode(w, r, &req) {
			return nil
		}
		r, id, err := a.locate(req.ID)
		var changed bool
		if err == nil {
			changed, err = Switch(r.Dir, a.TmpDir, a.Self, id, req.On)
		}
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return nil
		}
		if changed {
			log.Printf("🛠  %s: %s switched %s", a.name(), r.hostOf(id), map[bool]string{true: "on", false: "off"}[req.On])
		}
		a.sites(w)
	case "/api/flag":
		if !allow(w, r, http.MethodPost) || !sameOrigin(w, r) {
			return nil
		}
		var req struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			On   bool   `json:"on"`
		}
		if !decode(w, r, &req) {
			return nil
		}
		r, id, err := a.locate(req.ID)
		var changed bool
		if err == nil {
			changed, err = SetFlag(r.Dir, a.TmpDir, a.Self, id, req.Name, req.On)
		}
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return nil
		}
		if changed {
			log.Printf("🛠  %s: %s flag %s %s", a.name(), r.hostOf(id), req.Name, map[bool]string{true: "on", false: "off"}[req.On])
		}
		a.sites(w)
	case "/api/password":
		if !allow(w, r, http.MethodPost) || !sameOrigin(w, r) {
			return nil
		}
		var req struct {
			ID       string `json:"id"`
			Action   string `json:"action"` // set | generate | remove
			Password string `json:"password"`
		}
		if !decode(w, r, &req) {
			return nil
		}
		r, id, err := a.locate(req.ID)
		var generated string
		if err == nil {
			generated, err = SetLock(r.Dir, a.TmpDir, a.Self, id, req.Action, req.Password)
		}
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return nil
		}
		log.Printf("🛠  %s: %s password %s", a.name(), r.hostOf(id), req.Action)
		out, err := a.payload(generated)
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return nil
		}
		reply(w, out)
	case "/api/versions":
		if !allow(w, r, http.MethodGet) {
			return nil
		}
		rt, id, err := a.locate(r.URL.Query().Get("id"))
		var versions []Version
		if err == nil {
			versions, err = Versions(rt.Dir, id)
		}
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return nil
		}
		reply(w, map[string]any{"versions": versions})
	case "/api/rollback":
		if !allow(w, r, http.MethodPost) || !sameOrigin(w, r) {
			return nil
		}
		var req struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		}
		if !decode(w, r, &req) {
			return nil
		}
		rt, id, err := a.locate(req.ID)
		var backup string
		if err == nil {
			backup, err = Rollback(rt.Dir, a.TmpDir, a.Self, id, req.Version)
		}
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return nil
		}
		log.Printf("🛠  %s: %s rolled back to %s", a.name(), rt.hostOf(id), req.Version)
		out, err := a.payload("")
		if err == nil {
			out["versions"], err = Versions(rt.Dir, id)
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return nil
		}
		out["backup"] = backup
		reply(w, out)
	default:
		a.asset(w, r, p)
	}
	return nil
}

// sites answers the current list.
func (a *Admin) sites(w http.ResponseWriter) {
	out, err := a.payload("")
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	reply(w, out)
}

// root is one tree the page manages: a site folder and its host. ID is the
// prefix of its rows' IDs — "" for a site's back-office (the IDs stay the
// folder paths under it), the domain for one of every domain
// ("gabvdl.xyz/dev./card-editor."). Path is its path in localhost mode.
type root struct {
	ID, Dir, Host, Path string
}

// roots are the trees the page manages, in order. For ScopeAll they are read
// from the domains folder on every request, the way the server reads its
// domains, so a domain added since is listed too.
func (a *Admin) roots() ([]root, error) {
	if a.Scope != ScopeAll {
		return []root{{Dir: a.Root, Host: a.RootHost, Path: a.RootPath}}, nil
	}
	domains, err := config.ReadDomains(a.DomainsDir)
	if err != nil {
		return nil, err
	}
	out := make([]root, 0, len(domains))
	for _, d := range domains {
		r := root{ID: d, Dir: filepath.Join(a.DomainsDir, d), Host: d}
		if a.Prefix != "" {
			r.Path = "/" + d
		}
		out = append(out, r)
	}
	return out, nil
}

// locate splits a row's ID into its root and the folder path under it. For
// ScopeAll the first part must name one of the domains read from the folder:
// nothing else, so no ID reaches outside them.
func (a *Admin) locate(id string) (root, string, error) {
	rs, err := a.roots()
	if err != nil {
		return root{}, "", err
	}
	if a.Scope != ScopeAll {
		return rs[0], id, nil
	}
	domain, rest, _ := strings.Cut(id, "/")
	for _, r := range rs {
		if r.ID == domain {
			return r, rest, nil
		}
	}
	return root{}, "", fmt.Errorf("no site %q", id)
}

// name is how the logs call this back-office.
func (a *Admin) name() string {
	if a.Scope == ScopeAll {
		return "all domains"
	}
	return a.RootHost
}

// row is a Site as the page gets it: with the URL it opens.
type row struct {
	Site
	URL string `json:"url,omitempty"`
}

// payload is the page's whole state: every root's sites, parent-first, and
// which rows are roots.
func (a *Admin) payload(generated string) (map[string]any, error) {
	rs, err := a.roots()
	if err != nil {
		return nil, err
	}
	rows := []row{}
	ids := []string{}
	for _, r := range rs {
		list, err := listFrom(r.Dir, r.ID, r.Host, a.Self)
		if err != nil {
			if a.Scope == ScopeAll && errors.Is(err, os.ErrNotExist) {
				continue // a domain folder removed since it was read
			}
			return nil, err
		}
		ids = append(ids, r.ID)
		for _, s := range list {
			x := row{Site: s}
			if s.Type != "folder" && !s.Self {
				x.URL = r.link(strings.TrimPrefix(strings.TrimPrefix(s.ID, r.ID), "/"), s.Host)
			}
			rows = append(rows, x)
		}
	}
	out := map[string]any{"host": a.RootHost, "roots": ids, "sites": rows}
	if a.Scope == ScopeAll {
		out["scope"] = ScopeAll
	}
	if generated != "" {
		out["password"] = generated
	}
	return out, nil
}

// sizes is every root's Sizes, keyed by row ID.
func (a *Admin) sizes() (map[string]Size, error) {
	rs, err := a.roots()
	if err != nil {
		return nil, err
	}
	out := map[string]Size{}
	for _, r := range rs {
		sizes, err := Sizes(r.Dir)
		if err != nil {
			if a.Scope == ScopeAll && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for id, z := range sizes {
			out[joinID(r.ID, id)] = z
		}
	}
	return out, nil
}

// link is where a site is visited: its host, or in localhost mode its path.
// id is its folder path under the root.
func (r root) link(id, host string) string {
	if r.Path == "" {
		return "https://" + host
	}
	p := r.Path
	if id != "" {
		for _, part := range strings.Split(id, "/") {
			p += "/" + strings.TrimSuffix(part, ".")
		}
	}
	return p + "/"
}

// hostOf is the host of the folder at id under the root.
func (r root) hostOf(id string) string {
	h := r.Host
	if id == "" {
		return h
	}
	for _, part := range strings.Split(id, "/") {
		h = strings.TrimSuffix(part, ".") + "." + h
	}
	return h
}

// asset serves the page and its static files.
func (a *Admin) asset(w http.ResponseWriter, r *http.Request, p string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+p), "/")
	if name == "" {
		name = "index.html"
	}
	b, err := ui.ReadFile("ui/" + name)
	if err != nil || strings.Contains(name, "/") {
		http.NotFound(w, r)
		return
	}
	ct := mime.TypeByExtension(filepath.Ext(name))
	if strings.HasSuffix(name, ".webmanifest") {
		ct = "application/manifest+json"
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	if r.Method != http.MethodHead {
		w.Write(b)
	}
}

// allow answers 405 unless the request uses method.
func allow(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	fail(w, http.StatusMethodNotAllowed, fmt.Errorf("use %s", method))
	return false
}

// sameOrigin refuses a state-changing request that a browser says came from
// another site. The session cookie is SameSite=Lax, so a cross-site POST
// already arrives logged out; this is the second lock on that door, and the
// JSON content type (which a plain HTML form can't send) is the third.
func sameOrigin(w http.ResponseWriter, r *http.Request) bool {
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" {
		fail(w, http.StatusForbidden, errors.New("cross-site request"))
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host != r.Host {
			fail(w, http.StatusForbidden, errors.New("cross-site request"))
			return false
		}
	}
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, errors.New("send JSON"))
		return false
	}
	return true
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("bad request: %w", err))
		return false
	}
	return true
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
