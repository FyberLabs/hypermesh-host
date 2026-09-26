package agent

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/FyberLabs/hypermesh-host/internal/pull"
)

// diskFreeBytes is statfs Bavail*Bsize for the filesystem that holds the
// configured GGUF path. A missing file walks up to the nearest existing
// directory. An empty path or a failed statfs omits the field.
// The default stat is pull.FreeBytes, the same helper the download
// preflight uses.
func (a *Agent) diskFreeBytes() (int64, bool) {
	free, _, ok := a.diskUsage()
	return free, ok
}

// diskUsage is one reading of free and total bytes on the GGUF filesystem.
// ok is false when the path cannot be resolved or statfs fails, and then
// neither value is reported. A test stub that supplies free still reads
// size from pull.CapacityBytes unless statSize is set, and a failure of
// either side drops both.
func (a *Agent) diskUsage() (free, size int64, ok bool) {
	path, exists := nearestExisting(a.cfg.GGUFHostPath())
	if !exists {
		return 0, 0, false
	}
	if a.statFree != nil {
		n, err := a.statFree(path)
		if err != nil || n < 0 {
			return 0, 0, false
		}
		var s int64
		if a.statSize != nil {
			s, err = a.statSize(path)
		} else {
			s, err = pull.CapacityBytes(path)
		}
		if err != nil || s < 0 {
			return 0, 0, false
		}
		return n, s, true
	}
	f, s, err := pull.StatfsBytes(path)
	if err != nil || f < 0 || s < 0 {
		return 0, 0, false
	}
	return f, s, true
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
