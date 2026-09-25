package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/buildinfo"
	"github.com/FyberLabs/hypermesh-host/internal/metrics"
	"github.com/FyberLabs/hypermesh-host/internal/tunnel"
)

const healthProbeTimeout = 3 * time.Second

// syncAndRecord runs one tunnel sync and stores the outcome for heartbeat
// health. A sync error is still only a warning in Run; it is not fatal.
func (a *Agent) syncAndRecord(ctx context.Context) {
	err := a.syncTunnel(ctx)
	a.recordSync(err)
	if err != nil {
		a.log.Warn("tunnel sync failed", "err", err)
	}
}

func (a *Agent) recordSync(err error) {
	class, status := classifyErr(err)
	a.healthMu.Lock()
	defer a.healthMu.Unlock()
	if a.syncFail == nil {
		a.syncFail = map[string]uint64{}
	}
	if err == nil {
		a.lastSyncAt = a.clock()
		a.haveSync = true
		a.syncClass = ""
		a.syncHTTP = 0
		a.syncOK++
		return
	}
	a.syncClass = class
	a.syncHTTP = status
	a.syncFail[class]++
}

func (a *Agent) recordHeartbeat(err error) {
	class, _ := classifyErr(err)
	a.healthMu.Lock()
	defer a.healthMu.Unlock()
	if a.hbFail == nil {
		a.hbFail = map[string]uint64{}
	}
	if err == nil {
		a.hbOK++
		return
	}
	a.hbFail[class]++
}

// safeHealth builds the heartbeat health object. Collection failures are
// logged and omitted. This never returns an error and must not panic
// into the heartbeat.
func (a *Agent) safeHealth(ctx context.Context) (h *api.HostHealth) {
	h = &api.HostHealth{
		BuildCommit: buildinfo.Current(),
		HostType:    a.cfg.ReportedHostType(),
	}
	defer func() {
		if rec := recover(); rec != nil {
			a.log.Warn("health collection failed", "err", fmt.Sprint(rec))
		}
	}()
	a.fillHealth(ctx, h)
	return h
}

func (a *Agent) fillHealth(ctx context.Context, h *api.HostHealth) {
	at, ok, class, status := a.syncFacts()
	h.LastSyncErrorClass = class
	h.LastSyncHTTPStatus = status
	h.BuildCommit = buildinfo.Current()
	if ok {
		h.LastSyncAt = at.UTC().Format(time.RFC3339)
	}
	probe, cancel := context.WithTimeout(ctx, healthProbeTimeout)
	defer cancel()
	if src, ok := a.tunnels.(interface {
		Snapshot(context.Context) (tunnel.Snapshot, error)
	}); ok && a.tunnels != nil {
		snap, err := src.Snapshot(probe)
		if err != nil {
			a.log.Warn("tunnel health snapshot failed", "err", err)
		} else {
			h.TunnelState = snap.State
			h.HandshakeAgeS = copyInt(snap.HandshakeAgeS)
			h.WGPID = copyInt(snap.PID)
			h.WGPort = copyInt(snap.ListenPort)
		}
	}
	if n, ok := a.leaseCount(probe); ok {
		h.LeaseContainerCount = &n
	}
}

func (a *Agent) syncFacts() (at time.Time, ok bool, class string, status int) {
	a.healthMu.Lock()
	defer a.healthMu.Unlock()
	return a.lastSyncAt, a.haveSync, a.syncClass, a.syncHTTP
}

func (a *Agent) leaseCount(ctx context.Context) (int, bool) {
	if a.countLeases != nil {
		return a.countLeases(ctx)
	}
	return countRunningLeaseContainers(ctx)
}

// countRunningLeaseContainers is `docker ps` of running containers whose
// names use the hypermesh- lease prefix. The names are counted and dropped.
// A docker failure omits the field (ok is false).
func countRunningLeaseContainers(ctx context.Context) (int, bool) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "ps", "--filter", "status=running", "--format", "{{.Names}}")
	out, err := cmd.Output()
	if err != nil {
		return 0, false
	}
	return countLeaseNames(string(out)), true
}

func countLeaseNames(out string) int {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		name = strings.TrimPrefix(name, "/")
		if name == "" || name == "hypermesh-" {
			continue
		}
		if strings.HasPrefix(name, "hypermesh-") {
			n++
		}
	}
	return n
}

func copyInt(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func classifyErr(err error) (class string, status int) {
	if err == nil {
		return "", 0
	}
	var se *api.StatusError
	if errors.As(err, &se) && se != nil {
		return httpClass(se.StatusCode), se.StatusCode
	}
	var de *api.DecodeError
	if errors.As(err, &de) {
		return api.ErrClassDecode, 0
	}
	switch {
	case errors.Is(err, api.ErrUnauthorized), errors.Is(err, api.ErrInvalidEnrollToken):
		return api.ErrClassHTTP4xx, 401
	case errors.Is(err, api.ErrSecretMismatch):
		return api.ErrClassHTTP4xx, 403
	case errors.Is(err, api.ErrUnknownDevice), errors.Is(err, api.ErrUnknownJob), errors.Is(err, api.ErrNoTunnel):
		return api.ErrClassHTTP4xx, 404
	case errors.Is(err, api.ErrTunnelRevoked), errors.Is(err, api.ErrPeerPublicKeyNotRegistered), errors.Is(err, api.ErrEnrollTokenUsed):
		return api.ErrClassHTTP4xx, 409
	}
	if errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return "", 0
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return api.ErrClassTimeout, 0
	}
	var nerr net.Error
	if errors.As(err, &nerr) {
		if nerr.Timeout() {
			return api.ErrClassTimeout, 0
		}
		return api.ErrClassNetwork, 0
	}
	return "", 0
}

func httpClass(code int) string {
	switch {
	case code >= 500 && code <= 599:
		return api.ErrClassHTTP5xx
	case code >= 400 && code <= 499:
		return api.ErrClassHTTP4xx
	default:
		return ""
	}
}

// StartMetrics serves /metrics until ctx is cancelled. A refused address
// or a failed listen is logged. The agent keeps heartbeating either way.
func (a *Agent) StartMetrics(ctx context.Context) {
	addr := strings.TrimSpace(a.cfg.MetricsAddr)
	if addr == "" {
		addr = metrics.DefaultAddr
	}
	if _, err := metrics.Serve(ctx, metrics.Config{
		Addr:             addr,
		AllowNonLoopback: a.cfg.MetricsAllowNonLoopback,
		Log:              a.log,
		Gather:           a.metricsText,
	}); err != nil {
		a.log.Error("metrics endpoint not started", "addr", addr, "err", err)
	}
}

func (a *Agent) metricsText() []byte {
	defer func() {
		if rec := recover(); rec != nil {
			a.log.Warn("metrics render failed", "err", fmt.Sprint(rec))
		}
	}()
	h := a.safeHealth(context.Background())
	a.healthMu.Lock()
	defer a.healthMu.Unlock()
	sample := metrics.Sample{
		HeartbeatSuccess: a.hbOK,
		HeartbeatFailure: a.hbFail,
		SyncSuccess:      a.syncOK,
		SyncFailure:      a.syncFail,
		LastHTTP:         a.syncHTTP,
		ErrClass:         a.syncClass,
		Commit:           buildinfo.Current(),
	}
	if h != nil {
		sample.TunnelState = h.TunnelState
		sample.HandshakeAgeS = h.HandshakeAgeS
		sample.WGPID = h.WGPID
		sample.WGPort = h.WGPort
		sample.LeaseCount = h.LeaseContainerCount
		sample.LastHTTP = h.LastSyncHTTPStatus
		sample.ErrClass = h.LastSyncErrorClass
		if h.BuildCommit != "" {
			sample.Commit = h.BuildCommit
		}
	}
	if a.haveSync {
		sec := a.lastSyncAt.UTC().Unix()
		sample.LastSyncUnix = &sec
	}
	return metrics.Render(sample)
}
