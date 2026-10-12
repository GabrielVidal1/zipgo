package admin

import (
	"os"
	"syscall"
)

// chownLike gives file the owner and group of dir, when this process may (it
// runs as root). Best effort: a file left root-owned still serves, it is just
// awkward to replace by hand.
func chownLike(file, dir string) {
	if os.Geteuid() != 0 {
		return
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		_ = os.Chown(file, int(st.Uid), int(st.Gid))
	}
}
