package admin

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"zipgo/internal/password"
	"zipgo/internal/sites"
)

// minPasswordLen is the shortest password the page accepts. The CLI takes
// anything; a phone keyboard is where "1234" happens.
const minPasswordLen = 8

// SetLock puts the site at id behind a password ("set" with pw, or "generate",
// which makes one up and returns it — it is shown once, only its hash is
// kept), or takes its password off ("remove"). It edits the "password" block
// exactly as `zipgo password set|remove` does, keeping the block's title and
// session length and every other key. A deploy never undoes it.
func SetLock(root, tmpDir, self, id, action, pw string) (generated string, err error) {
	dir, err := resolve(root, id)
	if err != nil {
		return "", err
	}
	if self != "" && sameDir(dir, self) {
		return "", errors.New("the back-office's own password is changed with zipgo admin set")
	}
	switch action {
	case "set":
		if len(pw) < minPasswordLen {
			return "", fmt.Errorf("use at least %d characters", minPasswordLen)
		}
	case "generate":
		if pw, err = password.Generate(); err != nil {
			return "", err
		}
		generated = pw
	case "remove":
	default:
		return "", fmt.Errorf("unknown action %q", action)
	}
	var lock *sites.Password
	if action != "remove" {
		// Hash before taking the lock: bcrypt is the slow part.
		hash, err := password.Hash(pw)
		if err != nil {
			return "", err
		}
		lock = &sites.Password{Hash: hash}
	}

	switchMu.Lock()
	defer switchMu.Unlock()
	file := filepath.Join(dir, sites.ConfigFileName)
	raw, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	cfg, err := sites.ParseConfig(orEmpty(raw))
	if err != nil {
		return "", fmt.Errorf("%s: %w — fix it by hand first", sites.ConfigFileName, err)
	}
	if lock != nil && cfg.Password != nil {
		lock.Title, lock.SessionDays = cfg.Password.Title, cfg.Password.SessionDays
	}
	if lock == nil && cfg.Password == nil {
		return "", nil
	}
	out, err := password.SetPassword(raw, lock)
	if err != nil {
		return "", err
	}
	if bytes.Equal(bytes.TrimSpace(out), []byte("{}")) {
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			return "", err
		}
		return generated, nil
	}
	return generated, writeFile(dir, file, tmpDir, out)
}

func orEmpty(raw []byte) []byte {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []byte("{}")
	}
	return raw
}

// Size is what a site's own files weigh: its folder without the nested
// subdomain folders, which are sites of their own.
type Size struct {
	Bytes int64 `json:"bytes"`
	Files int   `json:"files"`
}

// sizesTTL: walking every file of every site is the one slow thing the page
// asks for (seconds on a Pi's SD card), so a result is reused for a minute.
const sizesTTL = time.Minute

var sizesCache struct {
	sync.Mutex
	root string
	at   time.Time
	val  map[string]Size
}

// Sizes returns every site's Size, keyed by Site.ID.
func Sizes(root string) (map[string]Size, error) {
	sizesCache.Lock()
	defer sizesCache.Unlock()
	if sizesCache.root == root && time.Since(sizesCache.at) < sizesTTL {
		return sizesCache.val, nil
	}
	out := map[string]Size{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable corner shouldn't hide every size
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		id := siteOf(root, p)
		s := out[id]
		s.Bytes += info.Size()
		s.Files++
		out[id] = s
		return nil
	})
	if err != nil {
		return nil, err
	}
	sizesCache.root, sizesCache.at, sizesCache.val = root, time.Now(), out
	return out, nil
}

// siteOf is the ID of the site a file belongs to: the deepest chain of
// trailing-dot folders above it, starting at root.
func siteOf(root, file string) string {
	rel, err := filepath.Rel(root, filepath.Dir(file))
	if err != nil || rel == "." {
		return ""
	}
	id := ""
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if !strings.HasSuffix(part, ".") || strings.HasPrefix(part, ".") {
			break
		}
		id = path.Join(id, part)
	}
	return id
}
