package update

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// defaultKeepBackups is how many dest.pre-* files to retain.
	// A backup named by the in-flight update is kept as well when it
	// is not already one of those files.
	defaultKeepBackups = 2
	// envKeepBackups overrides defaultKeepBackups. Values below 1 are
	// raised to 1. An empty or non-numeric value leaves the default.
	envKeepBackups = "HYPERMESH_UPDATE_KEEP_BACKUPS"
)

func pruneLog(log *slog.Logger) *slog.Logger {
	if log == nil {
		return slog.Default()
	}
	return log
}

func backupKeepCount(log *slog.Logger) int {
	raw := strings.TrimSpace(os.Getenv(envKeepBackups))
	if raw == "" {
		return defaultKeepBackups
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		pruneLog(log).Warn("update backup keep count ignored", "name", envKeepBackups, "value", raw)
		return defaultKeepBackups
	}
	if v < 1 {
		pruneLog(log).Warn("update backup keep count raised to minimum", "name", envKeepBackups, "value", v, "keep", 1)
		return 1
	}
	return v
}

// installFileKeeping installs on the OS filesystem and then prunes.
// The backup recorded in markerPath is retained. When that marker
// cannot be read, pruning is skipped so an unknown rollback file is
// not deleted. A leftover markerPath.new from an earlier atomic write
// is removed only after the install rename has succeeded.
func installFileKeeping(markerPath string, log *slog.Logger) func(string, []byte, string) (string, error) {
	return func(dest string, payload []byte, previous string) (string, error) {
		installMu.Lock()
		defer installMu.Unlock()
		pin, block := markerBackup(markerPath)
		if block {
			pruneLog(log).Warn("update prune skipped", "reason", "pending marker unreadable", "path", markerPath)
			res, err := performInstallLocked(OSFS, dest, payload, previous)
			return res.Backup, err
		}
		res, err := finishInstallLocked(OSFS, dest, payload, previous, log, pin)
		if err != nil {
			return res.Backup, err
		}
		if strings.TrimSpace(markerPath) != "" {
			pruneExactPath(OSFS, markerPath+".new", log)
		}
		return res.Backup, nil
	}
}

// markerBackup returns the backup path in a pending marker.
// block is true when a marker is present but its backup path is not known.
func markerBackup(path string) (backup string, block bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}
	m, err := ReadMarker(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false
		}
		return "", true
	}
	if m == nil {
		return "", false
	}
	if strings.TrimSpace(m.Backup) == "" {
		return "", true
	}
	return m.Backup, false
}

// pruneAfterInstall deletes old backups and a leftover dest.new.
// pins are never removed. Errors are logged and ignored.
func pruneAfterInstall(fs FS, dest string, pins []string, log *slog.Logger) {
	if fs == nil {
		return
	}
	dest = filepath.Clean(dest)
	dir := filepath.Dir(dest)
	if !pruneDirOK(dir) {
		return
	}
	entries, err := fs.ReadDir(dir)
	if err != nil {
		pruneLog(log).Warn("update prune list failed", "dir", dir, "err", err)
		return
	}
	pin := pinSet(pins)
	pin[dest] = struct{}{}
	pruneBackups(fs, dir, filepath.Base(dest), entries, pin, log)
	pruneNamed(fs, dir, entries, []string{filepath.Base(dest) + ".new"}, pin, log)
}

// pruneAfterConfirm runs after a heartbeat confirms the pending update.
// pin is the marker backup and is always kept. runningBuild is this
// process's build. A non-positive value does not delete update-bad.
func pruneAfterConfirm(fs FS, dest, pin, stateDir string, runningBuild int, log *slog.Logger) {
	if fs == nil {
		return
	}
	installMu.Lock()
	defer installMu.Unlock()
	var pins []string
	if strings.TrimSpace(pin) != "" {
		pins = append(pins, pin)
	}
	if strings.TrimSpace(dest) != "" {
		pruneAfterInstall(fs, dest, pins, log)
	}
	stateDir = filepath.Clean(stateDir)
	if !pruneDirOK(stateDir) {
		return
	}
	entries, err := fs.ReadDir(stateDir)
	if err != nil {
		pruneLog(log).Warn("update prune list failed", "dir", stateDir, "err", err)
		return
	}
	pruneNamed(fs, stateDir, entries, []string{BadName + ".new"}, nil, log)
	pruneStaleBad(fs, stateDir, entries, runningBuild, log)
}

func pruneDirOK(dir string) bool {
	dir = filepath.Clean(dir)
	return dir != "" && dir != "." && dir != string(filepath.Separator)
}

func pinSet(pins []string) map[string]struct{} {
	set := make(map[string]struct{}, len(pins))
	for _, p := range pins {
		if strings.TrimSpace(p) == "" {
			continue
		}
		set[filepath.Clean(p)] = struct{}{}
	}
	return set
}

type rankedBackup struct {
	path string
	name string
	mod  time.Time
}

func pruneBackups(fs FS, dir, base string, entries []DirEntry, pin map[string]struct{}, log *slog.Logger) {
	prefix := base + ".pre-"
	var ranks []rankedBackup
	for _, e := range entries {
		if !keepableFile(e) {
			continue
		}
		if !strings.HasPrefix(e.Name, prefix) {
			continue
		}
		rest := strings.TrimPrefix(e.Name, prefix)
		if rest == "" || sanitizeVersion(rest) != rest {
			continue
		}
		ranks = append(ranks, rankedBackup{
			path: filepath.Join(dir, e.Name),
			name: e.Name,
			mod:  e.ModTime,
		})
	}
	sort.Slice(ranks, func(i, j int) bool {
		if !ranks[i].mod.Equal(ranks[j].mod) {
			return ranks[i].mod.After(ranks[j].mod)
		}
		return ranks[i].name < ranks[j].name
	})
	keep := backupKeepCount(log)
	for i, b := range ranks {
		if i < keep {
			continue
		}
		if _, ok := pin[filepath.Clean(b.path)]; ok {
			continue
		}
		removePruned(fs, b.path, "removed old update backup", log)
	}
}

func keepableFile(e DirEntry) bool {
	return e.Mode.IsRegular() && safeEntry(e.Name)
}

func safeEntry(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return false
	}
	return true
}

func pruneNamed(fs FS, dir string, entries []DirEntry, names []string, pin map[string]struct{}, log *slog.Logger) {
	want := make(map[string]struct{}, len(names))
	for _, n := range names {
		if safeEntry(n) {
			want[n] = struct{}{}
		}
	}
	if len(want) == 0 {
		return
	}
	for _, e := range entries {
		if _, ok := want[e.Name]; !ok || !keepableFile(e) {
			continue
		}
		path := filepath.Join(dir, e.Name)
		if _, ok := pin[filepath.Clean(path)]; ok {
			continue
		}
		removePruned(fs, path, "removed stale update file", log)
	}
}

func pruneExactPath(fs FS, path string, log *slog.Logger) {
	path = filepath.Clean(path)
	dir := filepath.Dir(path)
	if !pruneDirOK(dir) || !safeEntry(filepath.Base(path)) {
		return
	}
	entries, err := fs.ReadDir(dir)
	if err != nil {
		pruneLog(log).Warn("update prune list failed", "dir", dir, "err", err)
		return
	}
	pruneNamed(fs, dir, entries, []string{filepath.Base(path)}, nil, log)
}

func pruneStaleBad(fs FS, stateDir string, entries []DirEntry, runningBuild int, log *slog.Logger) {
	if runningBuild <= 0 {
		return
	}
	var found *DirEntry
	for i := range entries {
		if entries[i].Name == BadName {
			found = &entries[i]
			break
		}
	}
	if found == nil || !keepableFile(*found) {
		return
	}
	path := filepath.Join(stateDir, BadName)
	r, ok := fs.(reader)
	if !ok {
		pruneLog(log).Warn("update prune skipped", "reason", "filesystem cannot read update-bad", "path", path)
		return
	}
	body, err := r.read(path)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		pruneLog(log).Warn("update prune failed", "path", path, "err", err)
		return
	}
	build, err := parseBadBuild(bytes.NewReader(body))
	if err != nil {
		pruneLog(log).Warn("update prune failed", "path", path, "err", err)
		return
	}
	if build <= 0 || build >= runningBuild {
		return
	}
	if err := fs.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return
		}
		pruneLog(log).Warn("update prune failed", "path", path, "err", err)
		return
	}
	pruneLog(log).Info("removed old update-bad", "path", path, "build", build, "running", runningBuild)
}

func removePruned(fs FS, path, msg string, log *slog.Logger) {
	if err := fs.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return
		}
		pruneLog(log).Warn("update prune failed", "path", path, "err", err)
		return
	}
	pruneLog(log).Info(msg, "path", path)
}
