package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ensureModelDir creates the parent of the weights file.
// The models directory is mode 0755. When this process is root, it is
// owned by root:root. Ancestors that do not exist yet (usually
// /var/lib/hypermesh, which also holds device.json) are created mode 0700
// so a fresh box does not leave the state directory world-accessible.
// An existing state directory is left alone.
func ensureModelDir(ggufPath string) error {
	if err := downloadDestOK(ggufPath); err != nil {
		if strings.TrimSpace(ggufPath) == "" {
			return nil
		}
		return err
	}
	dir := filepath.Dir(filepath.Clean(strings.TrimSpace(ggufPath)))
	parent := filepath.Dir(dir)
	if parent != dir {
		if _, err := os.Stat(parent); os.IsNotExist(err) {
			if err := os.MkdirAll(parent, 0o700); err != nil {
				return err
			}
			_ = os.Chmod(parent, 0o700)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if filepath.Base(dir) != "models" {
		return nil
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(dir, 0, 0); err != nil {
			return err
		}
	}
	return nil
}

// downloadDestOK rejects an empty path, a relative path, and a file
// whose parent is the root directory. systemd's cwd is /, so a relative
// or empty dest becomes /lab-pull-*.gguf.
func downloadDestOK(dest string) error {
	dest = strings.TrimSpace(dest)
	if dest == "" || !filepath.IsAbs(dest) {
		return fmt.Errorf("hypermesh: no download path")
	}
	dir := filepath.Dir(filepath.Clean(dest))
	if dir == "/" || dir == "." || dir == "" {
		return fmt.Errorf("hypermesh: refusing download onto the root filesystem")
	}
	return nil
}
