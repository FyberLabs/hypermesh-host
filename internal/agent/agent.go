// Package agent is the enroll → heartbeat → tunnel → job loop.
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
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
	"github.com/FyberLabs/hypermesh-host/internal/telem"
	"github.com/FyberLabs/hypermesh-host/internal/tunnel"
)

// Agent is the host process.
type Agent struct {
	cfg     Config
	client  *api.Client
	telem   telem.Source
	pathb   pathb.Runner
	runtime *runtime.NVIDIA
	tunnels tunnel.Applier
	log     *slog.Logger
	state   State
	lease   *runtime.Handle
	applied string // last allocated tunnel_id we applied
}

// New builds an agent. log may be nil.
func New(cfg Config, log *slog.Logger) (*Agent, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if log == nil {
		log = logsafe.New()
	}
	src := telem.Default()
	return &Agent{
		cfg:     cfg,
		client:  api.NewClient(cfg.ControlPlaneURL, cfg.TenantID),
		telem:   src,
		pathb:   pathb.Runner{Collector: src},
		runtime: runtime.NewNVIDIA(),
		tunnels: tunnel.NewRunner(),
		log:     log,
	}, nil
}

// Run enrolls if needed, then heartbeats, syncs tunnel, and pulls jobs.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.ensureEnrolled(ctx); err != nil {
		return err
	}
	a.lease = a.loadLease()

	if err := a.heartbeat(ctx); err != nil {
		a.log.Warn("heartbeat failed", "err", err)
	}
	if err := a.syncTunnel(ctx); err != nil {
		a.log.Warn("tunnel sync failed", "err", err)
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
			if err := a.syncTunnel(ctx); err != nil {
				a.log.Warn("tunnel sync failed", "err", err)
			}
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
	if st.DeviceSecret == "" {
		st.DeviceSecret = a.cfg.DeviceSecret
	}
	if strings.TrimSpace(st.DeviceID) != "" && strings.TrimSpace(st.DeviceSecret) != "" {
		a.state = st
		a.client.DeviceSecret = st.DeviceSecret
		a.log.Info("using stored device", "device_id", st.DeviceID)
		return nil
	}
	if strings.TrimSpace(st.DeviceID) != "" && strings.TrimSpace(st.DeviceSecret) == "" {
		return fmt.Errorf("hypermesh: device_id on disk but no device_secret; this device cannot re-enroll — mint a token with POST /api/v1/hypermesh/devices/{device_id}/enroll-token")
	}
	if strings.TrimSpace(a.cfg.EnrollToken) == "" {
		return fmt.Errorf("hypermesh: no device_id+secret on disk and no enroll token")
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
		return fmt.Errorf("hypermesh: enroll: %w", err)
	}
	if strings.TrimSpace(resp.DeviceSecret) == "" {
		return fmt.Errorf("hypermesh: enroll returned no device_secret")
	}
	st = State{
		DeviceID:         resp.DeviceID,
		CredentialPrefix: resp.CredentialPrefix,
		DeviceSecret:     resp.DeviceSecret,
	}
	if err := a.saveState(st); err != nil {
		return err
	}
	a.state = st
	a.client.DeviceSecret = st.DeviceSecret
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

func (a *Agent) syncTunnel(ctx context.Context) error {
	sess, err := a.client.GetTunnel(ctx, a.state.DeviceID)
	if err != nil {
		return err
	}
	if sess == nil {
		if a.applied != "" {
			if err := a.tunnels.Teardown(ctx); err != nil {
				return err
			}
			a.applied = ""
			a.log.Info("tunnel gone (204), torn down")
		}
		return nil
	}
	switch sess.Status {
	case api.TunnelAllocated:
		if a.applied == sess.TunnelID {
			return nil
		}
		if err := a.tunnels.Apply(ctx, tunnel.Session{
			TunnelID:        sess.TunnelID,
			LeaseID:         sess.LeaseID,
			Status:          string(sess.Status),
			Endpoint:        sess.Endpoint,
			ServerPublicKey: sess.ServerPublicKey,
			PeerPrivateKey:  sess.PeerPrivateKey,
			AllowedIPs:      sess.AllowedIPs,
			ClientAddress:   sess.ClientAddress,
		}); err != nil {
			return err
		}
		if err := a.client.AckTunnel(ctx, a.state.DeviceID, api.TunnelAckRequest{Applied: true}); err != nil {
			return err
		}
		a.applied = sess.TunnelID
		a.log.Info("tunnel applied", "tunnel_id", sess.TunnelID, "lease_id", sess.LeaseID)
		return nil
	case api.TunnelRevoked:
		if err := a.tunnels.Teardown(ctx); err != nil {
			return err
		}
		a.applied = ""
		a.log.Info("tunnel revoked, torn down", "tunnel_id", sess.TunnelID)
		return nil
	default:
		return fmt.Errorf("hypermesh: unknown tunnel status %s", sess.Status)
	}
}

func (a *Agent) pullAndRun(ctx context.Context) error {
	job, err := a.client.PullJob(ctx, a.state.DeviceID)
	if err != nil {
		return err
	}
	if job == nil {
		return nil
	}
	a.log.Info("got job", "job_id", job.JobID, "kind", job.Kind, "suite_id", job.SuiteID, "lease_id", job.LeaseID)

	res := a.runJob(ctx, *job)
	if err := a.client.PostResult(ctx, job.JobID, a.state.DeviceID, api.JobResultRequest{
		Passed:    res.Passed,
		ImageHash: res.ImageHash,
	}); err != nil {
		return err
	}
	a.log.Info("posted job result", "job_id", job.JobID, "passed", res.Passed)
	return nil
}

func (a *Agent) runJob(ctx context.Context, job api.Job) pathb.Result {
	switch job.Kind {
	case api.KindPathB, api.KindInstanceCheck, "":
		return a.pathb.Run(ctx, job)
	case api.KindLeaseStart:
		return a.leaseStart(ctx, job)
	case api.KindLeaseStop:
		return a.leaseStop(ctx, job)
	default:
		return pathb.Result{Passed: false, Reason: "unknown job kind"}
	}
}

func (a *Agent) leaseStart(ctx context.Context, job api.Job) pathb.Result {
	h, err := a.runtime.Start(ctx, runtime.Spec{
		Name:        "hypermesh-lease",
		LeaseID:     job.LeaseID,
		CatalogID:   job.CatalogID,
		ImageDigest: job.ImageDigest,
		Runtime:     job.Runtime,
	})
	if err != nil {
		return pathb.Result{Passed: false, Reason: "runtime start failed"}
	}
	a.lease = h
	if err := a.saveLease(h); err != nil {
		a.log.Warn("lease record not saved", "err", err)
	}
	snap, _ := a.telem.Collect(ctx)
	image := firstNonEmpty(a.cfg.ImageHash, snap.ImageHash)
	if h.Noop {
		a.log.Info("lease_start recorded no-op (empty image_digest)", "lease_id", job.LeaseID, "catalog_id", job.CatalogID)
	} else {
		a.log.Info("lease_start container", "lease_id", job.LeaseID, "catalog_id", job.CatalogID)
	}
	return pathb.Result{Passed: true, ImageHash: image, Reason: "lease_start"}
}

func (a *Agent) leaseStop(ctx context.Context, job api.Job) pathb.Result {
	h := a.lease
	if h == nil {
		h = a.loadLease()
	}
	if h != nil {
		if err := a.runtime.Stop(ctx, h); err != nil {
			return pathb.Result{Passed: false, Reason: "runtime stop failed"}
		}
	}
	a.lease = nil
	_ = a.saveLease(nil)
	// Tunnel teardown waits for GET /agent/tunnel status=revoked.
	a.log.Info("lease_stop", "lease_id", job.LeaseID)
	snap, _ := a.telem.Collect(ctx)
	return pathb.Result{Passed: true, ImageHash: firstNonEmpty(a.cfg.ImageHash, snap.ImageHash), Reason: "lease_stop"}
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
