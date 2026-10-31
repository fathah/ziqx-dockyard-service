package state

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// The root updater holds an exclusive lock across restart and verification.
// A stale, unlocked file never prevents admission. Existing daemons without
// this check remain compatible with the updater's stopped-state job review.
func (s *Store) Available() (bool, error) {
	if err := s.DB.Ping(); err != nil {
		return false, err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, "maintenance.lock"), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false, err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || owner.Uid != uint32(os.Geteuid()) {
		return false, errors.New("unsafe maintenance lock")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return false, nil
		}
		return false, err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return true, nil
}
