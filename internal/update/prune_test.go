package update

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPruneKeepsNewestBackups(t *testing.T) {
	t.Setenv(envKeepBackups, "2")
	const dest = "/usr/local/bin/hypermesh-agent"
	base := time.Unix(1_700_000_000, 0)
	bin := filepath.Dir(dest)
	fs := &memFS{
		files: map[string][]byte{
			dest:                            []byte("running"),
			dest + ".pre-a":                 []byte("oldest"),
			dest + ".pre-b":                 []byte("second"),
			dest + ".pre-c":                 []byte("third"),
			filepath.Join(bin, "notes.txt"): []byte("keep"),
			dest + ".pre-has space":         []byte("keep"),
			dest + ".pre-1..2":              []byte("keep"),
			dest + ".pre-dir":               []byte("dir"),
			dest + ".pre-link":              []byte("link"),
			dest + ".rollback":              []byte("rollback"),
		},
		mtimes: map[string]time.Time{
			dest:            base.Add(10 * time.Hour),
			dest + ".pre-a": base.Add(1 * time.Hour),
			dest + ".pre-b": base.Add(3 * time.Hour),
			dest + ".pre-c": base.Add(2 * time.Hour),
		},
		modes: map[string]os.FileMode{
			dest + ".pre-dir":  os.ModeDir | 0o755,
			dest + ".pre-link": os.ModeSymlink | 0o777,
		},
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	res, err := finishInstallLocked(fs, dest, []byte("new-binary"), "cur", log)
	if err != nil {
		t.Fatal(err)
	}
	if res.Backup != dest+".pre-cur" || string(fs.files[dest]) != "new-binary" {
		t.Fatalf("install backup %s dest %q", res.Backup, fs.files[dest])
	}
	if string(fs.files[res.Backup]) != "running" {
		t.Fatal("newest backup missing")
	}
	if _, ok := fs.files[dest+".pre-b"]; !ok {
		t.Fatal("second newest backup removed")
	}
	for _, name := range []string{".pre-a", ".pre-c"} {
		if _, ok := fs.files[dest+name]; ok {
			t.Fatalf("older backup kept: %s", name)
		}
	}
	for _, path := range []string{
		filepath.Join(bin, "notes.txt"),
		dest + ".pre-has space",
		dest + ".pre-1..2",
		dest + ".pre-dir",
		dest + ".pre-link",
		dest + ".rollback",
	} {
		if _, ok := fs.files[path]; !ok {
			t.Fatalf("removed %s", path)
		}
	}
	text := logs.String()
	if !strings.Contains(text, "level=INFO") || !strings.Contains(text, "removed old update backup") || !strings.Contains(text, dest+".pre-a") {
		t.Fatalf("log:\n%s", text)
	}
}

func TestPruneKeepsInFlightBackup(t *testing.T) {
	t.Setenv(envKeepBackups, "2")
	const dest = "/usr/local/bin/hypermesh-agent"
	base := time.Unix(1_700_000_000, 0)
	markerBackup := dest + ".pre-marker"
	fs := &memFS{
		files: map[string][]byte{
			dest:            []byte("running"),
			dest + ".pre-a": []byte("newest"),
			dest + ".pre-b": []byte("second"),
			dest + ".pre-c": []byte("third"),
			markerBackup:    []byte("marker"),
		},
		mtimes: map[string]time.Time{
			dest:            base,
			dest + ".pre-a": base.Add(3 * time.Hour),
			dest + ".pre-b": base.Add(2 * time.Hour),
			dest + ".pre-c": base.Add(1 * time.Hour),
			markerBackup:    base.Add(-time.Hour),
		},
	}
	res, err := Install(fs, dest, []byte("new-binary"), "old", markerBackup)
	if err != nil {
		t.Fatal(err)
	}
	inFlight := dest + ".pre-old"
	if res.Backup != inFlight {
		t.Fatalf("backup %s", res.Backup)
	}
	for _, path := range []string{inFlight, dest + ".pre-a", dest + ".pre-b", markerBackup} {
		if _, ok := fs.files[path]; !ok {
			t.Fatalf("required backup removed: %s", path)
		}
	}
	if _, ok := fs.files[dest+".pre-c"]; ok {
		t.Fatal("third newest backup was kept")
	}
	if string(fs.files[inFlight]) != "running" || string(fs.files[markerBackup]) != "marker" {
		t.Fatal("pinned bytes changed")
	}
}

func TestPruneErrorDoesNotFailInstall(t *testing.T) {
	const dest = "/usr/local/bin/hypermesh-agent"
	base := time.Unix(1_700_000_000, 0)
	newFS := func() *memFS {
		return &memFS{
			files: map[string][]byte{
				dest:            []byte("running"),
				dest + ".pre-a": []byte("a"),
				dest + ".pre-b": []byte("b"),
				dest + ".pre-c": []byte("c"),
			},
			mtimes: map[string]time.Time{
				dest:            base.Add(4 * time.Hour),
				dest + ".pre-a": base.Add(1 * time.Hour),
				dest + ".pre-b": base.Add(3 * time.Hour),
				dest + ".pre-c": base.Add(2 * time.Hour),
			},
		}
	}
	t.Run("list error", func(t *testing.T) {
		t.Setenv(envKeepBackups, "2")
		fs := newFS()
		fs.readDirErr = errors.New("disk")
		var logs bytes.Buffer
		log := slog.New(slog.NewTextHandler(&logs, nil))
		res, err := finishInstallLocked(fs, dest, []byte("new-binary"), "cur", log)
		if err != nil {
			t.Fatal(err)
		}
		if string(fs.files[dest]) != "new-binary" || string(fs.files[res.Backup]) != "running" {
			t.Fatalf("install dest %q backup %q", fs.files[dest], fs.files[res.Backup])
		}
		for _, name := range []string{".pre-a", ".pre-b", ".pre-c"} {
			if _, ok := fs.files[dest+name]; !ok {
				t.Fatalf("list error still deleted %s", name)
			}
		}
		text := logs.String()
		if !strings.Contains(text, "level=WARN") || !strings.Contains(text, "update prune list failed") {
			t.Fatalf("log:\n%s", text)
		}
	})
	t.Run("remove error", func(t *testing.T) {
		t.Setenv(envKeepBackups, "2")
		fs := newFS()
		fs.removeErr = errors.New("busy")
		res, err := Install(fs, dest, []byte("new-binary"), "cur")
		if err != nil {
			t.Fatal(err)
		}
		if string(fs.files[dest]) != "new-binary" || fs.files[res.Backup] == nil {
			t.Fatalf("install dest %q backup %q", fs.files[dest], fs.files[res.Backup])
		}
		for _, name := range []string{".pre-a", ".pre-b", ".pre-c"} {
			if _, ok := fs.files[dest+name]; !ok {
				t.Fatalf("remove error still deleted %s", name)
			}
		}
	})
	t.Run("one remove fails", func(t *testing.T) {
		t.Setenv(envKeepBackups, "2")
		fs := newFS()
		fs.removeFail = map[string]error{dest + ".pre-a": errors.New("busy")}
		var logs bytes.Buffer
		log := slog.New(slog.NewTextHandler(&logs, nil))
		if _, err := finishInstallLocked(fs, dest, []byte("new-binary"), "cur", log); err != nil {
			t.Fatal(err)
		}
		if _, ok := fs.files[dest+".pre-a"]; !ok {
			t.Fatal("failed remove deleted the file")
		}
		if _, ok := fs.files[dest+".pre-c"]; ok {
			t.Fatal("later backup was not removed")
		}
		if _, ok := fs.files[dest+".pre-b"]; !ok || fs.files[dest+".pre-cur"] == nil {
			t.Fatal("newer backups were not kept")
		}
		if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "update prune failed") {
			t.Fatalf("log:\n%s", logs.String())
		}
	})
}

func TestPruneOrdersByMtimeThenName(t *testing.T) {
	const dest = "/usr/local/bin/hypermesh-agent"
	base := time.Unix(1_700_000_000, 0)
	t.Run("newer mtime wins", func(t *testing.T) {
		t.Setenv(envKeepBackups, "1")
		fs := &memFS{
			files: map[string][]byte{
				dest:            []byte("running"),
				dest + ".pre-a": []byte("old"),
				dest + ".pre-z": []byte("new"),
			},
			mtimes: map[string]time.Time{
				dest:            base,
				dest + ".pre-a": base.Add(1 * time.Hour),
				dest + ".pre-z": base.Add(2 * time.Hour),
			},
		}
		res, err := Install(fs, dest, []byte("next"), "old")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := fs.files[res.Backup]; !ok {
			t.Fatal("in-flight backup removed")
		}
		if _, ok := fs.files[dest+".pre-z"]; !ok {
			t.Fatal("newer backup removed")
		}
		if _, ok := fs.files[dest+".pre-a"]; ok {
			t.Fatal("older backup kept ahead of a newer name")
		}
	})
	t.Run("equal mtime uses name", func(t *testing.T) {
		t.Setenv(envKeepBackups, "1")
		fs := &memFS{
			files: map[string][]byte{
				dest:            []byte("running"),
				dest + ".pre-a": []byte("a"),
				dest + ".pre-b": []byte("b"),
			},
			mtimes: map[string]time.Time{
				dest:            base,
				dest + ".pre-a": base,
				dest + ".pre-b": base,
			},
		}
		res, err := Install(fs, dest, []byte("next"), "m")
		if err != nil {
			t.Fatal(err)
		}
		if res.Backup != dest+".pre-m" {
			t.Fatalf("backup %s", res.Backup)
		}
		if _, ok := fs.files[dest+".pre-a"]; !ok {
			t.Fatal("name tiebreak dropped the earlier name")
		}
		if _, ok := fs.files[res.Backup]; !ok {
			t.Fatal("in-flight backup removed")
		}
		if _, ok := fs.files[dest+".pre-b"]; ok {
			t.Fatal("later equal-mtime name was kept")
		}
	})
}

func TestBackupKeepCount(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	t.Setenv(envKeepBackups, "")
	if backupKeepCount(log) != defaultKeepBackups {
		t.Fatalf("default %d", backupKeepCount(log))
	}
	t.Setenv(envKeepBackups, "4")
	if backupKeepCount(log) != 4 {
		t.Fatal(backupKeepCount(log))
	}
	t.Setenv(envKeepBackups, "0")
	if backupKeepCount(log) != 1 {
		t.Fatal(backupKeepCount(log))
	}
	t.Setenv(envKeepBackups, "nope")
	if backupKeepCount(log) != defaultKeepBackups {
		t.Fatal(backupKeepCount(log))
	}
	text := logs.String()
	if !strings.Contains(text, "level=WARN") || !strings.Contains(text, "update backup keep count") {
		t.Fatalf("log:\n%s", text)
	}
}

func TestInstallKeepsMarkerBackupAndStaleTemp(t *testing.T) {
	t.Setenv(envKeepBackups, "2")
	dir := t.TempDir()
	state := t.TempDir()
	dest := filepath.Join(dir, "hypermesh-agent")
	base := time.Unix(1_700_000_000, 0)
	writeAt := func(path, body string, mt time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	writeAt(dest, "running", base.Add(5*time.Hour))
	oldest := dest + ".pre-oldest"
	writeAt(oldest, "oldest", base)
	writeAt(dest+".pre-mid", "mid", base.Add(time.Hour))
	writeAt(dest+".pre-new", "new", base.Add(4*time.Hour))
	marker := filepath.Join(state, MarkerName)
	if err := WriteMarker(marker, Marker{
		Binary: dest, Backup: oldest, Version: "1.0.0", Deadline: base.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	stale := marker + ".new"
	if err := os.WriteFile(stale, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	backup, err := installFileKeeping(marker, log)(dest, []byte("next"), "cur")
	if err != nil {
		t.Fatal(err)
	}
	if backup != dest+".pre-cur" {
		t.Fatalf("backup %s", backup)
	}
	for _, path := range []string{backup, dest + ".pre-new", oldest} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	if _, err := os.Stat(dest + ".pre-mid"); !os.IsNotExist(err) {
		t.Fatal("mid backup still present")
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale marker temp still present")
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "next" {
		t.Fatalf("dest %q %v", got, err)
	}
}

func TestUnreadableMarkerSkipsPrune(t *testing.T) {
	t.Setenv(envKeepBackups, "1")
	dir := t.TempDir()
	dest := filepath.Join(dir, "hypermesh-agent")
	if err := os.WriteFile(dest, []byte("running"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".pre-a", ".pre-b", ".pre-c"} {
		if err := os.WriteFile(dest+name, []byte(name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(t.TempDir(), MarkerName)
	if err := os.WriteFile(marker, []byte("not a marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := installFileKeeping(marker, log)(dest, []byte("next"), "cur"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".pre-a", ".pre-b", ".pre-c", ".pre-cur"} {
		if _, err := os.Stat(dest + name); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if !strings.Contains(logs.String(), "update prune skipped") {
		t.Fatalf("log:\n%s", logs.String())
	}
}

func TestWatchConfirmPrunesBackups(t *testing.T) {
	t.Setenv(envKeepBackups, "2")
	dir := t.TempDir()
	state := t.TempDir()
	dest := filepath.Join(dir, "hypermesh-agent")
	base := time.Unix(1_700_000_000, 0)
	writeAt := func(path, body string, mt time.Time, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	writeAt(dest, "running", base.Add(5*time.Hour), 0o755)
	backup := dest + ".pre-1.0.0"
	writeAt(backup, "previous", base, 0o755)
	writeAt(dest+".pre-keep", "keep", base.Add(4*time.Hour), 0o755)
	writeAt(dest+".pre-also", "also", base.Add(3*time.Hour), 0o755)
	writeAt(dest+".pre-drop", "drop", base.Add(time.Hour), 0o755)
	writeAt(dest+".new", "stale-new", base, 0o644)
	writeAt(dest+".rollback", "rollback", base, 0o644)
	writeAt(filepath.Join(dir, "notes.txt"), "notes", base, 0o644)
	if err := os.Symlink(dest, dest+".pre-link"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dest+".pre-dir", 0o755); err != nil {
		t.Fatal(err)
	}
	writeAt(filepath.Join(state, BadName), "build=3\nrelease_id=1.0.0\n", base, 0o600)
	writeAt(filepath.Join(state, BadName+".new"), "partial-bad", base, 0o600)
	marker := filepath.Join(state, MarkerName)
	if err := WriteMarker(marker, Marker{
		Binary: dest, Backup: backup, Version: "1.2.3", Deadline: base.Add(time.Minute),
		Build: 4, ReleaseID: "1.2.3",
	}); err != nil {
		t.Fatal(err)
	}
	// Written after the marker so the atomic replace does not consume it.
	pendingNew := filepath.Join(state, MarkerName+".new")
	writeAt(pendingNew, "partial-marker", base, 0o600)
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	Watch(context.Background(), WatchConfig{
		MarkerPath:   marker,
		HeartbeatOK:  func() uint64 { return 1 },
		Baseline:     0,
		Now:          func() time.Time { return base },
		Logger:       log,
		RunningBuild: 10,
	})
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("marker still present")
	}
	for _, path := range []string{dest, backup, dest + ".pre-keep", dest + ".pre-also", dest + ".rollback", filepath.Join(dir, "notes.txt"), pendingNew} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
	}
	for _, path := range []string{dest + ".pre-drop", dest + ".new", filepath.Join(state, BadName), filepath.Join(state, BadName+".new")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("still present %s", path)
		}
	}
	info, err := os.Lstat(dest + ".pre-link")
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink %v %v", info, err)
	}
	info, err = os.Stat(dest + ".pre-dir")
	if err != nil || !info.IsDir() {
		t.Fatalf("directory %v %v", info, err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "running" {
		t.Fatalf("binary %q %v", got, err)
	}
	text := logs.String()
	if !strings.Contains(text, "removed old update backup") || !strings.Contains(text, "removed old update-bad") {
		t.Fatalf("log:\n%s", text)
	}

	t.Run("newer bad build is kept", func(t *testing.T) {
		st := t.TempDir()
		path := filepath.Join(st, BadName)
		if err := os.WriteFile(path, []byte("build=12\nrelease_id=9\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		pruneAfterConfirm(OSFS, "", "", st, 10, log)
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("not-a-marker\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		pruneAfterConfirm(OSFS, "", "", st, 10, log)
		if _, err := os.Stat(path); err != nil {
			t.Fatal("corrupt update-bad was removed")
		}
		pruneAfterConfirm(OSFS, "", "", st, 0, log)
		if _, err := os.Stat(path); err != nil {
			t.Fatal("build 0 removed update-bad")
		}
	})
}
