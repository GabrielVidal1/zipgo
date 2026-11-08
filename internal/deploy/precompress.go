package deploy

import (
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/andybalholm/brotli"
)

// Pre-compression: `zipgo deploy` ships a brotli and a gzip sibling next to
// every text-like file of the build (app.js → app.js.br, app.js.gz), and the
// server's file_server answers with the sibling the browser accepts. The work
// is done once, on the machine that deploys, at the best ratios — instead of on
// every request, at a fast level, on the server (which still compresses on the
// fly whatever has no sibling).
//
// The siblings are written to a temporary folder that mirrors the source's
// layout, never into the source itself (a hand-written site's folder stays
// clean), and rsync ships both folders into the site as one tree: with the
// default --delete mirror, a sibling whose file is gone is removed with it.
//
// Every compressible file gets both siblings, however small, so a deploy always
// overwrites them: a stale sibling left from an earlier deploy (--no-delete)
// would otherwise be served in place of the file it no longer matches.

// compressibleExt are the extensions that get siblings: text and the binary
// formats that compress well. Images, fonts in woff/woff2, audio, video and
// archives are already compressed.
var compressibleExt = map[string]bool{
	".html": true, ".htm": true, ".css": true, ".js": true, ".mjs": true, ".cjs": true,
	".json": true, ".map": true, ".svg": true, ".xml": true, ".txt": true, ".md": true,
	".csv": true, ".wasm": true, ".webmanifest": true, ".ico": true, ".ttf": true,
	".otf": true, ".gltf": true,
}

// siblingExts are the siblings made for each file, in the order the server
// prefers them.
var siblingExts = []string{".br", ".gz"}

// PrecompressResult is what Precompress made.
type PrecompressResult struct {
	Dir   string // the folder holding the siblings, mirroring the source
	Files int    // source files that got siblings
	Raw   int64  // their total size
	Br    int64  // the total size of their .br siblings
}

// brotliQuality trades ratio for time on big files: quality 11 runs at about
// 1 MB/s, so a multi-megabyte bundle (a game's wasm) uses a faster level.
func brotliQuality(size int64) int {
	switch {
	case size <= 1<<20:
		return brotli.BestCompression
	case size <= 8<<20:
		return 9
	default:
		return 6
	}
}

func gzipLevel(size int64) int {
	if size <= 8<<20 {
		return gzip.BestCompression
	}
	return 6
}

// Precompress writes the siblings of every compressible file under src into a
// new temporary folder and returns it; the caller removes res.Dir. Dotfiles
// and dot-folders (never served), nested subdomain folders (trailing dot:
// other sites, which the deploy does not ship) and symlinks are skipped, as is
// a sibling the build already made itself (a bundler's own app.js.br).
func Precompress(src string) (res PrecompressResult, err error) {
	var files []string
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == src {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || strings.HasPrefix(name, ".") {
			return nil
		}
		if compressibleExt[strings.ToLower(filepath.Ext(name))] {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return res, fmt.Errorf("precompress %s: %w", src, err)
	}
	dir, err := os.MkdirTemp("", "zipgo-precompress-")
	if err != nil {
		return res, err
	}
	res.Dir = dir

	type outcome struct {
		raw, br int64
		err     error
	}
	jobs := make(chan string)
	out := make(chan outcome)
	var wg sync.WaitGroup
	for range max(1, runtime.NumCPU()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				raw, br, err := compressOne(src, dir, p)
				out <- outcome{raw, br, err}
			}
		}()
	}
	go func() {
		for _, p := range files {
			jobs <- p
		}
		close(jobs)
		wg.Wait()
		close(out)
	}()
	for o := range out {
		if o.err != nil && err == nil {
			err = o.err
		}
		if o.raw >= 0 && o.err == nil {
			res.Files++
			res.Raw += o.raw
			res.Br += o.br
		}
	}
	if err != nil {
		os.RemoveAll(dir)
		return PrecompressResult{}, err
	}
	return res, nil
}

// compressOne writes the missing siblings of one file into dir (at its path
// relative to src). It returns the file's size and its .br sibling's (the
// build's own when it shipped one), or raw = -1 when the build already had
// every sibling.
//
// The siblings keep their fresh modification time on purpose. Stamped with the
// file's own, a sibling of the same size as the one on the server would pass
// rsync's quick check (size + mtime) and be skipped even when the file changed
// — a build that pins mtimes (reproducible builds) or two deploys in the same
// second — and the old sibling would be served in place of the new file. Fresh,
// every sibling goes through rsync's delta check, which sends next to nothing
// for an unchanged one.
func compressOne(src, dir, p string) (raw, br int64, err error) {
	rel, err := filepath.Rel(src, p)
	if err != nil {
		return 0, 0, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return 0, 0, err
	}
	size := int64(len(data))
	made := false
	for _, ext := range siblingExts {
		if own, err := os.Stat(p + ext); err == nil {
			if ext == ".br" {
				br = own.Size()
			}
			continue
		}
		target := filepath.Join(dir, rel+ext)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return 0, 0, err
		}
		n, err := writeSibling(target, ext, data, size)
		if err != nil {
			return 0, 0, fmt.Errorf("precompress %s: %w", rel, err)
		}
		if ext == ".br" {
			br = n
		}
		made = true
	}
	if !made {
		return -1, 0, nil
	}
	return size, br, nil
}

func writeSibling(target, ext string, data []byte, size int64) (int64, error) {
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	var w io.WriteCloser
	switch ext {
	case ".br":
		w = brotli.NewWriterLevel(f, brotliQuality(size))
	case ".gz":
		gz, err := gzip.NewWriterLevel(f, gzipLevel(size))
		if err != nil {
			f.Close()
			return 0, err
		}
		w = gz
	}
	if _, err := w.Write(data); err != nil {
		f.Close()
		return 0, err
	}
	if err := w.Close(); err != nil {
		f.Close()
		return 0, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return 0, err
	}
	return st.Size(), f.Close()
}

// siblingExcludes extends the user's --exclude patterns to the siblings, so a
// file kept out of the deploy can't have its sibling shipped (and served in its
// place) anyway: "config.js" also excludes "config.js.br" and "config.js.gz".
func siblingExcludes(excludes []string) []string {
	var out []string
	for _, ex := range excludes {
		if strings.HasSuffix(ex, "/") {
			continue // a folder: its siblings are inside it, excluded with it
		}
		for _, ext := range siblingExts {
			out = append(out, ex+ext)
		}
	}
	return out
}

// humanBytes is a size for the deploy output: 1.7 MB, 410 KB, 900 B.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}
