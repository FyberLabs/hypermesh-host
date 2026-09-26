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
	"github.com/FyberLabs/hypermesh-host/internal/bench"
	"github.com/FyberLabs/hypermesh-host/internal/catalog"
	"github.com/FyberLabs/hypermesh-host/internal/logsafe"
	"github.com/FyberLabs/hypermesh-host/internal/pathb"
	"github.com/FyberLabs/hypermesh-host/internal/pull"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
	"github.com/FyberLabs/hypermesh-host/internal/telem"
	"github.com/FyberLabs/hypermesh-host/internal/tunnel"
)

// Agent is the host process.
type Agent struct {
	cfg             Config
	client          *api.Client
	telem           telem.Source
	pathb           pathb.Runner
	runtime         *runtime.NVIDIA
	hip             *runtime.HIP
	metal           *runtime.Metal
	tunnels         tunnel.Applier
	log             *slog.Logger
	state           State
	lease           *runtime.Handle
	applied         string // fingerprint of the host peer we last acked (not tunnel_id)
	bindIP          string // last tunnel client address, used to publish completions
	keyDir          string // host WireGuard private key lives here only
	peerPosted      bool   // this process already POSTed postedPub (404 counts: the device key was saved)
	postedPub       string // last public key sent to POST /agent/tunnel/peer
	loggedNoTunnel  bool   // already logged success-with-no-tunnel
	loggedRevoked   bool   // already logged a GET 200 status=revoked streak
	now             func() time.Time
	sleep           func(time.Duration)
	resultTries     int
	resultRetryWait time.Duration
	ownerMu         sync.Mutex
	// inspectCatalog reads a local repo digest. Nil uses bench.Resolve,
	// which pulls the workload tag when the image is not already local.
	inspectCatalog func(ctx context.Context, catalogID string) string
	digests        map[string]string
	// certReadyWait bounds how long init waits for the cert container
	// to answer. Zero uses three minutes. Tests set a short wait.
	certReadyWait time.Duration
	// fetchWeights downloads a pinned artifact for lab_pull. Nil uses
	// the allowlisted HTTP fetcher. Tests stub this — never hit the network.
	fetchWeights func(ctx context.Context, dest, sourceRef, pin string) error
	// dockerRun runs docker CLI. Nil uses the NVIDIA runtime's runner when
	// available; tests inject a stub for egress observation.
	dockerRun runtime.DockerRunner
	// egressSample samples host-visible connections. Nil uses ss/conntrack.
	egressSample runtime.ConnSampler
	// countLeases counts running lease containers. Nil uses docker ps.
	// A false ok omits lease_container_count. Tests stub this.
	countLeases func(ctx context.Context) (n int, ok bool)
	// statFree is statfs Bavail*Bsize on the GGUF filesystem. Nil uses
	// pull.FreeBytes. The same value is disk_free_bytes and the pull
	// preflight. An error omits the heartbeat field and fails the pull
	// closed. Tests stub this.
	statFree func(path string) (int64, error)
	// headLength is HTTP HEAD Content-Length for a download URL.
	// Nil uses pull.HTTPHeadLength. Tests stub this so they do not dial.
	headLength func(ctx context.Context, rawURL string) (int64, error)

	leaseMu    sync.RWMutex
	healthMu   sync.Mutex
	lastSyncAt time.Time
	haveSync   bool
	syncClass  string
	syncHTTP   int
	hbOK       uint64
	hbFail     map[string]uint64
	syncOK     uint64
	syncFail   map[string]uint64
}

const certLeaseID = "cert-bench"

const (
	// HostTypeModel is the AGX (and any box that can load a weight).
	HostTypeModel = "model"
	// HostTypeSupervisor enrolls and heartbeats, owns no weight, and
	// refuses lease_start. hypermesh-supervisor uses this host type.
	HostTypeSupervisor = "supervisor"
)

// SupervisorLeaseRefuse is the lease_start reason for a supervisor host.
const SupervisorLeaseRefuse = "supervisor host refuses lease_start"

func keyDirFor(cfg Config) string {
	if d := strings.TrimSpace(cfg.WGKeyDir); d != "" {
		return d
	}
	return tunnel.DefaultKeyDir
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
	wg := tunnel.NewRunnerMode(cfg.WGMode)
	wg.Log = log
	if wg.Userspace {
		log.Info("userspace wireguard", "unit", "hypermesh-wg.service")
	}
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
		hip:     runtime.NewHIP(),
		metal:   runtime.NewMetal(),
		tunnels: wg,
		keyDir:  keyDirFor(cfg),
		log:     log,
	}, nil
}

// Run enrolls if needed, then heartbeats, syncs tunnel, and pulls jobs.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.ensureEnrolled(ctx); err != nil {
		return err
	}
	a.setLease(a.loadLease())
	if err := a.startOwner(ctx); err != nil {
		return err
	}
	if err := ensureModelDir(a.cfg.GGUFHostPath()); err != nil {
		a.log.Warn("model directory not created", "path", a.cfg.GGUFHostPath(), "err", err)
	}

	// Heartbeat and jobs are separate loops. A lab_pull of a multi-gigabyte
	// GGUF must not stall the heartbeat ticker: the plane drops a host
	// whose heartbeat is older than 180s. Shutdown waits for the in-flight
	// call on each loop, which stops when ctx is cancelled.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		a.heartbeatLoop(ctx)
	}()
	go func() {
		defer wg.Done()
		a.jobLoop(ctx)
	}()
	wg.Wait()
	return ctx.Err()
}

func (a *Agent) heartbeatLoop(ctx context.Context) {
	if err := a.heartbeat(ctx); err != nil {
		a.log.Warn("heartbeat failed", "err", err)
	}
	ticker := time.NewTicker(a.cfg.HeartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.heartbeat(ctx); err != nil {
				a.log.Warn("heartbeat failed", "err", err)
			}
		}
	}
}

func (a *Agent) jobLoop(ctx context.Context) {
	a.syncAndRecord(ctx)
	if err := a.pullAndRun(ctx); err != nil {
		a.log.Warn("job pull failed", "err", err)
	}
	ticker := time.NewTicker(a.cfg.JobEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.syncAndRecord(ctx)
			if err := a.pullAndRun(ctx); err != nil {
				a.log.Warn("job pull failed", "err", err)
			}
		}
	}
}

func (a *Agent) setLease(h *runtime.Handle) {
	a.leaseMu.Lock()
	a.lease = h
	a.leaseMu.Unlock()
}

func (a *Agent) currentLease() *runtime.Handle {
	a.leaseMu.RLock()
	defer a.leaseMu.RUnlock()
	return a.lease
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
		HostType:    a.cfg.ReportedHostType(),
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
		a.recordHeartbeat(err)
		return err
	}
	image := firstNonEmpty(a.cfg.ImageHash, snap.ImageHash)
	sell := a.effectiveSell()
	hb, err := a.client.Heartbeat(ctx, api.HeartbeatRequest{
		DeviceID:        a.state.DeviceID,
		ImageHash:       image,
		LoadedModelHash: a.leaseImageDigest(),
		UptimeS:         snap.UptimeS,
		ThermalC:        snap.ThermalC,
		SellState:       sell.SellState,
		Health:          a.safeHealth(ctx),
	})
	if err != nil {
		a.recordHeartbeat(err)
		return err
	}
	a.recordHeartbeat(nil)
	a.log.Info("heartbeat accepted",
		"device_id", hb.DeviceID,
		"path_b_state", hb.PathBState,
		"sell_state", sell.SellState,
		"nvpmodel", snap.Nvpmodel,
		"backend", runtime.HostLogLabel(),
	)
	return nil
}

// errRevokedStop ends this sync after a POST 409 "Tunnel is revoked".
// The warning is already logged and the interface is down.
var errRevokedStop = errors.New("hypermesh: tunnel revoked")

func (a *Agent) syncTunnel(ctx context.Context) error {
	// Plane contract (panopticon routes.py): Bearer hm_dev_ plus X-Tenant-ID.
	// POST /agent/tunnel/peer has no device_id. Body is only peer_public_key.
	// 404 "Tunnel not found" still saved the device key. 409 "Tunnel is revoked"
	// means do not bring WireGuard up. GET 204 is no tunnel. GET 200
	// status=revoked is not an error: tear WireGuard down and do not POST,
	// even when peer_public_key is null or different. GET 200 status=allocated
	// with a null or different key, or GET 409 whose body code is
	// peer_public_key_not_registered (not detail), means POST then re-GET.
	// On rotation keep the live config until a GET shows the new public key, then switch and
	// ack again (ack_applied is not cleared on the plane).
	priv, pub, err := tunnel.LoadOrCreatePrivateKey(a.keyDir)
	if err != nil {
		return err
	}
	if strings.TrimSpace(priv) == "" {
		return tunnel.ErrNoHostKey
	}
	if !a.peerPosted || a.postedPub != pub {
		if err := a.registerPeer(ctx, pub); err != nil {
			if errors.Is(err, errRevokedStop) {
				return nil
			}
			a.noteSyncFailure(ctx)
			return err
		}
	}

	sess, err := a.client.GetTunnel(ctx, a.state.DeviceID)
	if err == nil && sess != nil && sess.Status == api.TunnelRevoked {
		// 200 status=revoked is not an error. Do not POST because the
		// public key is null or different.
		return a.tunnelRevoked(ctx, sess)
	}
	sess, err = a.reregisterIfNeeded(ctx, sess, err, pub)
	if errors.Is(err, errRevokedStop) {
		return nil
	}
	if err != nil {
		// 401/503 is not a revoke. A handshaking userspace interface stays up.
		a.noteSyncFailure(ctx)
		return err
	}
	if sess == nil {
		return a.tunnelGone(ctx)
	}
	switch sess.Status {
	case api.TunnelAllocated:
		if sessionNeedsPeer(sess.PeerPublicKey, pub) {
			a.log.Info("session peer key does not match yet; keeping the current WireGuard config")
			return nil
		}
		return a.applyMatched(ctx, sess, priv)
	case api.TunnelRevoked:
		return a.tunnelRevoked(ctx, sess)
	default:
		return fmt.Errorf("hypermesh: unknown tunnel status %s", sess.Status)
	}
}

// reregisterIfNeeded POSTs and re-GETs when the session has no matching
// public key, or when GET returns peer_public_key_not_registered.
func (a *Agent) reregisterIfNeeded(ctx context.Context, sess *api.TunnelSession, err error, pub string) (*api.TunnelSession, error) {
	needs := errors.Is(err, api.ErrPeerPublicKeyNotRegistered)
	if err == nil && sess != nil && sess.Status == api.TunnelAllocated && sessionNeedsPeer(sess.PeerPublicKey, pub) {
		needs = true
	}
	if !needs {
		return sess, err
	}
	a.log.Info("tunnel peer key does not match; posting the public key and reading the session again")
	if err := a.registerPeer(ctx, pub); err != nil {
		return nil, err
	}
	return a.client.GetTunnel(ctx, a.state.DeviceID)
}

// registerPeer POSTs only the public key. 404 is success with no tunnel
// (the plane saved wg_public_key first). 409 revoked is logged and the
// interface stays down.
func (a *Agent) registerPeer(ctx context.Context, pub string) error {
	err := a.client.ProposePeer(ctx, api.TunnelPeerRequest{PeerPublicKey: pub})
	if errors.Is(err, api.ErrNoTunnel) {
		a.peerPosted = true
		a.postedPub = pub
		if !a.loggedNoTunnel {
			a.log.Info("host public key saved; no tunnel allocated yet")
			a.loggedNoTunnel = true
		}
		return nil
	}
	if errors.Is(err, api.ErrTunnelRevoked) {
		a.peerPosted = true
		a.postedPub = pub
		a.log.Warn("tunnel is revoked; not bringing WireGuard up")
		if terr := a.tunnels.Teardown(ctx); terr != nil {
			return terr
		}
		a.applied = ""
		return errRevokedStop
	}
	if err != nil {
		return err
	}
	a.loggedNoTunnel = false
	a.peerPosted = true
	a.postedPub = pub
	return nil
}

// noteSyncFailure leaves a userspace interface alone when the plane fetch
// fails. Revoke and 204 still call Teardown; this path must not.
func (a *Agent) noteSyncFailure(ctx context.Context) {
	type preserver interface {
		OnSyncError(context.Context) (bool, error)
	}
	p, ok := a.tunnels.(preserver)
	if !ok {
		return
	}
	kept, err := p.OnSyncError(ctx)
	if err != nil {
		a.log.Warn("tunnel preserve failed", "err", err)
		return
	}
	if kept {
		a.log.Info("tunnel sync failed; leaving handshaking interface up")
	}
}

func (a *Agent) tunnelRevoked(ctx context.Context, sess *api.TunnelSession) error {
	if err := a.tunnels.Teardown(ctx); err != nil {
		return err
	}
	if !a.loggedRevoked {
		a.log.Info("host peer revoked, torn down", "tunnel_id", sess.TunnelID)
		a.loggedRevoked = true
	}
	a.applied = ""
	return nil
}

func (a *Agent) tunnelGone(ctx context.Context) error {
	if err := a.tunnels.Teardown(ctx); err != nil {
		return err
	}
	if a.applied != "" {
		a.log.Info("tunnel gone (204), torn down")
	}
	a.applied = ""
	return nil
}

func (a *Agent) applyMatched(ctx context.Context, sess *api.TunnelSession, priv string) error {
	a.loggedRevoked = false
	a.bindIP = sess.ClientAddress
	a.refreshCompletions()
	ts := tunnel.Session{
		TunnelID:        sess.TunnelID,
		LeaseID:         sess.LeaseID,
		Status:          string(sess.Status),
		Endpoint:        sess.Endpoint,
		ServerPublicKey: sess.ServerPublicKey,
		PeerPrivateKey:  priv,
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
		// Rotation does not clear ack_applied on the plane, so a new config acks again.
		if err := a.client.AckTunnel(ctx, a.state.DeviceID, api.TunnelAckRequest{Applied: true}); err != nil {
			return err
		}
		a.applied = fp
		a.log.Info(a.peerApplyMessage(), "tunnel_id", sess.TunnelID, "lease_id", sess.LeaseID)
	}
	return nil
}

// peerApplyMessage distinguishes a restart that left the peer in place
// from one that programmed it. tunnel_id and lease_id stay on the line.
func (a *Agent) peerApplyMessage() string {
	type outcome interface {
		ApplyOutcome() string
	}
	o, ok := a.tunnels.(outcome)
	if !ok {
		return "host peer applied"
	}
	switch o.ApplyOutcome() {
	case "skip":
		return "host peer unchanged (skip apply)"
	case "syncconf":
		return "host peer applied (syncconf)"
	default:
		return "host peer applied"
	}
}

// sessionNeedsPeer is true when the plane has no host public key yet, or a
// different one. A match means this box already registered the local key.
func sessionNeedsPeer(planePub, localPub string) bool {
	return strings.TrimSpace(planePub) != strings.TrimSpace(localPub)
}

func (a *Agent) liveCompletionsURL() string {
	if u := strings.TrimSpace(a.cfg.CompletionsURL); u != "" {
		return u
	}
	host := runtime.TunnelBindHost(a.bindIP)
	if host == "" {
		return a.cfg.CompletionsEndpoint()
	}
	return "http://" + host + ":" + a.cfg.PublishPort() + "/v1/chat/completions"
}

func (a *Agent) refreshCompletions() {
	a.pathb.Endpoint = a.liveCompletionsURL()
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

	if job.Kind == api.KindPathB || job.Kind == api.KindCompactCert {
		stop := a.ensureCertSample(ctx)
		defer stop()
	}
	res := a.runJob(ctx, *job)
	if job.Kind == api.KindPathB {
		a.attachEgressObservation(ctx, &res)
	}
	// Compact-cert band check on the plane needs the scorecard before the
	// job result greens path_b_state. Lab path_b keeps post-then-scorecard.
	if job.Kind == api.KindCompactCert {
		a.reportScorecard(ctx, *job, res.Result)
		if err := a.postResult(ctx, job.JobID, a.state.DeviceID, jobResultBody(job.Kind, res)); err != nil {
			return err
		}
	} else {
		if err := a.postResult(ctx, job.JobID, a.state.DeviceID, jobResultBody(job.Kind, res)); err != nil {
			return err
		}
		a.reportScorecard(ctx, *job, res.Result)
	}
	a.log.Info("posted job result", "job_id", job.JobID, "passed", res.Passed, "reason", res.Reason, "backend", jobBackendLog(*job))
	return nil
}

func (a *Agent) reportScorecard(ctx context.Context, job api.Job, res pathb.Result) {
	if job.Kind != api.KindPathB && job.Kind != api.KindInstanceCheck && job.Kind != api.KindCompactCert {
		return
	}
	if !res.Passed || strings.TrimSpace(res.ImageHash) == "" {
		return
	}
	if !res.Measured {
		a.log.Info("scorecard skipped", "reason", "inference skipped")
		return
	}
	runtimeName := strings.TrimSpace(job.Runtime)
	if runtimeName == "" {
		runtimeName = "nvidia"
	}
	seen := map[string]bool{}
	var ids []string
	if id := strings.TrimSpace(job.CatalogID); id != "" {
		ids = []string{id}
	} else if h := a.currentLease(); h != nil && strings.TrimSpace(h.CatalogID) != "" {
		ids = []string{h.CatalogID}
	} else {
		for _, id := range bench.PackIDs() {
			if bench.LocalImage(id) != "" {
				ids = append(ids, id)
			}
		}
	}
	for _, catalogID := range ids {
		if seen[catalogID] {
			continue
		}
		seen[catalogID] = true
		digest := a.catalogDigest(ctx, catalogID)
		if digest == "" && !res.Measured {
			continue
		}
		report := api.ScorecardReport{
			DeviceID:    a.state.DeviceID,
			CatalogID:   catalogID,
			ImageHash:   res.ImageHash,
			Runtime:     runtimeName,
			PromptPack:  "pack-v1",
			ImageDigest: digest,
			ThermalC:    res.ThermalC,
			Sustained:   false,
		}
		if res.Measured {
			report.SampleMs = res.SampleMs
			report.DecodeTokS = res.DecodeTokS
		}
		if err := a.client.PostScorecard(ctx, report); err != nil {
			a.log.Warn("scorecard post failed", "catalog_id", catalogID, "err", err)
		}
	}
}

func (a *Agent) catalogDigest(ctx context.Context, catalogID string) string {
	if a.digests != nil {
		if digest := strings.TrimSpace(a.digests[catalogID]); digest != "" {
			return digest
		}
	}
	var digest string
	if a.inspectCatalog != nil {
		digest = a.inspectCatalog(ctx, catalogID)
	} else {
		digest = bench.Resolve(ctx, catalogID)
	}
	a.rememberDigest(catalogID, digest)
	return digest
}

func (a *Agent) rememberDigest(catalogID, digest string) {
	digest = strings.TrimSpace(digest)
	if digest == "" {
		return
	}
	if a.digests == nil {
		a.digests = map[string]string{}
	}
	a.digests[catalogID] = digest
}

// ensureCertSample pulls the workload image when it is missing and starts
// that repo@sha256 so path_b can take a timed sample. It does not record
// a lease. A missing image, a missing GGUF, or a completions timeout
// leaves the sample unset. Path B can still pass telem. That pass does
// not tag the catalog.
func (a *Agent) ensureCertSample(ctx context.Context) func() {
	noop := func() {}
	digest := a.catalogDigest(ctx, catalog.Llama318BQ4)
	if digest == "" {
		a.log.Info("cert image not local", "catalog_id", catalog.Llama318BQ4)
		return noop
	}
	if a.currentLease() != nil || a.runtime == nil {
		return noop
	}
	gguf := a.cfg.GGUFHostPath()
	if err := runtime.RequireGGUFFile(gguf); err != nil {
		a.log.Info("cert sample skipped", "reason", "no gguf")
		return noop
	}
	handle, err := a.runtime.Start(ctx, runtime.Spec{
		LeaseID:      certLeaseID,
		CatalogID:    catalog.Llama318BQ4,
		ImageDigest:  digest,
		Runtime:      "nvidia",
		HostPort:     a.cfg.PublishPort(),
		BindAddress:  a.bindIP,
		GGUFPath:     gguf,
		LlamaJinja:   a.cfg.LlamaJinja,
		LlamaCtxSize: a.cfg.LlamaCtxSize,
		LlamaTimeout: a.cfg.LlamaTimeout,
	})
	if err != nil {
		a.log.Info("cert sample skipped", "reason", err.Error())
		return noop
	}
	stop := func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if stopErr := a.runtime.Stop(cctx, handle); stopErr != nil {
			a.log.Warn("cert container stop", "err", stopErr)
		}
	}
	if !a.waitCompletions(ctx) {
		a.log.Info("cert sample skipped", "reason", "completions not ready")
		stop()
		return noop
	}
	return stop
}

func (a *Agent) waitCompletions(ctx context.Context) bool {
	wait := a.certReadyWait
	if wait <= 0 {
		wait = 3 * time.Minute
	}
	deadline := time.Now().Add(wait)
	for {
		if a.pathb.Probe(ctx) {
			return true
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return false
		}
		sleep := a.sleep
		if sleep == nil {
			sleep = time.Sleep
		}
		sleep(2 * time.Second)
	}
}

func (a *Agent) postResult(ctx context.Context, jobID, deviceID string, body api.JobResultRequest) error {
	tries := a.resultTries
	if tries <= 0 {
		tries = defaultResultTries
	}
	wait := a.resultRetryWait
	if wait <= 0 {
		wait = defaultResultRetry
	}
	sleep := a.sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	var last error
	for i := 0; i < tries; i++ {
		last = a.client.PostResult(ctx, jobID, deviceID, body)
		if last == nil {
			return nil
		}
		if i+1 < tries {
			a.log.Warn("job result post failed, retrying", "job_id", jobID, "attempt", i+1, "err", last)
			sleep(wait)
		}
	}
	return last
}

// attachEgressObservation records a host-namespace egress boolean for lab
// path_b soaks. Fail closed when observation is unavailable. Does not scan
// or weaken the jail.
func (a *Agent) attachEgressObservation(ctx context.Context, res *jobOutcome) {
	run := a.dockerRun
	if run == nil && a.runtime != nil {
		run = a.runtime.DockerRun
	}
	ok, reason := runtime.ObserveWorkloadEgress(ctx, run, a.egressSample)
	res.EgressOk = &ok
	res.EgressReason = reason
	a.log.Info("path_b egress", "ok", ok, "reason", reason)
}

func (a *Agent) runJob(ctx context.Context, job api.Job) jobOutcome {
	_ = a.effectiveSell()
	switch job.Kind {
	case api.KindPathB, api.KindInstanceCheck, api.KindCompactCert:
		if res, stub := a.backendStub(job); stub {
			return jobOutcome{Result: res}
		}
		return jobOutcome{Result: a.requireImageHash(job.Kind, a.pathb.Run(ctx, job))}
	case api.KindLabPull:
		return jobOutcome{Result: a.labPull(ctx, job)}
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

// labPull fetches a pinned artifact from an allowlisted source onto the host
// GGUF path (when source_ref is set), then verifies the content hash. Fail
// closed on untrusted URL, hash mismatch, or missing pin. Does not start a
// container — the subsequent lab soak / path_b job runs the jailed sample.
func (a *Agent) labPull(ctx context.Context, job api.Job) pathb.Result {
	pin := strings.TrimSpace(job.ImageDigest)
	if !strings.HasPrefix(strings.ToLower(pin), "sha256:") || len(pin) < len("sha256:")+64 {
		a.log.Info("lab_pull refused", "reason", "pinned hash required", "catalog_id", job.CatalogID)
		return pathb.Result{Passed: false, Reason: "pinned hash required"}
	}
	dest := a.cfg.GGUFHostPath()
	if source := strings.TrimSpace(job.SourceRef); source != "" {
		if err := a.doFetchWeights(ctx, dest, source, pin, job.SizeBytes); err != nil {
			reason := labPullFailReason(err)
			a.log.Info("lab_pull refused", "reason", reason, "catalog_id", job.CatalogID)
			return pathb.Result{Passed: false, Reason: reason}
		}
	}
	hash, err := runtime.HashGGUFFile(dest)
	if err != nil {
		reason := leaseStartFailReason(err)
		a.log.Info("lab_pull refused", "reason", reason, "catalog_id", job.CatalogID)
		return pathb.Result{Passed: false, Reason: reason}
	}
	if !strings.EqualFold(hash, pin) {
		a.log.Info("lab_pull refused", "reason", "artifact hash mismatch", "catalog_id", job.CatalogID)
		return pathb.Result{Passed: false, Reason: "artifact hash mismatch"}
	}
	return pathb.Result{Passed: true, ImageHash: hash}
}

func (a *Agent) doFetchWeights(ctx context.Context, dest, sourceRef, pin string, size int64) error {
	if err := downloadDestOK(dest); err != nil {
		return err
	}
	rawURL, err := pull.TrustedDownloadURL(sourceRef)
	if err != nil {
		return err
	}
	lim := a.pullLimits(size)
	// A test stub must still refuse before it writes. Production ToFileWith
	// HEADs when size_bytes is absent, then preflights, then downloads.
	if a.fetchWeights != nil {
		if err := a.preflightPull(ctx, dest, rawURL, lim); err != nil {
			return err
		}
		return a.fetchWeights(ctx, dest, sourceRef, pin)
	}
	return pull.ToFileWith(ctx, dest, rawURL, pin, pull.HTTPGet(nil), lim)
}

func (a *Agent) pullLimits(size int64) pull.Limits {
	floor, _ := a.cfg.DiskFloorBytes()
	margin, _ := a.cfg.DiskMarginBytes()
	stat := pull.FreeBytes
	if a.statFree != nil {
		stat = a.statFree
	}
	return pull.Limits{
		Expected:  size,
		Floor:     floor,
		Margin:    margin,
		FloorSet:  true,
		MarginSet: true,
		Stat:      stat,
		Head:      a.headLength,
	}
}

// preflightPull resolves size (job payload, else HEAD) and refuses before
// any byte is written. Unknown size fails closed.
func (a *Agent) preflightPull(ctx context.Context, dest, rawURL string, lim pull.Limits) error {
	size, err := pull.ResolveSize(ctx, rawURL, lim.Expected, lim.Head)
	if err != nil || size <= 0 {
		free, statErr := lim.Stat(dest)
		if statErr != nil {
			return &pull.DiskError{StatErr: true}
		}
		return &pull.DiskError{Unknown: true, Free: free}
	}
	lim.Expected = size
	return pull.Preflight(dest, size, lim)
}

func labPullFailReason(err error) string {
	if err == nil {
		return "lab_pull failed"
	}
	var disk *pull.DiskError
	if errors.As(err, &disk) {
		return disk.Error()
	}
	msg := err.Error()
	if strings.HasPrefix(msg, "insufficient_disk:") {
		return msg
	}
	switch {
	case strings.Contains(msg, "untrusted"):
		return "untrusted source"
	case strings.Contains(msg, "hash mismatch"):
		return "artifact hash mismatch"
	case strings.Contains(msg, "pinned hash"):
		return "pinned hash required"
	case strings.Contains(msg, "root filesystem"), strings.Contains(msg, "no download path"):
		return "no GGUF path"
	default:
		return "lab_pull failed"
	}
}

// backendStub fails lease_start / path_b / instance_check on a stub or
// unknown backend. instance_check must not probe or fake a completion.
func (a *Agent) backendStub(job api.Job) (pathb.Result, bool) {
	path, reason := runtime.ResolveBackend(job.Backend)
	if path.Runnable() {
		return pathb.Result{}, false
	}
	a.log.Info("backend stub", "kind", job.Kind, "backend", runtime.LogLabel(job.Backend), "reason", reason)
	return pathb.Result{Passed: false, Reason: reason}, true
}

func (a *Agent) leaseStart(ctx context.Context, job api.Job) pathb.Result {
	if a.cfg.SupervisorHost() {
		a.log.Info("lease_start refused", "reason", SupervisorLeaseRefuse, "host_type", HostTypeSupervisor)
		return pathb.Result{Passed: false, Reason: SupervisorLeaseRefuse}
	}
	if res, stub := a.backendStub(job); stub {
		return res
	}
	if sell := a.effectiveSell(); sell.SellState == api.SellPaused {
		a.log.Info("lease_start refused", "reason", "sell paused", "sell_state", sell.SellState, "until", sell.Until)
		return pathb.Result{Passed: false, Reason: "sell paused"}
	}
	if _, err := runtime.ImageRef(job.ImageDigest); err != nil {
		return pathb.Result{Passed: false, Reason: leaseStartFailReason(err)}
	}
	if runtime.TunnelBindHost(a.bindIP) == "" {
		a.log.Info("lease_start refused", "reason", "no tunnel address", "lease_id", job.LeaseID)
		return pathb.Result{Passed: false, Reason: "no tunnel address"}
	}
	if existing := a.localLease(); existing != nil && strings.TrimSpace(existing.LeaseID) != "" {
		if strings.TrimSpace(existing.LeaseID) != strings.TrimSpace(job.LeaseID) {
			a.log.Info("lease_start refused", "reason", "lease already running", "lease_id", job.LeaseID, "local_lease_id", existing.LeaseID)
			return pathb.Result{Passed: false, Reason: "lease already running"}
		}
		return a.leaseStartAlready(existing)
	}
	gguf := a.cfg.GGUFHostPath()
	hash, err := runtime.HashGGUFFile(gguf)
	if err != nil {
		reason := leaseStartFailReason(err)
		a.log.Info("lease_start refused", "reason", reason, "lease_id", job.LeaseID)
		return pathb.Result{Passed: false, Reason: reason}
	}
	path, _ := runtime.ResolveBackend(job.Backend)
	h, err := a.starterFor(path).Start(ctx, runtime.Spec{
		LeaseID:      job.LeaseID,
		CatalogID:    job.CatalogID,
		ImageDigest:  job.ImageDigest,
		Runtime:      job.Runtime,
		HostPort:     a.cfg.PublishPort(),
		BindAddress:  a.bindIP,
		GGUFPath:     gguf,
		Backend:      job.Backend,
		LlamaJinja:   a.cfg.LlamaJinja,
		LlamaCtxSize: a.cfg.LlamaCtxSize,
		LlamaTimeout: a.cfg.LlamaTimeout,
	})
	if err != nil {
		reason := leaseStartFailReason(err)
		a.log.Info("lease_start failed", "reason", reason, "lease_id", job.LeaseID)
		return pathb.Result{Passed: false, Reason: reason}
	}
	h.FileHash = hash
	if strings.TrimSpace(h.FileHash) == "" {
		a.log.Info("lease_start refused", "reason", "no image_hash", "lease_id", job.LeaseID)
		return pathb.Result{Passed: false, Reason: "no image_hash"}
	}
	a.setLease(h)
	if err := a.saveLease(h); err != nil {
		a.log.Warn("lease record not saved", "err", err)
	}
	a.log.Info("lease_start container", "lease_id", job.LeaseID, "catalog_id", job.CatalogID, "backend", runtime.LogLabel(job.Backend), "runner", runnerLog(path))
	return pathb.Result{Passed: true, ImageHash: hash, Reason: "lease_start"}
}

// leaseStartAlready is a same-lease remint or PostResult retry. Do not
// docker run a second container. Reuse the measured GGUF hash.
func (a *Agent) leaseStartAlready(h *runtime.Handle) pathb.Result {
	hash := strings.TrimSpace(h.FileHash)
	if hash == "" {
		var err error
		hash, err = runtime.HashGGUFFile(a.cfg.GGUFHostPath())
		if err != nil || hash == "" {
			a.log.Info("lease_start refused", "reason", "no image_hash", "lease_id", h.LeaseID)
			return pathb.Result{Passed: false, Reason: "no image_hash"}
		}
		h.FileHash = hash
		a.setLease(h)
		if err := a.saveLease(h); err != nil {
			a.log.Warn("lease record not saved", "err", err)
		}
	}
	a.log.Info("lease_start already running", "lease_id", h.LeaseID)
	return pathb.Result{Passed: true, ImageHash: hash, Reason: "lease_start"}
}

// leaseStartFailReason keeps known sentinels exact. Docker pull/start
// errors append the wrapped Start() string so the journal and Result.Reason
// show unauthorized / nvidia-cdi-hook instead of a bare "runtime start failed".
func leaseStartFailReason(err error) string {
	switch {
	case errors.Is(err, runtime.ErrNoImage):
		return "no image_digest; no soak image"
	case errors.Is(err, runtime.ErrBareDigest):
		return "image_digest is not repo@digest"
	case errors.Is(err, runtime.ErrNoLeaseID):
		return "lease_start requires lease_id"
	case errors.Is(err, runtime.ErrNoCatalogID):
		return "catalog_id required for llama-server alias"
	case errors.Is(err, runtime.ErrNoGGUF):
		return "no GGUF path"
	case errors.Is(err, runtime.ErrGGUFNotFile):
		return "GGUF is not a file"
	default:
		return "runtime start failed: " + err.Error()
	}
}

func jobBackendLog(job api.Job) string {
	return runtime.LogLabel(job.Backend)
}

func runnerLog(path runtime.Path) string {
	switch path {
	case runtime.HIPROCm:
		return runtime.BackendHIPROCm
	case runtime.LlamaCppHost:
		return runtime.MetalRunnerLabel
	default:
		return runtime.NVIDIALogLabel
	}
}

func (a *Agent) starterFor(path runtime.Path) runtime.Starter {
	switch path {
	case runtime.HIPROCm:
		if a.hip != nil {
			return a.hip
		}
		return runtime.NewHIP()
	case runtime.LlamaCppHost:
		if a.metal != nil {
			return a.metal
		}
		return runtime.NewMetal()
	default:
		return a.runtime
	}
}

func (a *Agent) stopHandle(ctx context.Context, h *runtime.Handle) error {
	path, _ := runtime.ResolveBackend("")
	if h != nil && strings.TrimSpace(h.Backend) != "" {
		path, _ = runtime.ResolveBackend(h.Backend)
	}
	return a.starterFor(path).Stop(ctx, h)
}

func (a *Agent) leaseStop(ctx context.Context, job api.Job) pathb.Result {
	h := a.currentLease()
	if h == nil {
		h = a.loadLease()
	}
	if h == nil {
		// Soft no-op: already stopped / no local handle. Do not fail.
		// Prefer an existing hash; do not invent one.
		a.log.Info("lease_stop", "lease_id", job.LeaseID, "reason", "no local handle")
		snap, _ := a.telem.Collect(ctx)
		return pathb.Result{Passed: true, ImageHash: a.preferredImageHash(nil, snap), Reason: "lease_stop"}
	}

	// Strict identity before docker stop/rm. Empty job.lease_id is a
	// mismatch when a handle exists — do not stop whatever is running.
	jobLease := strings.TrimSpace(job.LeaseID)
	localLease := strings.TrimSpace(h.LeaseID)
	if jobLease == "" || jobLease != localLease {
		a.log.Info("lease_stop refused", "lease_id", job.LeaseID, "local_lease_id", h.LeaseID, "reason", "lease_id mismatch")
		return pathb.Result{Passed: false, Reason: "lease_id mismatch"}
	}

	if err := a.stopHandle(ctx, h); err != nil {
		return pathb.Result{Passed: false, Reason: "runtime stop failed"}
	}
	a.setLease(nil)
	_ = a.saveLease(nil)
	// Host WireGuard peer stays up. Teardown only on GET revoked or 204.
	a.log.Info("lease_stop", "lease_id", job.LeaseID)
	snap, _ := a.telem.Collect(ctx)
	return pathb.Result{Passed: true, ImageHash: a.preferredImageHash(h, snap), Reason: "lease_stop"}
}

func (a *Agent) localLease() *runtime.Handle {
	if h := a.currentLease(); h != nil {
		return h
	}
	return a.loadLease()
}

func (a *Agent) leaseImageDigest() string {
	h := a.localLease()
	if h == nil {
		return ""
	}
	return strings.TrimSpace(h.ImageDigest)
}

// requireImageHash enforces the Product lock: path_b / instance_check
// passed=true must POST a non-empty image_hash. Use telem/host hash, else
// the running lease ImageDigest. Do not invent a hash.
func (a *Agent) requireImageHash(kind api.JobKind, res pathb.Result) pathb.Result {
	if !res.Passed {
		return res
	}
	hash := strings.TrimSpace(firstNonEmpty(res.ImageHash, a.cfg.ImageHash, a.leaseImageDigest()))
	if hash == "" {
		a.log.Info(string(kind)+" refused", "reason", "no image_hash")
		return pathb.Result{Passed: false, Reason: "no image_hash"}
	}
	res.ImageHash = hash
	return res
}

func (a *Agent) preferredImageHash(h *runtime.Handle, snap telem.Snapshot) string {
	digest := ""
	if h != nil {
		digest = strings.TrimSpace(h.ImageDigest)
	}
	return firstNonEmpty(digest, a.cfg.ImageHash, snap.ImageHash)
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
