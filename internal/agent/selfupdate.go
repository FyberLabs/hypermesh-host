package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/buildinfo"
	"github.com/FyberLabs/hypermesh-host/internal/update"
)

// The updater sees Busy and nothing else from the agent loop.
var _ update.IdleGate = (*Agent)(nil)

// Busy is true when a restart would drop a lease or an unacked job.
// The job loop is the caller. Heartbeat does not call it.
func (a *Agent) Busy() bool {
	if a.lease != nil && strings.TrimSpace(a.lease.LeaseID) != "" {
		return true
	}
	return a.pendingResult.Load()
}

func (a *Agent) heartbeatOK() uint64 {
	a.healthMu.Lock()
	defer a.healthMu.Unlock()
	return a.hbOK
}

func (a *Agent) startUpdateWatch(ctx context.Context) {
	if strings.TrimSpace(a.cfg.UpdateChannel) == "" {
		return
	}
	if a.updateWatch != nil {
		a.updateWatch(ctx)
		return
	}
	marker, err := filepath.Abs(filepath.Join(a.cfg.StateDir, update.MarkerName))
	if err != nil {
		a.log.Warn("update watch", "err", err)
		return
	}
	baseline := a.heartbeatOK()
	go update.Watch(ctx, update.WatchConfig{
		MarkerPath:  marker,
		Interval:    time.Second,
		HeartbeatOK: a.heartbeatOK,
		Baseline:    baseline,
		Rollback: func() error {
			m, err := update.ReadMarker(marker)
			if err != nil || m == nil {
				if err == nil {
					return nil
				}
				return err
			}
			return update.RollbackFile(m.Binary, m.Backup)
		},
		Exit: os.Exit,
		Log: func(msg string, args ...any) {
			a.log.Warn(msg, args...)
		},
	})
}

func (a *Agent) maybeSelfUpdate(ctx context.Context) {
	if strings.TrimSpace(a.cfg.UpdateChannel) == "" {
		return
	}
	every := a.cfg.UpdateEvery
	if every <= 0 {
		every = time.Hour
	}
	if !a.lastUpdateCheck.IsZero() && time.Since(a.lastUpdateCheck) < every {
		return
	}
	a.lastUpdateCheck = time.Now()
	fn := a.selfUpdate
	if fn == nil {
		fn = a.defaultSelfUpdate
	}
	if err := fn(ctx, a.Busy()); err != nil {
		a.log.Warn("update check failed", "err", err)
	}
}

func (a *Agent) defaultSelfUpdate(ctx context.Context, busy bool) error {
	binary := strings.TrimSpace(a.cfg.UpdateBinary)
	if binary == "" {
		p, err := os.Executable()
		if err != nil {
			return err
		}
		binary = p
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		return err
	}
	marker, err := filepath.Abs(filepath.Join(a.cfg.StateDir, update.MarkerName))
	if err != nil {
		return err
	}
	plan, err := update.Tick(ctx, update.Config{
		Channel:        a.cfg.UpdateChannel,
		CurrentVersion: buildinfo.CurrentVersion(),
		CurrentCommit:  buildinfo.Current(),
		Arch:           runtime.GOARCH,
		BinaryPath:     binary,
		PubKeyPath:     a.cfg.UpdatePubKey,
		MarkerPath:     marker,
		Token:          a.cfg.UpdateToken,
		Busy:           busy,
		RollbackWindow: a.cfg.UpdateRollback,
	}, update.Hooks{})
	if err != nil {
		return err
	}
	a.log.Info("update",
		"action", plan.Action,
		"version", plan.Version,
		"commit", plan.Commit,
		"reason", plan.Reason,
	)
	return nil
}
