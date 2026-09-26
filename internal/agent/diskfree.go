package agent

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// diskFreeBytes is statfs Bavail*Bsize for the filesystem that holds the
// configured GGUF path. A missing file walks up to the nearest existing
// directory. An empty path or a failed statfs omits the field.
func (a *Agent) diskFreeBytes() (int64, bool) {
	path, ok := nearestExisting(a.cfg.GGUFHostPath())
	if !ok {
		return 0, false
	}
	fn := a.statFree
	if fn == nil {
		fn = statfsFree
	}
	n, err := fn(path)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// nearestExisting returns path when it exists, otherwise the closest
// existing parent. An empty path does not fall back to the working directory.
func nearestExisting(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}
	path = filepath.Clean(path)
	for {
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", false
		}
		path = parent
	}
}

// statfsFree is free bytes available to non-root users.
func statfsFree(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	bsize := int64(st.Bsize)
	if bsize <= 0 {
		return 0, errors.New("hypermesh: statfs block size")
	}
	if st.Bavail > uint64(math.MaxInt64)/uint64(bsize) {
		return 0, errors.New("hypermesh: statfs free bytes overflow")
	}
	return int64(st.Bavail) * bsize, nil
}
