package secure

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// Caddy is a trusted route writer. Its socket/tree may belong to root or the
// caddy account, but must not be writable by any other account or group.
func CaddySocket(path string) error {
	u, e := user.Lookup("caddy")
	if e != nil {
		return errors.New("Caddy service account required")
	}
	uid, e := strconv.ParseUint(u.Uid, 10, 32)
	if e != nil {
		return e
	}
	first := true
	for p := path; ; p = filepath.Dir(p) {
		st, e := os.Lstat(p)
		if e != nil {
			return e
		}
		info, ok := st.Sys().(*syscall.Stat_t)
		if !ok || st.Mode()&os.ModeSymlink != 0 || info.Uid != 0 && info.Uid != uint32(uid) || st.Mode().Perm()&0022 != 0 {
			return errors.New("unsafe Caddy socket path")
		}
		if first {
			if st.Mode()&os.ModeSocket == 0 || st.Mode().Perm()&0077 != 0 {
				return errors.New("Caddy admin requires a private Unix socket")
			}
		} else if !st.IsDir() {
			return errors.New("unsafe Caddy socket parent")
		}
		first = false
		if p == "/" {
			return nil
		}
	}
}
