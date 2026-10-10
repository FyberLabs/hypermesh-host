package update

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DirEntry is one name from ReadDir. Name is a base name.
// ModTime is the file modification time. Mode is the lstat mode,
// so a symlink is not reported as a regular file.
type DirEntry struct {
	Name    string
	ModTime time.Time
	Mode    os.FileMode
}

// FS is the filesystem the installer uses. Tests fake it.
// WriteNew creates path, writes data, and fsyncs the file.
// ReadDir lists one directory so retention can order backups by mtime.
type FS interface {
	WriteNew(path string, data []byte, mode os.FileMode) error
	SyncDir(dir string) error
	Rename(oldpath, newpath string) error
	Exists(path string) bool
	Remove(path string) error
	ReadDir(dir string) ([]DirEntry, error)
}

// installMu stops retention from removing a file the installer is writing.
var installMu sync.Mutex

// InstallResult is the backup path, empty when dest did not exist.
type InstallResult struct {
	Backup string
	Ops    []string
}

// Install writes payload beside dest, fsyncs, renames the current binary
// to dest.pre-<previous>, then renames the new file onto dest.
// After that succeeds, older dest.pre-* backups are pruned.
// keep paths are retained even when they are not among the newest.
// Pruning never changes the result: a prune error is logged and ignored.
func Install(fs FS, dest string, payload []byte, previous string, keep ...string) (InstallResult, error) {
	installMu.Lock()
	defer installMu.Unlock()
	return finishInstallLocked(fs, dest, payload, previous, nil, keep...)
}

func finishInstallLocked(fs FS, dest string, payload []byte, previous string, log *slog.Logger, keep ...string) (InstallResult, error) {
	res, err := performInstallLocked(fs, dest, payload, previous)
	if err != nil {
		return res, err
	}
	pins := make([]string, 0, 1+len(keep))
	if res.Backup != "" {
		pins = append(pins, res.Backup)
	}
	for _, k := range keep {
		if strings.TrimSpace(k) != "" {
			pins = append(pins, k)
		}
	}
	pruneAfterInstall(fs, dest, pins, log)
	return res, nil
}

func performInstallLocked(fs FS, dest string, payload []byte, previous string) (InstallResult, error) {
	if fs == nil {
		return InstallResult{}, fmt.Errorf("hypermesh update: filesystem is required")
	}
	dest = filepath.Clean(dest)
	if dest == "." || dest == string(filepath.Separator) || strings.HasSuffix(dest, string(filepath.Separator)) {
		return InstallResult{}, fmt.Errorf("hypermesh update: binary path is required")
	}
	dir := filepath.Dir(dest)
	tmp := dest + ".new"
	var res InstallResult
	note := func(op string) { res.Ops = append(res.Ops, op) }

	if err := fs.WriteNew(tmp, payload, 0o755); err != nil {
		return res, err
	}
	note("write " + tmp)
	note("syncfile " + tmp)
	if err := fs.SyncDir(dir); err != nil {
		return res, err
	}
	note("syncdir " + dir)
	if !fs.Exists(dest) {
		_ = fs.Remove(tmp)
		return res, fmt.Errorf("%w: current binary missing", ErrRejected)
	}
	backup := filepath.Join(dir, filepath.Base(dest)+".pre-"+sanitizeVersion(previous))
	if fs.Exists(backup) {
		if err := fs.Remove(backup); err != nil {
			return res, err
		}
		note("remove " + backup)
	}
	if err := fs.Rename(dest, backup); err != nil {
		return res, err
	}
	note("rename " + dest + " " + backup)
	res.Backup = backup
	if err := fs.Rename(tmp, dest); err != nil {
		return res, err
	}
	note("rename " + tmp + " " + dest)
	if err := fs.SyncDir(dir); err != nil {
		return res, err
	}
	note("syncdir " + dir)
	return res, nil
}

// Rollback copies backup onto dest and fsyncs. The backup file stays
// so a second attempt can still restore it.
func Rollback(fs FS, dest, backup string) error {
	if fs == nil {
		return fmt.Errorf("hypermesh update: filesystem is required")
	}
	if !safePath(dest) || !safePath(backup) {
		return fmt.Errorf("%w: backup path", ErrRejected)
	}
	if !fs.Exists(backup) {
		return fmt.Errorf("%w: backup missing", ErrRejected)
	}
	// Read through the production helper when fs is the OS. Fakes
	// implement the copy by Rename of a duplicate. See rollbackCopy.
	return rollbackCopy(fs, dest, backup)
}

func sanitizeVersion(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return "unknown"
	}
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r == '.' || r == '_' || r == '+' || r == '-':
		default:
			return "unknown"
		}
	}
	if strings.Contains(v, "..") {
		return "unknown"
	}
	return v
}

func safePath(p string) bool {
	if p == "" || strings.ContainsAny(p, " \t\r\n") || strings.Contains(p, "..") {
		return false
	}
	return strings.HasPrefix(p, "/")
}

// OSFS is the process filesystem.
var OSFS FS = osFS{}

type osFS struct{}

func (osFS) WriteNew(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	if serr != nil {
		return serr
	}
	return cerr
}

func (osFS) SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	serr := d.Sync()
	cerr := d.Close()
	if serr != nil {
		return serr
	}
	return cerr
}

func (osFS) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

func (osFS) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (osFS) Remove(path string) error { return os.Remove(path) }

func (osFS) ReadDir(dir string) ([]DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, DirEntry{
			Name:    e.Name(),
			ModTime: info.ModTime(),
			Mode:    info.Mode(),
		})
	}
	return out, nil
}

func (osFS) read(path string) ([]byte, error) { return os.ReadFile(path) }

// reader is implemented by osFS and the test fake.
type reader interface {
	read(path string) ([]byte, error)
}

func rollbackCopy(fs FS, dest, backup string) error {
	r, ok := fs.(reader)
	if !ok {
		return fmt.Errorf("hypermesh update: filesystem cannot read the backup")
	}
	body, err := r.read(backup)
	if err != nil {
		return err
	}
	tmp := dest + ".rollback"
	if err := fs.WriteNew(tmp, body, 0o755); err != nil {
		return err
	}
	dir := filepath.Dir(dest)
	if err := fs.SyncDir(dir); err != nil {
		return err
	}
	if err := fs.Rename(tmp, dest); err != nil {
		return err
	}
	return fs.SyncDir(dir)
}

// InstallFile installs payload over dest on the real filesystem.
func InstallFile(dest string, payload []byte, previous string) (string, error) {
	res, err := Install(OSFS, dest, payload, previous)
	return res.Backup, err
}

// RollbackFile restores backup onto dest on the real filesystem.
func RollbackFile(dest, backup string) error {
	return Rollback(OSFS, dest, backup)
}
