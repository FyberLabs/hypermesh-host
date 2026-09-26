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
// same keys. Deadline is a unix second.
type Marker struct {
	Binary   string
	Backup   string
	Version  string
	Deadline time.Time
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
	body := fmt.Sprintf("binary=%s\nbackup=%s\nversion=%s\ndeadline=%d\n",
		m.Binary, m.Backup, ver, m.Deadline.Unix())
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
			_ = os.Remove(cfg.MarkerPath)
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
