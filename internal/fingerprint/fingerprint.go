// Package fingerprint tells a fingerprinted build file — one whose name carries
// a hash of its content, like Vite's assets/index-F6vy2umU.js — from an
// ordinary one, and registers that test as a Caddy request matcher
// (http.matchers.zipgo_fingerprinted).
//
// A fingerprinted file never changes under its name: a new build writes a new
// name. So it can be cached for a year without revalidating
// (`Cache-Control: public, max-age=31536000, immutable`), while every other
// file is revalidated on each use. The test is a heuristic, so it errs on the
// side of "not fingerprinted": a hashed file it misses only costs a cheap 304,
// while an ordinary file taken for a hashed one would be stuck in browsers for a
// year after it changes.
package fingerprint

import (
	"net/http"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// bundleDirs are the folders bundlers write their hashed output to. A file
// only counts as fingerprinted inside one of them: a photo named
// beach-IMG_1234.jpg at the root of a site looks like a hash but isn't one.
// Vite/Rollup write to assets/, Create React App to static/, SvelteKit to
// _app/immutable/, Next.js to _next/static/, Parcel/esbuild setups often to
// build/ or dist/.
var bundleDirs = []string{"assets", "static", "immutable", "_next", "build", "dist"}

// Is reports whether the URL path names a fingerprinted file: inside one of
// the bundleDirs (at any depth), with a last segment of the form
// <name>[-.]<hash>.<ext>, where the hash is either
//   - exactly 8 characters of [A-Za-z0-9_-] with at least one digit and one
//     letter (Vite, Rollup, esbuild, webpack's [contenthash:8]), or
//   - 16 to 64 lowercase hex digits (webpack's default [contenthash]).
func Is(path string) bool {
	segs := strings.Split(path, "/")
	if len(segs) < 2 {
		return false
	}
	name := segs[len(segs)-1]
	inBundle := false
	for _, s := range segs[:len(segs)-1] {
		for _, d := range bundleDirs {
			if s == d {
				inBundle = true
			}
		}
	}
	if !inBundle {
		return false
	}
	dot := strings.LastIndexByte(name, '.')
	if dot <= 0 || dot == len(name)-1 || !alnum(name[dot+1:]) {
		return false
	}
	stem := name[:dot]
	// An 8-character base64url hash may itself hold a '-' (index-B3x_9-aZ),
	// so it is read as the last 8 characters behind a separator rather than
	// split at the last '-'.
	if n := len(stem); n > 9 && (stem[n-9] == '-' || stem[n-9] == '.') && short(stem[n-8:]) {
		return true
	}
	sep := strings.LastIndexAny(stem, "-.")
	return sep > 0 && long(stem[sep+1:])
}

// short is the 8-character form: [A-Za-z0-9_-], at least one digit and one
// letter.
func short(tok string) bool {
	digit, letter := false, false
	for _, c := range tok {
		switch {
		case c >= '0' && c <= '9':
			digit = true
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			letter = true
		case c == '_' || c == '-':
		default:
			return false
		}
	}
	return digit && letter
}

// long is the hex form: 16 to 64 lowercase hex digits.
func long(tok string) bool {
	if len(tok) < 16 || len(tok) > 64 {
		return false
	}
	for _, c := range tok {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func alnum(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// MatcherName is the JSON key of the matcher in a route's match set.
const MatcherName = "zipgo_fingerprinted"

func init() { caddy.RegisterModule(Matcher{}) }

// Matcher matches a request whose path names a fingerprinted file (see Is).
// It is configured with an empty object: {"zipgo_fingerprinted": {}}.
type Matcher struct{}

// CaddyModule returns the Caddy module information.
func (Matcher) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.matchers." + MatcherName,
		New: func() caddy.Module { return new(Matcher) },
	}
}

// MatchWithError implements caddyhttp.RequestMatcherWithError.
func (Matcher) MatchWithError(r *http.Request) (bool, error) { return Is(r.URL.Path), nil }

var _ caddyhttp.RequestMatcherWithError = (*Matcher)(nil)
