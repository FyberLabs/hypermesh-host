package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/pathb"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

const (
	// defaultLeaseReadyTimeout is how long lease_start waits for
	// llama-server /health. HYPERMESH_LEASE_START_READY_TIMEOUT_S overrides it.
	defaultLeaseReadyTimeout = 600 * time.Second
	// defaultLeaseReadyInterval is how often /health and the container
	// status are checked. A container that exits is reported on the next check.
	defaultLeaseReadyInterval = time.Second
	// defaultLeaseProbeTimeout bounds one /health request.
	defaultLeaseProbeTimeout = time.Second
	// defaultForwarderInterval is how often a dropped WireGuard listener
	// is started again. A failure is logged and does not change heartbeat health.
	defaultForwarderInterval = 5 * time.Second
	// leaseModelAttempts is the first start plus one re-download when the
	// weights file is missing or its hash does not match file_hash.
	leaseModelAttempts = 2
	// leaseDiagReserve is the most the readiness budget sets aside at its
	// end for reading the container log tail. Inspection and /health probes
	// stop before it. The log read ends at the readiness deadline.
	leaseDiagReserve = 2 * time.Second
	leaseLogLines    = 40
	leaseLogBytes    = 2048
)

var (
	leaseSecretToken = regexp.MustCompile(`\bhm_(?:dev|msi|ct)_[A-Za-z0-9]+`)
	leaseBearer      = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-+/=]+`)
	leaseKVSecret    = regexp.MustCompile(`(?i)(token|secret|password|private_key|authorization)\s*[=:]\s*\S+`)
)

type leaseReadyError struct {
	Kind       string
	ExitCode   int
	HaveExit   bool
	Log        string
	LogMissing bool
	Fix        string
}

func (e *leaseReadyError) Error() string {
	if e == nil {
		return "model not ready"
	}
	var b strings.Builder
	b.WriteString(e.Kind)
	if e.HaveExit && e.ExitCode >= 0 {
		fmt.Fprintf(&b, ": exit %d", e.ExitCode)
	}
	if strings.TrimSpace(e.Log) != "" {
		b.WriteString("; log: ")
		b.WriteString(e.Log)
	} else if e.LogMissing {
		b.WriteString("; container log unavailable (log driver none)")
	}
	if e.Fix != "" {
		b.WriteString("; fix ")
		b.WriteString(e.Fix)
	}
	return b.String()
}

func (a *Agent) readyTimeout() time.Duration {
	if a.cfg.LeaseReadyWait > 0 {
		return a.cfg.LeaseReadyWait
	}
	return defaultLeaseReadyTimeout
}

func (a *Agent) readyInterval() time.Duration {
	if a.leaseReadyEvery > 0 {
		return a.leaseReadyEvery
	}
	return defaultLeaseReadyInterval
}

func (a *Agent) probeTimeout() time.Duration {
	if a.leaseProbeTimeout > 0 {
		return a.leaseProbeTimeout
	}
	return defaultLeaseProbeTimeout
}

func (a *Agent) forwarderInterval() time.Duration {
	if a.forwarderEvery > 0 {
		return a.forwarderEvery
	}
	return defaultForwarderInterval
}

func (a *Agent) leaseListenAddr() string {
	host := runtime.TunnelBindHost(a.currentBindIP())
	if host == "" {
		return ""
	}
	return net.JoinHostPort(host, a.cfg.PublishPort())
}

func (a *Agent) bridgeAddr(ip string) string {
	if a.bridgeDial != nil {
		return a.bridgeDial(ip)
	}
	return net.JoinHostPort(ip, runtime.DefaultListenPort)
}

func (a *Agent) containerHealthURL(ip string) string {
	if a.containerHealth != nil {
		return a.containerHealth(ip)
	}
	return "http://" + a.bridgeAddr(ip) + "/health"
}

func leaseHealthURL(addr string) string {
	return "http://" + addr + "/health"
}

func (a *Agent) forwarderLoop(ctx context.Context) {
	a.ensureLeaseForwarder(ctx)
	ticker := time.NewTicker(a.forwarderInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			a.stopLeaseForwarder()
			return
		case <-ticker.C:
			a.ensureLeaseForwarder(ctx)
		}
	}
}

// ensureLeaseForwarder keeps the WireGuard listener on the current tunnel
// address for a running lease. A live listener on a previous client_address
// is replaced. It logs the failure. It does not record a heartbeat or
// tunnel health failure.
func (a *Agent) ensureLeaseForwarder(ctx context.Context) {
	if a.leaseReady != nil {
		return
	}
	h := a.currentLease()
	if h == nil || !h.Serving() || h.Backend == runtime.BackendLlamaCppCPU {
		return
	}
	listen := a.leaseListenAddr()
	if listen == "" {
		a.log.Warn("lease forwarder not restarted", "reason", "no tunnel address", "lease_id", h.LeaseID)
		return
	}
	ip := strings.TrimSpace(h.ContainerIP)
	state, err := a.readWorkload(ctx, h)
	if err == nil && state.Exited() {
		a.log.Warn("lease forwarder not restarted", "reason", "container exited", "lease_id", h.LeaseID)
		return
	}
	if err == nil && state.IP != "" {
		ip = state.IP
	}
	if ip == "" {
		a.log.Warn("lease forwarder not restarted", "reason", "container address unknown", "lease_id", h.LeaseID)
		return
	}
	dial := a.bridgeAddr(ip)
	a.fwdMu.Lock()
	defer a.fwdMu.Unlock()
	cur := a.currentLease()
	if cur == nil || !cur.Serving() || strings.TrimSpace(cur.LeaseID) != strings.TrimSpace(h.LeaseID) {
		return
	}
	if a.forwarderCurrentLocked(listen, dial) {
		return
	}
	if err := a.startLeaseForwarderLocked(listen, dial); err != nil {
		a.log.Warn("lease forwarder not restarted", "err", err, "lease_id", h.LeaseID)
		return
	}
	cur.ContainerIP = ip
	cur.PublishAddr = listen
	a.log.Info("lease forwarder listening", "lease_id", cur.LeaseID, "listen", listen)
}

func (a *Agent) dockerRunnerFor(h *runtime.Handle) runtime.DockerRunner {
	path, _ := runtime.ResolveBackend("")
	if h != nil && strings.TrimSpace(h.Backend) != "" {
		path, _ = runtime.ResolveBackend(h.Backend)
	}
	switch v := a.starterFor(path).(type) {
	case *runtime.NVIDIA:
		return v.DockerRun
	case *runtime.HIP:
		return v.DockerRun
	default:
		return a.dockerRun
	}
}

func (a *Agent) readWorkload(ctx context.Context, h *runtime.Handle) (runtime.WorkloadState, error) {
	if h == nil {
		return runtime.WorkloadState{}, errors.New("workload container unavailable")
	}
	id := strings.TrimSpace(h.ContainerID)
	if id == "" {
		id = strings.TrimSpace(h.Name)
	}
	if a.workloadState != nil {
		return a.workloadState(ctx, id)
	}
	return runtime.InspectWorkloadState(ctx, a.dockerRunnerFor(h), id)
}

func (a *Agent) openLeaseIngress(ctx context.Context, h *runtime.Handle, path runtime.Path) error {
	listen := a.leaseListenAddr()
	if listen == "" {
		return &leaseReadyError{Kind: "ingress unreachable", Fix: "WireGuard client address or HYPERMESH_LISTEN_PORT"}
	}
	if path == runtime.LlamaCppHost {
		return a.waitLeaseHealth(ctx, listen, h, false)
	}
	err := a.waitLeaseHealth(ctx, listen, h, true)
	if err != nil {
		a.stopLeaseForwarder()
		return err
	}
	return nil
}

func (a *Agent) waitLeaseHealth(ctx context.Context, listen string, h *runtime.Handle, docker bool) error {
	timeout := a.readyTimeout()
	interval := a.readyInterval()
	// The workload controls /health. A redirect is not a healthy answer and
	// is never followed, so the privileged agent cannot be sent to another host.
	client := &http.Client{
		Timeout:       a.probeTimeout(),
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	started := time.Now()
	// The whole wait, including docker inspect and the log tail read, ends
	// at deadline. Probes stop at probeDeadline so the log tail still has a
	// slice of the budget when the model never becomes ready.
	deadline := started.Add(timeout)
	reserve := leaseDiagReserve
	if limit := timeout / 5; reserve > limit {
		reserve = limit
	}
	probeDeadline := deadline.Add(-reserve)
	probeCtx, cancelProbe := context.WithDeadline(ctx, probeDeadline)
	defer cancelProbe()
	diagCtx, cancelDiag := context.WithDeadline(ctx, deadline)
	defer cancelDiag()
	var containerURL, ingressURL string
	if !docker {
		ingressURL = leaseHealthURL(listen)
	}
	containerOK := false
	ingressOK := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if docker && h != nil {
			state, err := a.readWorkload(probeCtx, h)
			if err == nil && state.Exited() {
				return a.exitedError(diagCtx, h, state)
			}
			if err == nil && state.Running && state.IP != "" && containerURL == "" {
				if err := a.startLeaseForwarder(listen, a.bridgeAddr(state.IP)); err != nil {
					return &leaseReadyError{
						Kind: "ingress unreachable",
						Fix:  "WireGuard client address or HYPERMESH_LISTEN_PORT",
						Log:  redactLeaseText(err.Error()),
					}
				}
				h.ContainerIP = state.IP
				h.PublishAddr = listen
				containerURL = a.containerHealthURL(state.IP)
				ingressURL = leaseHealthURL(listen)
				a.log.Info("lease forwarder listening", "lease_id", h.LeaseID, "listen", listen)
			} else if containerURL != "" {
				// A tunnel sync can change client_address while the model loads.
				// Listen on the current address and probe it, not the one
				// captured when the container first answered. A failed rebind
				// is retried on the next check until the deadline.
				if want := a.leaseListenAddr(); want != "" && want != listen {
					if err := a.startLeaseForwarder(want, a.bridgeAddr(h.ContainerIP)); err != nil {
						a.log.Warn("lease forwarder not rebound", "lease_id", h.LeaseID, "listen", want, "err", err)
					} else {
						listen = want
						h.PublishAddr = listen
						ingressURL = leaseHealthURL(listen)
						a.log.Info("lease forwarder rebound", "lease_id", h.LeaseID, "listen", listen)
					}
				}
			}
		}
		if ingressURL != "" && probeCtx.Err() == nil {
			if containerURL != "" {
				containerOK = healthReady(probeCtx, client, containerURL)
			} else if !docker {
				containerOK = true
			}
			ingressOK = healthReady(probeCtx, client, ingressURL)
			if containerOK && ingressOK {
				load := time.Since(started).Seconds()
				leaseID := ""
				if h != nil {
					leaseID = h.LeaseID
				}
				a.log.Info("lease_start ready", "lease_id", leaseID, "load_s", load)
				return nil
			}
		}
		if !time.Now().Before(probeDeadline) {
			if err := ctx.Err(); err != nil {
				return err
			}
			return a.timeoutError(diagCtx, h, containerURL != "" && containerOK, ingressOK, containerURL == "" && docker)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func healthReady(ctx context.Context, client *http.Client, rawURL string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode == http.StatusOK
}

func (a *Agent) timeoutError(ctx context.Context, h *runtime.Handle, containerOK, ingressOK, noAddress bool) error {
	kind := "model not ready"
	fix := "HYPERMESH_LEASE_START_READY_TIMEOUT_S"
	if noAddress {
		kind = "ingress unreachable"
		fix = "hypermesh-workload container address"
	} else if containerOK && !ingressOK {
		kind = "ingress unreachable"
		fix = "WireGuard client address or HYPERMESH_LISTEN_PORT"
	}
	logTail, missing := a.workloadLogTail(ctx, h)
	if strings.Contains(strings.ToLower(logTail), "no gguf") {
		fix = "HYPERMESH_GGUF_PATH"
	}
	return &leaseReadyError{Kind: kind, Log: logTail, LogMissing: missing && logTail == "", Fix: fix}
}

func (a *Agent) exitedError(ctx context.Context, h *runtime.Handle, state runtime.WorkloadState) error {
	logTail, missing := a.workloadLogTail(ctx, h)
	fix := ""
	low := strings.ToLower(logTail)
	if strings.Contains(low, "no gguf") || strings.Contains(low, "/models/model.gguf") {
		fix = "HYPERMESH_GGUF_PATH"
	}
	return &leaseReadyError{
		Kind:       "container exited",
		ExitCode:   state.ExitCode,
		HaveExit:   state.ExitCode >= 0,
		Log:        logTail,
		LogMissing: missing && logTail == "",
		Fix:        fix,
	}
}

func (a *Agent) workloadLogTail(ctx context.Context, h *runtime.Handle) (string, bool) {
	if h == nil {
		return "", false
	}
	run := a.dockerRunnerFor(h)
	if run == nil {
		return "", true
	}
	id := strings.TrimSpace(h.ContainerID)
	if id == "" {
		id = strings.TrimSpace(h.Name)
	}
	if id == "" {
		return "", true
	}
	out, err := run(ctx, "logs", "--tail", fmt.Sprintf("%d", leaseLogLines), id)
	if err != nil {
		return "", true
	}
	text := redactLeaseText(string(out))
	if text == "" {
		return "", true
	}
	return text, false
}

func redactLeaseText(s string) string {
	s = leaseSecretToken.ReplaceAllString(s, "[redacted]")
	s = leaseBearer.ReplaceAllString(s, "Bearer [redacted]")
	s = leaseKVSecret.ReplaceAllString(s, "$1=[redacted]")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > leaseLogLines {
		lines = lines[len(lines)-leaseLogLines:]
	}
	s = strings.TrimSpace(strings.Join(lines, "\n"))
	if len(s) > leaseLogBytes {
		s = s[len(s)-leaseLogBytes:]
	}
	return s
}

// recordStarting makes a started container visible in memory and in
// lease.json before the readiness wait. The published handle is a copy in
// the starting state, so the caller keeps its own handle to update.
func (a *Agent) recordStarting(h *runtime.Handle) {
	rec := *h
	rec.State = runtime.HandleStarting
	a.fwdMu.Lock()
	a.setLease(&rec)
	a.fwdMu.Unlock()
	if err := a.saveLease(&rec); err != nil && a.log != nil {
		a.log.Warn("lease record not saved", "lease_id", h.LeaseID, "err", err)
	}
}

// markServing publishes h as the running lease once readiness passed. It
// does so under the forwarder lock and only while the starting record this
// start made is still the current lease. A lease_stop that overlapped the
// end of the wait has already removed the container and the record, and
// must not be undone. It returns false in that case and writes nothing.
func (a *Agent) markServing(h *runtime.Handle) bool {
	a.fwdMu.Lock()
	defer a.fwdMu.Unlock()
	cur := a.currentLease()
	if cur == nil || cur.State != runtime.HandleStarting ||
		strings.TrimSpace(cur.LeaseID) != strings.TrimSpace(h.LeaseID) ||
		strings.TrimSpace(cur.ContainerID) != strings.TrimSpace(h.ContainerID) {
		return false
	}
	h.State = ""
	a.setLease(h)
	if err := a.saveLease(h); err != nil && a.log != nil {
		a.log.Warn("lease record not saved", "err", err)
	}
	return true
}

// recoverUnreadyLease stops the container of a lease that was recorded
// but never became ready, or whose earlier cleanup failed. It returns ok
// when the container and record are gone and a new start may continue.
func (a *Agent) recoverUnreadyLease(h *runtime.Handle) (pathb.Result, bool) {
	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := a.stopHandle(stopCtx, h); err != nil {
		if a.log != nil {
			a.log.Warn("lease_start cleanup of earlier attempt failed", "lease_id", h.LeaseID, "err", err)
		}
		a.markCleanupFailed(h)
		return pathb.Result{Passed: false, Reason: "earlier lease_start container could not be removed; fix docker stop and rm for " + strings.TrimSpace(h.Name)}, false
	}
	a.dropCurrentLease()
	_ = a.saveLease(nil)
	return pathb.Result{}, true
}

// markCleanupFailed keeps the handle, in the cleanup_failed state, so a
// later lease_stop or lease_start can remove the container. The handle
// never counts as a serving lease.
func (a *Agent) markCleanupFailed(h *runtime.Handle) {
	rec := *h
	rec.State = runtime.HandleCleanupFailed
	a.fwdMu.Lock()
	a.stopLeaseForwarderLocked()
	a.setLease(&rec)
	a.fwdMu.Unlock()
	if err := a.saveLease(&rec); err != nil && a.log != nil {
		a.log.Warn("lease record not saved", "lease_id", h.LeaseID, "err", err)
	}
}

// abandonStarted stops a container that failed readiness. It returns true
// when stop or rm failed and the handle was kept, in the cleanup_failed
// state, for a later lease_stop or lease_start. A kept handle is not
// started again in this attempt and never passes readiness.
func (a *Agent) abandonStarted(_ context.Context, h *runtime.Handle) bool {
	if h == nil {
		a.stopLeaseForwarder()
		return false
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := a.stopHandle(stopCtx, h)
	if err != nil {
		if a.log != nil {
			a.log.Warn("lease_start cleanup failed", "lease_id", h.LeaseID, "err", err)
		}
		a.markCleanupFailed(h)
		return true
	}
	a.fwdMu.Lock()
	cur := a.currentLease()
	same := cur != nil && strings.TrimSpace(cur.LeaseID) == strings.TrimSpace(h.LeaseID)
	if same {
		a.setLease(nil)
	}
	a.stopLeaseForwarderLocked()
	a.fwdMu.Unlock()
	if same {
		_ = a.saveLease(nil)
	}
	return false
}

func modelRepairable(job api.Job, reason string) bool {
	if strings.TrimSpace(job.SourceRef) == "" {
		return false
	}
	if _, ok := leaseExpectedHash(job); !ok && !strings.HasPrefix(job.SourceRef, privateModelPrefix) {
		return false
	}
	switch {
	case reason == "artifact hash mismatch":
		return true
	case reason == "pending model missing":
		return true
	case reason == "no GGUF path" || strings.HasPrefix(reason, "no GGUF path"):
		return true
	case reason == "GGUF is not a file" || strings.Contains(reason, "GGUF is not a file"):
		return true
	case strings.Contains(strings.ToLower(reason), "hash mismatch"):
		return true
	default:
		return false
	}
}

func (a *Agent) modelStillBroken(job api.Job, gguf string) bool {
	if strings.TrimSpace(job.SourceRef) == "" {
		return false
	}
	if _, ok := leaseExpectedHash(job); !ok && !strings.HasPrefix(job.SourceRef, privateModelPrefix) {
		return false
	}
	if err := runtime.RequireGGUFFile(gguf); err != nil {
		return true
	}
	hash, err := runtime.HashGGUFFile(gguf)
	if err != nil {
		return true
	}
	if expected, ok := leaseExpectedHash(job); ok && !strings.EqualFold(hash, expected) {
		return true
	}
	return false
}

func finishRepairFailure(res pathb.Result) pathb.Result {
	if strings.Contains(res.Reason, "fix ") {
		return res
	}
	fix := "file_hash"
	low := strings.ToLower(res.Reason)
	if strings.Contains(low, "gguf") || strings.Contains(res.Reason, "pending model") {
		fix = "HYPERMESH_GGUF_PATH"
	}
	res.Reason += "; fix " + fix
	return res
}
