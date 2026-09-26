package update

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Marker is the pending-update record. The rollback script reads the
// same keys. Deadline is a unix second. Build and ReleaseID are copied
// into update-bad when this build is rolled back. Build 0 is not recorded.
type Marker struct {
	Binary    string
	Backup    string
	Version   string
	Deadline  time.Time
	Build     int
	ReleaseID string
}

// WatchAction is what to do with a pending marker.
type WatchAction string

const (
	WatchNone     WatchAction = "none"
	WatchWait     WatchAction = "wait"
	WatchConfirm  WatchAction = "confirm"
	WatchRollback WatchAction = "rollback"
)

// Evaluate reports whether a pending update has heartbeated, run out
// of time, or should keep waiting. hb is the process heartbeat success
// count. baseline is that count at process start, before this process
// has heartbeated. A greater hb confirms the new binary.
func Evaluate(m *Marker, now time.Time, hb, baseline uint64) WatchAction {
	if m == nil || m.Deadline.IsZero() {
		return WatchNone
	}
	if hb > baseline {
		return WatchConfirm
	}
	if !now.Before(m.Deadline) {
		return WatchRollback
	}
	return WatchWait
}

// WriteMarker replaces path atomically. Values are single-line paths
// and a unix deadline. The rollback script parses this file.
func WriteMarker(path string, m Marker) error {
	if !safePath(m.Binary) || !safePath(m.Backup) || !safePath(path) {
		return fmt.Errorf("%w: marker path", ErrRejected)
	}
	if m.Deadline.IsZero() {
		return fmt.Errorf("hypermesh update: marker deadline is required")
	}
	ver := sanitizeVersion(m.Version)
	if m.ReleaseID != "" && !releaseIDOK(m.ReleaseID) {
		return fmt.Errorf("%w: marker release", ErrRejected)
	}
	if m.Build < 0 {
		return fmt.Errorf("%w: marker build", ErrRejected)
	}
	body := fmt.Sprintf("binary=%s\nbackup=%s\nversion=%s\ndeadline=%d\nbuild=%d\nrelease_id=%s\n",
		m.Binary, m.Backup, ver, m.Deadline.Unix(), m.Build, m.ReleaseID)
	return writeAtomic(path, body)
}

func syncDir(path string) error {
	d, err := os.Open(filepathDir(path))
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

func filepathDir(path string) string {
	i := strings.LastIndex(path, "/")
	if i <= 0 {
		return "/"
	}
	return path[:i]
}

// ReadMarker parses a pending file. A missing file is a nil marker.
func ReadMarker(path string) (*Marker, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	m := &Marker{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.ContainsAny(v, " \t") {
			return nil, fmt.Errorf("%w: marker line", ErrRejected)
		}
		switch k {
		case "binary":
			m.Binary = v
		case "backup":
			m.Backup = v
		case "version":
			m.Version = v
		case "deadline":
			sec, err := strconv.ParseInt(v, 10, 64)
			if err != nil || sec <= 0 {
				return nil, fmt.Errorf("%w: marker deadline", ErrRejected)
			}
			m.Deadline = time.Unix(sec, 0)
		case "build":
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("%w: marker build", ErrRejected)
			}
			m.Build = n
		case "release_id":
			if v != "" && !releaseIDOK(v) {
				return nil, fmt.Errorf("%w: marker release", ErrRejected)
			}
			m.ReleaseID = v
		default:
			return nil, fmt.Errorf("%w: marker key", ErrRejected)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !safePath(m.Binary) || !safePath(m.Backup) || m.Deadline.IsZero() {
		return nil, fmt.Errorf("%w: marker fields", ErrRejected)
	}
	return m, nil
}

func releaseIDOK(s string) bool {
	if s == "" || strings.Contains(s, "..") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r == '.' || r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// WriteBadBuild records a rolled-back build with an atomic replace in
// the state directory. Build 0 is not recorded: that marker would make
// every later candidate look old.
func WriteBadBuild(path string, build int, releaseID string) error {
	if build <= 0 {
		return nil
	}
	if !safePath(path) {
		return fmt.Errorf("%w: update-bad path", ErrRejected)
	}
	if releaseID != "" && !releaseIDOK(releaseID) {
		return fmt.Errorf("%w: update-bad release", ErrRejected)
	}
	body := fmt.Sprintf("build=%d\nrelease_id=%s\n", build, releaseID)
	return writeAtomic(path, body)
}

// ReadBadBuild returns the build recorded by a rollback. A missing file
// is 0. A corrupt file is an error so the check is retried.
func ReadBadBuild(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()
	build := 0
	seen := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.ContainsAny(v, " \t") {
			return 0, fmt.Errorf("%w: update-bad line", ErrRejected)
		}
		switch k {
		case "build":
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("%w: update-bad build", ErrRejected)
			}
			build = n
			seen = true
		case "release_id":
			if v != "" && !releaseIDOK(v) {
				return 0, fmt.Errorf("%w: update-bad release", ErrRejected)
			}
		default:
			return 0, fmt.Errorf("%w: update-bad key", ErrRejected)
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	if !seen {
		return 0, fmt.Errorf("%w: update-bad build", ErrRejected)
	}
	return build, nil
}

func writeAtomic(path, body string) error {
	tmp := path + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(body)
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	if serr != nil {
		return serr
	}
	if cerr != nil {
		return cerr
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(path)
}

// WatchConfig drives the in-process rollback watch.
type WatchConfig struct {
	MarkerPath  string
	Interval    time.Duration
	HeartbeatOK func() uint64
	Baseline    uint64
	Now         func() time.Time
	Rollback    func() error
	Exit        func(int)
	Log         func(msg string, args ...any)
}

// Watch confirms the marker after a heartbeat or restores the backup
// when the window passes with no heartbeat. It returns when the marker
// is absent, the context ends, or the rollback has run.
func Watch(ctx context.Context, cfg WatchConfig) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.HeartbeatOK == nil {
		cfg.HeartbeatOK = func() uint64 { return cfg.Baseline }
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = time.Second
	}
	act := func() bool {
		m, err := ReadMarker(cfg.MarkerPath)
		if err != nil {
			if cfg.Log != nil {
				cfg.Log("update marker", "err", err)
			}
			return true
		}
		switch Evaluate(m, cfg.Now(), cfg.HeartbeatOK(), cfg.Baseline) {
		case WatchNone:
			return true
		case WatchConfirm:
			_ = os.Remove(cfg.MarkerPath)
			if cfg.Log != nil {
				cfg.Log("update confirmed", "version", m.Version)
			}
			return true
		case WatchRollback:
			if cfg.Rollback == nil {
				return true
			}
			if err := cfg.Rollback(); err != nil {
				if cfg.Log != nil {
					cfg.Log("update rollback failed", "err", err)
				}
				return true
			}
			badPath := filepathDir(cfg.MarkerPath) + "/" + BadName
			if err := WriteBadBuild(badPath, m.Build, m.ReleaseID); err != nil {
				if cfg.Log != nil {
					cfg.Log("update rollback memory", "err", err)
				}
			} else {
				_ = os.Remove(cfg.MarkerPath)
			}
			if cfg.Log != nil {
				cfg.Log("update rolled back", "version", m.Version)
			}
			if cfg.Exit != nil {
				cfg.Exit(1)
			}
			return true
		default:
			return false
		}
	}
	if act() {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if act() {
				return
			}
		}
	}
}
