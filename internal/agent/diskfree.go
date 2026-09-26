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
	path, ok := nearestExisting(a.cfg.GGUFHostPath())
	if !ok {
		return 0, false
	}
	fn := a.statFree
	if fn == nil {
		fn = pull.FreeBytes
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
