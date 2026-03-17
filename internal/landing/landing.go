// Package landing generates a default root landing page that lists all hosted
// sites with metadata scraped from their index.html <head>.
package landing

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/net/html"

	"sitehost/internal/sites"
)

// SiteCard holds display info for one site on the landing page.
type SiteCard struct {
	Name        string
	URL         string
	Title       string
	Description string
	IsSPA       bool
}

// Generate writes an index.html to destDir and returns its path.
// urlFor maps a site name to its public URL.
func Generate(discovered []sites.Site, urlFor func(string) string, destDir string) (string, error) {
	var cards []SiteCard
	for _, s := range discovered {
		meta := scrapeHead(filepath.Join(s.Path, "index.html"))
		title := meta.title
		if title == "" {
			title = s.Name
		}
		cards = append(cards, SiteCard{
			Name:        s.Name,
			URL:         urlFor(s.Name),
			Title:       title,
			Description: meta.description,
			IsSPA:       s.IsSPA,
		})
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}

	outPath := filepath.Join(destDir, "index.html")
	f, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if err := landingTmpl.Execute(f, cards); err != nil {
		return "", err
	}
	return destDir, nil
}

// ---------- head metadata scraper -------------------------------------------

type headMeta struct {
	title       string
	description string
}

func scrapeHead(indexPath string) headMeta {
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return headMeta{}
	}
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return headMeta{}
	}

	var meta headMeta
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "title":
				if n.FirstChild != nil {
					meta.title = strings.TrimSpace(n.FirstChild.Data)
				}
			case "meta":
				name, content := attrVal(n, "name"), attrVal(n, "content")
				prop := attrVal(n, "property")
				switch strings.ToLower(name) {
				case "description":
					meta.description = content
				}
				// Also accept og:description as fallback
				if meta.description == "" && strings.ToLower(prop) == "og:description" {
					meta.description = content
				}
				// og:title as fallback
				if meta.title == "" && strings.ToLower(prop) == "og:title" {
					meta.title = content
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return meta
}

func attrVal(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

// ---------- template --------------------------------------------------------

//go:embed home.html
var landingHTML string

var landingTmpl = template.Must(template.New("landing").Funcs(template.FuncMap{
	"initial": func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" {
			return "?"
		}
		return strings.ToUpper(string([]rune(s)[0]))
	},
	"hue": func(s string) int {
		// deterministic hue from name
		h := 0
		for _, c := range s {
			h = (h*31 + int(c)) % 360
		}
		return h
	},
	"truncate": func(s string, n int) string {
		r := []rune(s)
		if len(r) <= n {
			return s
		}
		return string(r[:n]) + "…"
	},
}).Parse(landingHTML))

// Unused — satisfies go vet if template funcs reference fmt
var _ = fmt.Sprintf
