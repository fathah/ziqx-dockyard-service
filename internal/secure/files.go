package secure

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// Decode also rejects duplicate object keys: encoding/json alone accepts them.
func Decode(b []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	if err := walk(d); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(dst)
}

func walk(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	if x, ok := t.(json.Delim); ok {
		switch x {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				s, ok := k.(string)
				if !ok || seen[s] {
					return errors.New("duplicate JSON key")
				}
				seen[s] = true
				if e = walk(d); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e := walk(d); e != nil {
					return e
				}
			}
		default:
			return errors.New("unexpected delimiter")
		}
		_, err = d.Token()
	}
	return err
}

// File and every ancestor must be owned by root and not writable by other users.
// Symlinks are deliberately rejected, including paths under a symlinked directory.
func Check(path string, secret bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("absolute clean path required")
	}
	first := true
	for p := path; ; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink rejected")
		}
		u, ok := st.Sys().(*syscall.Stat_t)
		if !ok || u.Uid != 0 || st.Mode().Perm()&0022 != 0 {
			return errors.New("unsafe ownership or permissions")
		}
		if first && secret && (!st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0) {
			return errors.New("secret must be a private regular file")
		}
		if !first && !st.IsDir() {
			return errors.New("unsafe parent")
		}
		first = false
		if p == "/" {
			return nil
		}
	}
}

func Atomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".dockyard-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func PrivateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 {
		return errors.New("private directory required")
	}
	return nil
}
