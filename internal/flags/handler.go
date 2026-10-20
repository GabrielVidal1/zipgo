// Package flags serves a site's feature flags: the http.handlers.zipgo_flags
// Caddy module answers /__zipgo/flags.json with the current switches (the
// site's own code reads it at boot to show or hide unfinished features) and
// answers 404 on the paths of every flag that is off, so a feature that lives
// on pages of its own can't be reached by URL either.
//
// Like the back-office it has no state: the builder emits it from the site's
// .zipgoconfig.json "flags" block on every reload, so a switch flipped from
// the back-office or `zipgo flag` is live as soon as the watcher reloads.
package flags

import (
	"encoding/json"
	"net/http"
	"path"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"

	"zipgo/internal/sites"
)

// HandlerName is the handler name the builder emits (module
// http.handlers.zipgo_flags).
const HandlerName = "zipgo_flags"

func init() {
	caddy.RegisterModule(Handler{})
}

// Handler is the http.handlers.zipgo_flags module.
type Handler struct {
	// Host is the site's host.
	Host string `json:"host"`
	// Flags are the site's switches, by name.
	Flags map[string]bool `json:"flags"`
	// Closed are the path prefixes of the flags that are off, normalised
	// (sites.NormalizeFlagPath): each covers itself and everything under it.
	Closed []string `json:"closed,omitempty"`
	// Prefix is the site's path prefix in localhost mode ("" for a host).
	Prefix string `json:"prefix,omitempty"`
}

// CaddyModule returns the Caddy module information.
func (Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers." + HandlerName,
		New: func() caddy.Module { return new(Handler) },
	}
}

// Response is the body of /__zipgo/flags.json.
type Response struct {
	Flags map[string]bool `json:"flags"`
}

// ServeHTTP answers the flags endpoint, refuses the paths of the flags that
// are off and hands everything else to the site.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	rel, ok := h.relative(r.URL.Path)
	if !ok {
		return next.ServeHTTP(w, r)
	}
	if rel == sites.FlagsPath {
		h.serveFlags(w, r)
		return nil
	}
	if h.closed(rel) {
		notFound(w)
		return nil
	}
	return next.ServeHTTP(w, r)
}

// relative is the request path inside the site, cleaned the way the file
// server will clean it (so //arena or /x/../arena can't slip past a closed
// /arena).
func (h *Handler) relative(p string) (string, bool) {
	if h.Prefix != "" {
		if p != h.Prefix && !strings.HasPrefix(p, h.Prefix+"/") {
			return "", false
		}
		p = strings.TrimPrefix(p, h.Prefix)
	}
	if p == "" {
		p = "/"
	}
	return path.Clean("/" + p), true
}

// closed reports whether rel is under one of the off flags' paths.
func (h *Handler) closed(rel string) bool {
	for _, c := range h.Closed {
		if rel == c || strings.HasPrefix(rel, c+"/") {
			return true
		}
	}
	return false
}

func (h *Handler) serveFlags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flags := h.Flags
	if flags == nil {
		flags = map[string]bool{}
	}
	b, _ := json.Marshal(Response{Flags: flags})
	w.Header().Set("Content-Type", "application/json")
	// A switch must take effect on the next page load, not when a cache
	// expires.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(append(b, '\n'))
	}
}

// notFound is what a closed path answers: the same as a path that doesn't
// exist, so it says nothing about the feature behind it.
func notFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte("404 page not found\n"))
}

// Interface guard.
var _ caddyhttp.MiddlewareHandler = (*Handler)(nil)
