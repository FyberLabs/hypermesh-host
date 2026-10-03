package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/buildinfo"
	"github.com/FyberLabs/hypermesh-host/internal/update"
)

// The updater sees Busy and nothing else from the agent loop.
var _ update.IdleGate = (*Agent)(nil)

// Busy is true when a restart would drop a lease or an unacked job.
// The job loop calls it at the start of a check and again before
// install and restart. Heartbeat does not call it.
func (a *Agent) Busy() bool {
 if a.maintenanceHeld(){return true}
	h := a.currentLease()
	if h != nil && strings.TrimSpace(h.LeaseID) != "" {
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
 a.maintenanceMu.Lock();defer a.maintenanceMu.Unlock()
 if a.maintenanceHeld(){return}
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
		a.log.Warn("update check failed", "err", update.RedactQuery(err.Error()))
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
	badBuild, err := update.ReadBadBuild(filepath.Join(filepath.Dir(marker), update.BadName))
	if err != nil {
		return err
	}
	plan, err := update.Tick(ctx, update.Config{
		Channel:        a.cfg.UpdateChannel,
        CurrentChannel: buildinfo.CurrentChannel(),
		CurrentVersion: buildinfo.CurrentVersion(),
		CurrentCommit:  buildinfo.Current(),
		CurrentBuild:   buildinfo.CurrentBuild(),
		BadBuild:       badBuild,
		Arch:           runtime.GOARCH,
		BinaryPath:     binary,
		PubKeyPath:     a.cfg.UpdatePubKey,
		Fingerprints:   a.cfg.UpdateFingerprints,
		MarkerPath:     marker,
		Busy:           busy,
		RollbackWindow: a.cfg.UpdateRollback,
	}, update.Hooks{
		Source:    a.updateSource(),
		StillBusy: a.Busy,
	})
	if err != nil {
		return err
	}
	logUpdate(a.log, plan)
	return nil
}

// logUpdate writes one update decision. The steady state, already
// running the signed build, is debug. Install, reject, and defer stay
// at info.
func logUpdate(log *slog.Logger, plan update.Plan) {
	args := []any{
		"action", plan.Action,
		"version", plan.Version,
		"commit", plan.Commit,
		"reason", update.RedactQuery(plan.Reason),
	}
	if plan.Action == update.ActionSkip && plan.Reason == "up to date" {
		log.Debug("update", args...)
		return
	}
	log.Info("update", args...)
}

// updateSource asks the control plane with the device credential the
// agent already uses. Artifact bytes are fetched from the SAS URL
// with no Authorization header.
func (a *Agent) updateSource() update.Source {
	return update.Source{
		LatestFn: func(ctx context.Context, channel string) (*update.Candidate, error) {
			m, err := a.client.LatestUpdate(ctx, channel, a.state.DeviceID)
			if err != nil || m == nil {
				return nil, err
			}
			return candidateFromManifest(m), nil
		},
	}
}

func candidateFromManifest(m *api.UpdateManifest) *update.Candidate {
	c := &update.Candidate{
		Channel:   m.Channel,
		Version:   m.Version,
		Commit:    m.Commit,
		ReleaseID: m.ReleaseID,
		Build:     m.Build,
	}
	for _, art := range m.Artifacts {
		c.Artifacts = append(c.Artifacts, update.Artifact{
			Name:   art.Name,
			SHA256: art.SHA256,
			Size:   art.Size,
			URL:    art.URL,
		})
	}
	return c
}
