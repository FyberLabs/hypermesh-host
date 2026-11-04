//go:build linux || darwin

package installer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Darwin's /var and /etc are root-owned aliases. Resolve only those fixed
// prefixes, then refuse symlinks in the installation's own path components.
func macPath(root, path string) string {
	if root != "" {
		return filepath.Join(root, strings.TrimPrefix(path, "/"))
	}
	for _, prefix := range []string{"/var", "/etc"} {
		if strings.HasPrefix(path, prefix+"/") {
			if real, err := filepath.EvalSymlinks(prefix); err == nil {
				return real + strings.TrimPrefix(path, prefix)
			}
		}
	}
	return path
}
func macDirectory(path string, create, private bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return fmt.Errorf("invalid registration directory")
	}
	current := "/"
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) && create {
			err = os.Mkdir(current, 0o700)
			if err == nil {
				info, err = os.Lstat(current)
			}
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("registration requires real directories")
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) {
			return fmt.Errorf("registration path ownership is unsafe")
		}
		sticky := st.Uid == 0 && info.Mode()&os.ModeSticky != 0 && current != path
		if info.Mode().Perm()&0o022 != 0 && !sticky {
			return fmt.Errorf("registration directory is writable by others")
		}
		if current == path && private && (st.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0o700) {
			return fmt.Errorf("registration state requires owner-only mode 0700")
		}
	}
	return nil
}
func macRead(path string, mode os.FileMode, limit int64) ([]byte, error) {
	if err := macDirectory(filepath.Dir(path), false, false); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != mode {
		return nil, fmt.Errorf("registration input is missing or has unsafe permissions: %s", path)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		return nil, fmt.Errorf("registration input ownership or links are unsafe: %s", path)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("cannot open registration input")
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(info, after) {
		return nil, fmt.Errorf("registration input changed")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, fmt.Errorf("registration input exceeds limit")
	}
	return data, nil
}
func macWrite(path string, data []byte, replace bool) error {
	if err := macDirectory(filepath.Dir(path), true, true); err != nil {
		return err
	}
	if old, err := macRead(path, 0o600, 2<<20); err == nil {
		if bytes.Equal(old, data) {
			return nil
		}
		if !replace {
			return fmt.Errorf("existing host identity conflicts; preserve files for recovery")
		}
	} else if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".registration-")
	if err != nil {
		return fmt.Errorf("cannot stage registration state")
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("cannot write registration state")
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("cannot sync registration state")
	}
	if err = f.Close(); err != nil {
		return err
	}
	if replace {
		err = os.Rename(f.Name(), path)
	} else {
		err = os.Link(f.Name(), path)
	}
	if err != nil {
		return fmt.Errorf("cannot publish registration state; preserve files and retry")
	}
	if !replace {
		if err = os.Remove(f.Name()); err != nil {
			return fmt.Errorf("cannot finalize registration state")
		}
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func macDigest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func macDecode(data []byte, dest any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(dest) != nil {
		return fmt.Errorf("invalid registration JSON")
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("invalid trailing registration JSON")
	}
	return nil
}
func macLock(state string) (func(), error) {
	if err := macDirectory(state, true, true); err != nil {
		return nil, err
	}
	path := filepath.Join(state, "apply.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cannot open registration lock")
	}
	if _, err = macRead(path, 0o600, 1024); err != nil {
		f.Close()
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another registration is running")
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
