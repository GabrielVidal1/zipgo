package gate

import (
	"bytes"
	"html/template"
)

// page is the one login page every password-protected site shows. Only the
// title changes from site to site, on purpose: it is a door, not a landing
// page, so there is nothing else to configure. It is self-contained (inline
// CSS, no script, no external request), so the strict CSP below holds.
var page = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<title>{{.Title}}</title>
<style>
:root { color-scheme: light dark; --bg:#f4f4f2; --fg:#1d1d1b; --muted:#6b6b66;
  --card:#fff; --line:#d6d6d0; --accent:#2f6f4f; --err:#b3261e; }
@media (prefers-color-scheme: dark) { :root { --bg:#121311; --fg:#e8e8e3;
  --muted:#9a9a93; --card:#1b1c1a; --line:#33352f; --accent:#7cc4a0; --err:#f2b8b5; } }
* { box-sizing: border-box; }
body { margin:0; min-height:100vh; display:grid; place-items:center; padding:16px;
  background:var(--bg); color:var(--fg);
  font:16px/1.45 system-ui, -apple-system, "Segoe UI", sans-serif; }
form { width:100%; max-width:340px; background:var(--card); border:1px solid var(--line);
  border-radius:12px; padding:24px; display:grid; gap:12px; }
h1 { font-size:1.05rem; margin:0; font-weight:600; overflow-wrap:anywhere; }
p { margin:0; color:var(--muted); font-size:.9rem; }
input { font:inherit; padding:10px 12px; border-radius:8px; border:1px solid var(--line);
  background:transparent; color:inherit; width:100%; }
input:focus { outline:2px solid var(--accent); outline-offset:1px; }
button { font:inherit; padding:10px 12px; border-radius:8px; border:0; cursor:pointer;
  background:var(--accent); color:var(--bg); font-weight:600; }
.err { color:var(--err); }
</style></head>
<body>
<form method="post" action="{{.Action}}">
<h1>{{.Title}}</h1>
<input type="password" name="password" placeholder="Password" aria-label="Password"
  autocomplete="current-password" required autofocus>
<input type="hidden" name="next" value="{{.Next}}">
<button type="submit">Enter</button>
{{if .Error}}<p class="err" role="alert">{{.Error}}</p>{{end}}
</form>
</body></html>
`))

// pageHeaders go on every login page. no-store keeps the page (and the fact
// that a URL is locked) out of every cache; the CSP allows the inline style and
// a same-origin form post, and nothing else.
var pageHeaders = map[string]string{
	"Content-Type":            "text/html; charset=utf-8",
	"Cache-Control":           "no-store",
	"X-Robots-Tag":            "noindex, nofollow",
	"Referrer-Policy":         "no-referrer",
	"X-Content-Type-Options":  "nosniff",
	"X-Frame-Options":         "DENY",
	"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'",
}

// pageData fills the template.
type pageData struct {
	Title  string
	Action string
	Next   string
	Error  string
}

// render executes the login page. html/template escapes every field.
func render(d pageData) []byte {
	var b bytes.Buffer
	if err := page.Execute(&b, d); err != nil {
		// The template is static and its data plain strings: this can't fail
		// short of a bug, and a bare page is better than none.
		return []byte("<!doctype html><title>Password required</title>")
	}
	return b.Bytes()
}
