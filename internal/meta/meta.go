// Package meta extracts lightweight page metadata (title, description,
// OpenGraph tags, favicon) from a site's index.html. It is used to build the
// /sub-domains-meta JSON endpoint so a page can dynamically list subdomains.
package meta

import (
	"os"
	"strings"

	"golang.org/x/net/html"
)

// Meta is the metadata extracted from a single index.html.
type Meta struct {
	Title       string            `json:"title,omitempty"`
	Description string            `json:"description,omitempty"`
	OpenGraph   map[string]string `json:"openGraph,omitempty"`
	Icon        string            `json:"icon,omitempty"`
	// Zipgo holds custom metadata declared via <meta property="zipgo:KEY">
	// tags, keyed by KEY (the part after the "zipgo:" prefix).
	Zipgo map[string]string `json:"zipgo,omitempty"`
}

// Extract parses indexPath and returns its metadata. Missing files or absent
// tags are not errors — a zero-value (or partially filled) Meta is returned. A
// non-nil error is only returned when the file exists but cannot be parsed as
// HTML.
func Extract(indexPath string) (Meta, error) {
	f, err := os.Open(indexPath)
	if err != nil {
		// No index.html (or unreadable): nothing to extract, not fatal.
		return Meta{}, nil
	}
	defer f.Close()

	doc, err := html.Parse(f)
	if err != nil {
		return Meta{}, err
	}

	var m Meta
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "title":
				if m.Title == "" && n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
					m.Title = strings.TrimSpace(n.FirstChild.Data)
				}
			case "meta":
				name, property, content := attr(n, "name"), attr(n, "property"), attr(n, "content")
				if strings.EqualFold(name, "description") && m.Description == "" {
					m.Description = content
				}
				lowProp := strings.ToLower(property)
				if key := strings.TrimPrefix(lowProp, "og:"); key != lowProp && key != "" {
					if m.OpenGraph == nil {
						m.OpenGraph = map[string]string{}
					}
					if _, ok := m.OpenGraph[key]; !ok {
						m.OpenGraph[key] = content
					}
				}
				if key := strings.TrimPrefix(lowProp, "zipgo:"); key != lowProp && key != "" {
					if m.Zipgo == nil {
						m.Zipgo = map[string]string{}
					}
					if _, ok := m.Zipgo[key]; !ok {
						m.Zipgo[key] = content
					}
				}
			case "link":
				rel := strings.ToLower(attr(n, "rel"))
				if m.Icon == "" && (rel == "icon" || rel == "shortcut icon") {
					m.Icon = attr(n, "href")
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	return m, nil
}

// attr returns the value of the named attribute (case-insensitive), or "".
func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}
