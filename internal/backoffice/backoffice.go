// Package backoffice provides an embedded web UI for managing sites.
// It is served at https://backoffice.<rootDomain> by the same Caddy instance,
// protected by HTTP Basic Auth.
package backoffice

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"zipgo/internal/config"
	"zipgo/internal/sites"
)

// Handler returns an http.Handler for the backoffice UI.
//
//   - domainsDir   path to the domains/ directory
//   - username     Basic Auth username
//   - password     Basic Auth password
//   - onReload     called after upload/delete to trigger a Caddy config reload
//   - urlFor       returns the public URL for a given domain + site name
//   - vinceURL     public base URL of the Vince analytics instance
//     (e.g. "https://analytics.example.com" or "http://localhost:8898").
//     Pass an empty string to disable script injection.
func Handler(
	domainsDir, username, password string,
	onReload func() error,
	urlFor func(domain, name string) string,
	vinceURL string,
) http.Handler {
	mux := http.NewServeMux()

	bo := &backoffice{
		domainsDir: domainsDir,
		username:   username,
		password:   password,
		onReload:   onReload,
		urlFor:     urlFor,
		vinceURL:   vinceURL,
	}

	mux.HandleFunc("/", bo.auth(bo.handleIndex))
	mux.HandleFunc("/upload", bo.auth(bo.handleUpload))
	mux.HandleFunc("/delete", bo.auth(bo.handleDelete))

	return mux
}

type backoffice struct {
	domainsDir string
	username   string
	password   string
	onReload   func() error
	urlFor     func(domain, name string) string
	vinceURL   string // public Vince base URL; empty = injection disabled
}

// ---------- auth middleware ----------

func (bo *backoffice) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != bo.username || pass != bo.password {
			w.Header().Set("WWW-Authenticate", `Basic realm="zipgo backoffice"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// ---------- index ----------

type pageData struct {
	Sites   []siteInfo
	Domains []string // available domain names for the upload selector
	Flash   string
	IsError bool
}

type siteInfo struct {
	Domain  string
	Name    string
	IsSPA   bool
	Files   int
	ModTime string
	URL     string
}

func (bo *backoffice) handleIndex(w http.ResponseWriter, r *http.Request) {
	flash := r.URL.Query().Get("flash")
	isErr := r.URL.Query().Get("error") == "1"
	data := pageData{Flash: flash, IsError: isErr}

	domains, _ := config.ReadDomains(bo.domainsDir)
	data.Domains = domains

	for _, domain := range domains {
		domainDir := filepath.Join(bo.domainsDir, domain)
		discovered, _ := sites.Discover(domainDir)
		for _, s := range discovered {
			count := countFiles(s.Path)
			mod := latestMod(s.Path)
			data.Sites = append(data.Sites, siteInfo{
				Domain:  domain,
				Name:    s.Name,
				IsSPA:   s.IsSPA,
				Files:   count,
				ModTime: mod,
				URL:     bo.urlFor(domain, s.Name),
			})
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTmpl.Execute(w, data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

// ---------- upload ----------

func (bo *backoffice) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// 100 MB max upload
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		bo.redirectFlash(w, r, "Upload too large (max 100 MB)", true)
		return
	}

	domain := strings.TrimSpace(r.FormValue("domain"))
	if domain == "" {
		bo.redirectFlash(w, r, "Domain is required", true)
		return
	}
	if !isValidDomain(domain) {
		bo.redirectFlash(w, r, "Invalid domain name", true)
		return
	}

	siteName := strings.TrimSpace(r.FormValue("name"))
	if siteName == "" {
		bo.redirectFlash(w, r, "Site name is required", true)
		return
	}
	if !isValidName(siteName) {
		bo.redirectFlash(w, r, "Site name may only contain letters, numbers, hyphens and underscores", true)
		return
	}

	file, header, err := r.FormFile("zipfile")
	if err != nil {
		bo.redirectFlash(w, r, "Could not read uploaded file: "+err.Error(), true)
		return
	}
	defer file.Close()

	buf, err := io.ReadAll(file)
	if err != nil {
		bo.redirectFlash(w, r, "Read error: "+err.Error(), true)
		return
	}

	destDir := filepath.Join(bo.domainsDir, domain, siteName)
	_ = os.RemoveAll(destDir)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		bo.redirectFlash(w, r, "Could not create directory: "+err.Error(), true)
		return
	}

	lowerName := strings.ToLower(header.Filename)
	switch {
	case strings.HasSuffix(lowerName, ".zip"):
		if err := extractZip(buf, destDir); err != nil {
			_ = os.RemoveAll(destDir)
			bo.redirectFlash(w, r, "Invalid zip: "+err.Error(), true)
			return
		}
	case strings.HasSuffix(lowerName, ".html"), strings.HasSuffix(lowerName, ".htm"):
		dest := filepath.Join(destDir, "index.html")
		if err := os.WriteFile(dest, buf, 0o644); err != nil {
			_ = os.RemoveAll(destDir)
			bo.redirectFlash(w, r, "Write error: "+err.Error(), true)
			return
		}
	default:
		_ = os.RemoveAll(destDir)
		bo.redirectFlash(w, r, "Unsupported file type — upload a .zip or .html file", true)
		return
	}

	// ---- inject Vince analytics tracking script into all HTML files ----
	if bo.vinceURL != "" {
		siteDomain := bo.siteDataDomain(domain, siteName)
		injectVinceScript(destDir, siteDomain, bo.vinceURL)
	}

	if bo.onReload != nil {
		if err := bo.onReload(); err != nil {
			bo.redirectFlash(w, r, fmt.Sprintf("Site uploaded but reload failed: %v", err), true)
			return
		}
	}

	bo.redirectFlash(w, r, fmt.Sprintf("✅ Site \"%s\" deployed to %s", siteName, domain), false)
}

// ---------- delete ----------

func (bo *backoffice) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	domain := strings.TrimSpace(r.FormValue("domain"))
	siteName := strings.TrimSpace(r.FormValue("name"))
	if domain == "" || !isValidDomain(domain) || siteName == "" || !isValidName(siteName) {
		bo.redirectFlash(w, r, "Invalid domain or site name", true)
		return
	}

	target := filepath.Join(bo.domainsDir, domain, siteName)
	// Safety: make sure we stay inside domainsDir
	abs, _ := filepath.Abs(target)
	domainsAbs, _ := filepath.Abs(bo.domainsDir)
	if !strings.HasPrefix(abs, domainsAbs+string(filepath.Separator)) {
		bo.redirectFlash(w, r, "Invalid path", true)
		return
	}

	if err := os.RemoveAll(target); err != nil {
		bo.redirectFlash(w, r, "Delete failed: "+err.Error(), true)
		return
	}

	if bo.onReload != nil {
		_ = bo.onReload()
	}

	bo.redirectFlash(w, r, fmt.Sprintf("🗑️  Site \"%s\" deleted from %s", siteName, domain), false)
}

// ---------- Vince script injection ------------------------------------------

// siteDataDomain returns the value to use as the Vince data-domain attribute
// for the given domain + site name.
//
//   - "root" maps to the apex domain (e.g. "example.com")
//   - others map to subdomain (e.g. "blog.example.com")
func (bo *backoffice) siteDataDomain(domain, siteName string) string {
	if domain == "" {
		return siteName
	}
	if siteName == "root" {
		return domain
	}
	return siteName + "." + domain
}

// injectVinceScript walks destDir and injects the Vince tracking <script> tag
// into the <head> of every .html / .htm file.  It is idempotent: files that
// already contain a snippet for the same domain are skipped.
func injectVinceScript(destDir, domain, vinceURL string) {
	snippet := fmt.Sprintf(
		`<script defer data-domain="%s" src="%s/js/script.js"></script>`,
		domain, vinceURL,
	)
	marker := `data-domain="` + domain + `"`

	_ = filepath.Walk(destDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		lower := strings.ToLower(info.Name())
		if !strings.HasSuffix(lower, ".html") && !strings.HasSuffix(lower, ".htm") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		// Idempotency check — don't inject twice.
		if bytes.Contains(data, []byte(marker)) {
			return nil
		}

		// Try to inject before </head>, then before </body>, then just append.
		injected := injectBeforeTag(data, "</head>", snippet)
		if injected == nil {
			injected = injectBeforeTag(data, "</body>", snippet)
		}
		if injected == nil {
			injected = append(data, []byte("\n"+snippet+"\n")...)
		}

		_ = os.WriteFile(path, injected, info.Mode())
		return nil
	})
}

// injectBeforeTag inserts snippet on its own line immediately before the first
// occurrence of tag (case-insensitive).  Returns nil if tag is not found.
func injectBeforeTag(data []byte, tag, snippet string) []byte {
	lower := bytes.ToLower(data)
	idx := bytes.Index(lower, []byte(strings.ToLower(tag)))
	if idx < 0 {
		return nil
	}
	out := make([]byte, 0, len(data)+len(snippet)+2)
	out = append(out, data[:idx]...)
	out = append(out, []byte("\n"+snippet+"\n")...)
	out = append(out, data[idx:]...)
	return out
}

// ---------- helpers ----------

func (bo *backoffice) redirectFlash(w http.ResponseWriter, r *http.Request, msg string, isErr bool) {
	errParam := ""
	if isErr {
		errParam = "&error=1"
	}
	http.Redirect(w, r, "/?flash="+template.URLQueryEscaper(msg)+errParam, http.StatusSeeOther)
}

func isValidName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func isValidDomain(name string) bool {
	return name != "" &&
		strings.Contains(name, ".") &&
		!strings.Contains(name, "/") &&
		!strings.Contains(name, "..") &&
		!strings.HasPrefix(name, ".")
}

func countFiles(dir string) int {
	count := 0
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			count++
		}
		return nil
	})
	return count
}

func latestMod(dir string) string {
	var latest time.Time
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		return nil
	})
	if latest.IsZero() {
		return "—"
	}
	return latest.Format("2006-01-02 15:04")
}

// extractZip extracts a zip archive (given as raw bytes) into destDir.
// It strips a single top-level directory if the zip was created as folder/...
func extractZip(data []byte, destDir string) error {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}

	prefix := detectZipPrefix(r.File)

	for _, f := range r.File {
		relPath := strings.TrimPrefix(f.Name, prefix)
		if relPath == "" || strings.HasPrefix(relPath, "..") {
			continue
		}

		dest := filepath.Join(destDir, filepath.FromSlash(relPath))

		// Guard against zip-slip.
		absDir, _ := filepath.Abs(destDir)
		absDest, _ := filepath.Abs(dest)
		if !strings.HasPrefix(absDest, absDir+string(filepath.Separator)) && absDest != absDir {
			return fmt.Errorf("zip slip detected: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}

		out, err := os.Create(dest)
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			out.Close()
			return err
		}

		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// detectZipPrefix returns a common path prefix to strip when all entries share
// a single top-level directory (e.g. zipped as mysite/ instead of the contents directly).
func detectZipPrefix(files []*zip.File) string {
	if len(files) == 0 {
		return ""
	}
	parts := strings.SplitN(files[0].Name, "/", 2)
	if len(parts) < 2 {
		return ""
	}
	candidate := parts[0] + "/"
	for _, f := range files {
		if !strings.HasPrefix(f.Name, candidate) {
			return ""
		}
	}
	return candidate
}

// ---------- favicon (inline base64 so zero external deps) ----------

var faviconB64 = "PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHZpZXdCb3g9IjAgMCAzMiAzMiI+PHRleHQgeT0iMjYiIGZvbnQtc2l6ZT0iMjgiPvCfjon8L3RleHQ+PC9zdmc+"

func init() {
	_ = faviconB64
}

// FaviconBytes returns the favicon as raw bytes.
func FaviconBytes() []byte {
	b, _ := base64.StdEncoding.DecodeString(faviconB64)
	return b
}

// ---------- HTML template ----------
//
//go:embed template.html
var pageHTML string
var pageTmpl = template.Must(template.New("page").Parse(pageHTML))