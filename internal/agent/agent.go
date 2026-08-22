// Package agent is the enroll → heartbeat → job loop.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/logsafe"
	"github.com/FyberLabs/hypermesh-host/internal/pathb"
	"github.com/FyberLabs/hypermesh-host/internal/telem"
	"github.com/FyberLabs/hypermesh-host/internal/tunnel"
)

// Agent is the host process.
type Agent struct {
	cfg    Config
	client *api.Client
	telem  telem.Source
	pathb  pathb.Runner
	log    *slog.Logger
	state  State
}

// New builds an agent. log may be nil.
func New(cfg Config, log *slog.Logger) (*Agent, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if log == nil {
		log = logsafe.New()
	}
	c := api.NewClient(cfg.ControlPlaneURL, cfg.TenantID)
	c.DeviceAuth = api.DeviceAuth{
		Header: cfg.DeviceSecretHeader,
		Value:  cfg.DeviceSecret,
	}
	src := telem.Default()
	if cfg.ImageHash != "" {
		// Prefer the explicit pin; Default() still consults the env/file.
		src.ImageHashEnv = "HYPERMESH_IMAGE_HASH"
	}
	return &Agent{
		cfg:    cfg,
		client: c,
		telem:  src,
		pathb:  pathb.Runner{Collector: src},
		log:    log,
	}, nil
}

// Run enrolls if needed, then heartbeats and pulls jobs until ctx is done.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.ensureEnrolled(ctx); err != nil {
		return err
	}
	// Tunnel allocate is not on the control plane yet.
	if err := tunnel.Apply(ctx, nil); err != nil {
		return err
	}

	if err := a.heartbeat(ctx); err != nil {
		a.log.Warn("heartbeat failed", "err", err)
	}
	if err := a.pullAndRun(ctx); err != nil {
		a.log.Warn("job pull failed", "err", err)
	}

	hb := time.NewTicker(a.cfg.HeartbeatEvery)
	jobs := time.NewTicker(a.cfg.JobEvery)
	defer hb.Stop()
	defer jobs.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-hb.C:
			if err := a.heartbeat(ctx); err != nil {
				a.log.Warn("heartbeat failed", "err", err)
			}
		case <-jobs.C:
			if err := a.pullAndRun(ctx); err != nil {
				a.log.Warn("job pull failed", "err", err)
			}
		}
	}
}

func (a *Agent) ensureEnrolled(ctx context.Context) error {
	st, err := a.loadState()
	if err != nil {
		return err
	}
	if strings.TrimSpace(st.DeviceID) != "" {
		a.state = st
		a.log.Info("using stored device", "device_id", st.DeviceID)
		return nil
	}
	if strings.TrimSpace(a.cfg.EnrollToken) == "" {
		return fmt.Errorf("hypermesh: no device_id on disk and no enroll token")
	}

	snap, err := a.telem.Collect(ctx)
	if err != nil {
		a.log.Warn("telemetry before enroll failed", "err", err)
	}
	image := firstNonEmpty(a.cfg.ImageHash, snap.ImageHash)

	resp, err := a.client.Enroll(ctx, api.EnrollRequest{
		EnrollToken: a.cfg.EnrollToken,
		ImageHash:   image,
	})
	if err != nil {
		// Do not log the token. Status errors already omit bodies.
		return fmt.Errorf("hypermesh: enroll: %w", err)
	}
	st = State{DeviceID: resp.DeviceID, CredentialPrefix: resp.CredentialPrefix}
	if err := a.saveState(st); err != nil {
		return err
	}
	a.state = st
	a.log.Info("enrolled", "device_id", st.DeviceID, "credential_prefix", st.CredentialPrefix)
	return nil
}

func (a *Agent) heartbeat(ctx context.Context) error {
	snap, err := a.telem.Collect(ctx)
	if err != nil {
		return err
	}
	image := firstNonEmpty(a.cfg.ImageHash, snap.ImageHash)
	hb, err := a.client.Heartbeat(ctx, api.HeartbeatRequest{
		DeviceID:  a.state.DeviceID,
		ImageHash: image,
		UptimeS:   snap.UptimeS,
		ThermalC:  snap.ThermalC,
	})
	if err != nil {
		return err
	}
	a.log.Info("heartbeat accepted",
		"device_id", hb.DeviceID,
		"path_b_state", hb.PathBState,
		"nvpmodel", snap.Nvpmodel,
	)
	return nil
}

func (a *Agent) pullAndRun(ctx context.Context) error {
	job, err := a.client.PullJob(ctx, a.state.DeviceID)
	if err != nil {
		return err
	}
	if job == nil {
		return nil
	}
	a.log.Info("got job", "job_id", job.ID, "kind", job.Kind, "suite_id", job.SuiteID)
	res := a.pathb.Run(ctx, *job)
	_, err = a.client.PostResult(ctx, job.ID, api.JobResultRequest{
		Passed:    res.Passed,
		ImageHash: res.ImageHash,
	})
	if err != nil {
		return err
	}
	a.log.Info("posted job result", "job_id", job.ID, "passed", res.Passed)
	return nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
