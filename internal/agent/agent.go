// Package agent is the enroll → heartbeat → tunnel → job loop.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
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
	applied string // fingerprint of the host peer we last acked (not tunnel_id)
	now     func() time.Time
	ownerMu sync.Mutex
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
		cfg:    cfg,
		client: api.NewClient(cfg.ControlPlaneURL, cfg.TenantID),
		telem:  src,
		pathb: pathb.Runner{
			Collector: src,
			Endpoint:  cfg.CompletionsEndpoint(),
			Store:     pathb.FileStore{Dir: cfg.StateDir},
		},
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
	if err := a.startOwner(ctx); err != nil {
		return err
	}

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
		return fmt.Errorf("hypermesh: this box has an id on disk but no secret; it cannot enroll again — ask for a new enroll-token")
	}
	if strings.TrimSpace(a.cfg.EnrollToken) == "" {
		return fmt.Errorf("hypermesh: this box has no saved identity and no enroll token")
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
		return fmt.Errorf("hypermesh: could not enroll this box: %w", err)
	}
	if strings.TrimSpace(resp.DeviceSecret) == "" {
		return fmt.Errorf("hypermesh: enroll did not return a secret")
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
	sell := a.effectiveSell()
	hb, err := a.client.Heartbeat(ctx, api.HeartbeatRequest{
		DeviceID:  a.state.DeviceID,
		ImageHash: image,
		UptimeS:   snap.UptimeS,
		ThermalC:  snap.ThermalC,
		SellState: sell.SellState,
	})
	if err != nil {
		return err
	}
	a.log.Info("heartbeat accepted",
		"device_id", hb.DeviceID,
		"path_b_state", hb.PathBState,
		"sell_state", sell.SellState,
		"nvpmodel", snap.Nvpmodel,
		"backend", runtime.NVIDIALogLabel,
	)
	return nil
}

func (a *Agent) syncTunnel(ctx context.Context) error {
	sess, err := a.client.GetTunnel(ctx, a.state.DeviceID)
	if err != nil {
		return err
	}
	if sess == nil {
		if err := a.tunnels.Teardown(ctx); err != nil {
			return err
		}
		if a.applied != "" {
			a.log.Info("tunnel gone (204), torn down")
		}
		a.applied = ""
		return nil
	}
	switch sess.Status {
	case api.TunnelAllocated:
		ts := tunnel.Session{
			TunnelID:        sess.TunnelID,
			LeaseID:         sess.LeaseID,
			Status:          string(sess.Status),
			Endpoint:        sess.Endpoint,
			ServerPublicKey: sess.ServerPublicKey,
			PeerPrivateKey:  sess.PeerPrivateKey,
			AllowedIPs:      sess.AllowedIPs,
			ClientAddress:   sess.ClientAddress,
		}
		// lease_id / tunnel_id are tickets on the same host peer.
		// Apply is a no-op unless the peer fields change.
		if err := a.tunnels.Apply(ctx, ts); err != nil {
			return err
		}
		fp := tunnel.PeerFingerprint(ts)
		if a.applied != fp {
			if err := a.client.AckTunnel(ctx, a.state.DeviceID, api.TunnelAckRequest{Applied: true}); err != nil {
				return err
			}
			a.applied = fp
			a.log.Info("host peer applied", "tunnel_id", sess.TunnelID, "lease_id", sess.LeaseID)
		}
		return nil
	case api.TunnelRevoked:
		if err := a.tunnels.Teardown(ctx); err != nil {
			return err
		}
		a.applied = ""
		a.log.Info("host peer revoked, torn down", "tunnel_id", sess.TunnelID)
		return nil
	default:
		return fmt.Errorf("hypermesh: unknown tunnel status %s", sess.Status)
	}
}

func (a *Agent) pullAndRun(ctx context.Context) error {
	_ = a.effectiveSell()
	job, err := a.client.PullJob(ctx, a.state.DeviceID)
	if err != nil {
		return err
	}
	if job == nil {
		return nil
	}
	a.log.Info("got job", "job_id", job.JobID, "kind", job.Kind, "suite_id", job.SuiteID, "lease_id", job.LeaseID, "backend", jobBackendLog(*job))

	res := a.runJob(ctx, *job)
	if err := a.client.PostResult(ctx, job.JobID, a.state.DeviceID, jobResultBody(job.Kind, res)); err != nil {
		return err
	}
	a.log.Info("posted job result", "job_id", job.JobID, "passed", res.Passed, "reason", res.Reason, "backend", jobBackendLog(*job))
	return nil
}

func (a *Agent) runJob(ctx context.Context, job api.Job) jobOutcome {
	_ = a.effectiveSell()
	switch job.Kind {
	case api.KindPathB, api.KindInstanceCheck:
		if res, stub := a.backendStub(job); stub {
			return jobOutcome{Result: res}
		}
		return jobOutcome{Result: a.pathb.Run(ctx, job)}
	case api.KindLeaseStart:
		return jobOutcome{Result: a.leaseStart(ctx, job)}
	case api.KindLeaseStop:
		return jobOutcome{Result: a.leaseStop(ctx, job)}
	case api.KindSellPause:
		return a.sellPause(job)
	case "":
		return jobOutcome{Result: pathb.Result{Passed: false, Reason: "empty job kind"}}
	default:
		return jobOutcome{Result: pathb.Result{Passed: false, Reason: "unknown job kind"}}
	}
}

// backendStub fails lease_start / path_b / instance_check on a stub or
// unknown backend. instance_check must not probe or fake a completion.
func (a *Agent) backendStub(job api.Job) (pathb.Result, bool) {
	path, reason := runtime.ResolveBackend(job.Backend)
	if path == runtime.NVIDIAContainer {
		return pathb.Result{}, false
	}
	a.log.Info("backend stub", "kind", job.Kind, "backend", runtime.LogLabel(job.Backend), "reason", reason)
	return pathb.Result{Passed: false, Reason: reason}, true
}

func (a *Agent) leaseStart(ctx context.Context, job api.Job) pathb.Result {
	if res, stub := a.backendStub(job); stub {
		return res
	}
	if sell := a.effectiveSell(); sell.SellState == api.SellPaused {
		a.log.Info("lease_start refused", "reason", "sell paused", "sell_state", sell.SellState, "until", sell.Until)
		return pathb.Result{Passed: false, Reason: "sell paused"}
	}
	if strings.TrimSpace(job.ImageDigest) == "" {
		return pathb.Result{Passed: false, Reason: "no image_digest; no soak image"}
	}
	h, err := a.runtime.Start(ctx, runtime.Spec{
		LeaseID:     job.LeaseID,
		CatalogID:   job.CatalogID,
		ImageDigest: job.ImageDigest,
		Runtime:     job.Runtime,
		HostPort:    a.cfg.PublishPort(),
	})
	if err != nil {
		reason := "runtime start failed"
		switch {
		case errors.Is(err, runtime.ErrNoImage):
			reason = "no image_digest; no soak image"
		case errors.Is(err, runtime.ErrBareDigest):
			reason = "image_digest is not repo@digest"
		case errors.Is(err, runtime.ErrNoLeaseID):
			reason = "lease_start requires lease_id"
		}
		return pathb.Result{Passed: false, Reason: reason}
	}
	a.lease = h
	if err := a.saveLease(h); err != nil {
		a.log.Warn("lease record not saved", "err", err)
	}
	snap, _ := a.telem.Collect(ctx)
	image := firstNonEmpty(a.cfg.ImageHash, snap.ImageHash)
	a.log.Info("lease_start container", "lease_id", job.LeaseID, "catalog_id", job.CatalogID, "backend", runtime.NVIDIALogLabel)
	return pathb.Result{Passed: true, ImageHash: image, Reason: "lease_start"}
}

func jobBackendLog(job api.Job) string {
	return runtime.LogLabel(job.Backend)
}

func (a *Agent) leaseStop(ctx context.Context, job api.Job) pathb.Result {
	h := a.lease
	if h == nil {
		h = a.loadLease()
	}
	if h == nil {
		// Soft no-op: already stopped / no local handle. Do not fail.
		a.log.Info("lease_stop", "lease_id", job.LeaseID, "reason", "no local handle")
		snap, _ := a.telem.Collect(ctx)
		return pathb.Result{Passed: true, ImageHash: firstNonEmpty(a.cfg.ImageHash, snap.ImageHash), Reason: "lease_stop"}
	}

	// Strict identity before docker stop. Empty job.lease_id is a mismatch
	// when a handle exists — do not stop whatever is running.
	jobLease := strings.TrimSpace(job.LeaseID)
	localLease := strings.TrimSpace(h.LeaseID)
	if jobLease == "" || jobLease != localLease {
		a.log.Info("lease_stop refused", "lease_id", job.LeaseID, "local_lease_id", h.LeaseID, "reason", "lease_id mismatch")
		return pathb.Result{Passed: false, Reason: "lease_id mismatch"}
	}

	if err := a.runtime.Stop(ctx, h); err != nil {
		return pathb.Result{Passed: false, Reason: "runtime stop failed"}
	}
	a.lease = nil
	_ = a.saveLease(nil)
	// Host WireGuard peer stays up. Teardown only on GET revoked or 204.
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
