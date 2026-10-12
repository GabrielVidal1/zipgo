package builder

import (
	"html"
	"path"
	"path/filepath"
	"strings"

	"zipgo/internal/admin"
	"zipgo/internal/sites"
)

// adminHandler builds the back-office of an "admin": true site (internal/admin):
// a page managing the site its folder is nested in — the whole domain when the
// folder sits at the domain root. siteHandler puts the site's login page in
// front of it; with no "password" there is no login page to put, so the
// back-office is refused outright rather than served to anyone (fail closed).
func adminHandler(s sites.Site, host, stripPrefix string) (obj, error) {
	if !s.Config.HasPassword() {
		return adminRefusal(host), nil
	}
	self, err := filepath.Abs(s.Path)
	if err != nil {
		return nil, err
	}
	// The managed site is the parent folder; an apex marked admin (doctor
	// errors on it) can only manage itself.
	root, rootHost, rootPath := self, host, stripPrefix
	if !s.IsApex() {
		root = filepath.Dir(self)
		rootHost = strings.TrimPrefix(host, s.Labels[0]+".")
		if stripPrefix != "" {
			rootPath = path.Dir(stripPrefix)
		}
	}
	// New configs are written in the domains folder: the domain folder's
	// parent, one level above the apex.
	domainDir := self
	for range s.Labels {
		domainDir = filepath.Dir(domainDir)
	}
	h := obj{
		"handler":   admin.HandlerName,
		"root":      root,
		"root_host": rootHost,
		"self":      self,
		"tmp_dir":   filepath.Dir(domainDir),
	}
	if stripPrefix != "" {
		h["prefix"] = stripPrefix
		h["root_path"] = rootPath
	}
	return h, nil
}

// adminRefusal is what an admin site without a password answers: a 403 that
// says how to fix it, and nothing about the sites it would manage.
func adminRefusal(host string) obj {
	body := "<!doctype html><meta charset=\"utf-8\"><meta name=\"robots\" content=\"noindex\">" +
		"<title>403 Forbidden</title><p>This back-office has no password, so it stays closed.</p>" +
		"<p>Set one with <code>zipgo admin set " + html.EscapeString(host) + "</code>.</p>\n"
	return obj{"handler": "subroute", "routes": arr{
		obj{"handle": arr{securityHeaders("", map[string]string{"Cache-Control": "no-store", "X-Robots-Tag": "noindex, nofollow"})}},
		obj{"handle": arr{obj{
			"handler":     "static_response",
			"status_code": "403",
			"headers":     obj{"Content-Type": arr{"text/html; charset=utf-8"}},
			"body":        body,
		}}},
	}}
}
