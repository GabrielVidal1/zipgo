package deploy

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
)

func unbrotli(t *testing.T, p string) string {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, err := io.ReadAll(brotli.NewReader(f))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func ungzip(t *testing.T, p string) string {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPrecompress(t *testing.T) {
	src := t.TempDir()
	js := strings.Repeat("export const x = 'zipgo';\n", 100)
	writeFiles(t, src, map[string]string{
		"index.html":         "<!doctype html><p>hi</p>",
		"assets/app-Ab12.js": js,
		"assets/logo.png":    "png bytes",
		"assets/own.js":      js,
		"assets/own.js.br":   "the bundler's own brotli",
		".hidden.js":         js,
		".well-known/x.json": `{}`,
		"pb./index.html":     "a nested site",
		"data/Big.JSON":      `{"a": 1}`,
	})
	if err := os.Symlink(filepath.Join(src, "index.html"), filepath.Join(src, "link.html")); err != nil {
		t.Fatal(err)
	}

	res, err := Precompress(src)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(res.Dir)

	var got []string
	filepath.WalkDir(res.Dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(res.Dir, p)
			got = append(got, rel)
		}
		return nil
	})
	want := []string{
		"assets/app-Ab12.js.br", "assets/app-Ab12.js.gz",
		"assets/own.js.gz", // the build shipped its own .br
		"data/Big.JSON.br", "data/Big.JSON.gz",
		"index.html.br", "index.html.gz",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("siblings:\n got %v\nwant %v", got, want)
	}
	if res.Files != 4 {
		t.Errorf("Files = %d, want 4", res.Files)
	}
	if s := unbrotli(t, filepath.Join(res.Dir, "assets/app-Ab12.js.br")); s != js {
		t.Error(".br does not decode to the file")
	}
	if s := ungzip(t, filepath.Join(res.Dir, "assets/app-Ab12.js.gz")); s != js {
		t.Error(".gz does not decode to the file")
	}
	if res.Br >= res.Raw {
		t.Errorf("brotli %d ≥ raw %d", res.Br, res.Raw)
	}
}

func TestSiblingExcludes(t *testing.T) {
	got := siblingExcludes([]string{"config.js", "*.map", "drafts/"})
	want := []string{"config.js.br", "config.js.gz", "*.map.br", "*.map.gz"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRsyncArgsExtraSources(t *testing.T) {
	args := rsyncArgs(Options{Delete: true}, "dist/", "h:/d/", false, "/tmp/pre/")
	n := len(args)
	if args[n-3] != "dist/" || args[n-2] != "/tmp/pre/" || args[n-1] != "h:/d/" {
		t.Fatalf("sources/dest = %v", args[n-3:])
	}
}

// A real deploy ships the siblings beside the files, and the mirror removes a
// sibling whose file the next build no longer has.
func TestRunShipsSiblings(t *testing.T) {
	base := localDeploy(t)
	site := filepath.Join(base, "example.com", "app.")
	js := strings.Repeat("let a = 1;\n", 300)

	src := t.TempDir()
	writeFiles(t, src, map[string]string{"index.html": "<p>v1</p>", "assets/a-Ab12Cd34.js": js})
	o := Options{Delete: true, SSH: "u@h:" + base, Jobs: []Job{{Host: "app.example.com", Src: src}}}
	if err := Run(o); err != nil {
		t.Fatal(err)
	}
	if s := unbrotli(t, filepath.Join(site, "assets/a-Ab12Cd34.js.br")); s != js {
		t.Fatal("the shipped .br is not the file")
	}
	if _, err := os.Stat(filepath.Join(site, "index.html.gz")); err != nil {
		t.Fatal("index.html.gz was not shipped")
	}
	if left, _ := filepath.Glob(filepath.Join(src, "*.br")); len(left) != 0 {
		t.Fatalf("siblings written into the source: %v", left)
	}

	// v2 drops the script: its siblings go with it. Its index.html has the
	// same size and a pinned mtime (a reproducible build): rsync's quick check
	// can't see the change, and the sibling must not keep v1 anyway.
	os.Remove(filepath.Join(src, "assets/a-Ab12Cd34.js"))
	writeFiles(t, src, map[string]string{"index.html": "<p>v2</p>"})
	pinned := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	os.Chtimes(filepath.Join(src, "index.html"), pinned, pinned)
	os.Chtimes(filepath.Join(site, "index.html"), pinned, pinned)
	// The previous deploy was a while ago (two deploys in one second are
	// beyond rsync's quick check anyway).
	hourAgo := time.Now().Add(-time.Hour)
	siblings, _ := filepath.Glob(filepath.Join(site, "*.*.*"))
	for _, p := range siblings {
		os.Chtimes(p, hourAgo, hourAgo)
	}
	if err := Run(o); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"assets/a-Ab12Cd34.js.br", "assets/a-Ab12Cd34.js.gz"} {
		if _, err := os.Stat(filepath.Join(site, p)); !os.IsNotExist(err) {
			t.Errorf("%s survived the mirror", p)
		}
	}
	if s := unbrotli(t, filepath.Join(site, "index.html.br")); s != "<p>v2</p>" {
		t.Errorf("index.html.br = %q, want v2", s)
	}

	// --no-precompress ships no sibling (and the mirror removes the old ones).
	o.NoPrecompress = true
	if err := Run(o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(site, "index.html.br")); !os.IsNotExist(err) {
		t.Error("--no-precompress: index.html.br still there")
	}
}
