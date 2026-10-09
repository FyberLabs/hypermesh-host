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
	leaseLogLines      = 40
	leaseLogBytes      = 2048
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
	if h == nil || h.Backend == runtime.BackendLlamaCppCPU {
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
	if cur == nil || strings.TrimSpace(cur.LeaseID) != strings.TrimSpace(h.LeaseID) {
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
	client := &http.Client{Timeout: a.probeTimeout(), Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	defer client.CloseIdleConnections()
	started := time.Now()
	deadline := started.Add(timeout)
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
			state, err := a.readWorkload(ctx, h)
			if err == nil && state.Exited() {
				return a.exitedError(ctx, h, state)
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
			}
		}
		if ingressURL != "" {
			if containerURL != "" {
				containerOK = healthReady(ctx, client, containerURL)
			} else if !docker {
				containerOK = true
			}
			ingressOK = healthReady(ctx, client, ingressURL)
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
		if !time.Now().Before(deadline) {
			return a.timeoutError(ctx, h, containerURL != "" && containerOK, ingressOK, containerURL == "" && docker)
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

// abandonStarted stops a container that failed readiness. It returns true
// when stop or rm failed and the handle was kept for a later lease_stop
// or the same lease. A kept handle is not started again in this attempt.
func (a *Agent) abandonStarted(_ context.Context, h *runtime.Handle) bool {
	if h == nil {
		a.stopLeaseForwarder()
		return false
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := a.stopHandle(stopCtx, h)
	a.fwdMu.Lock()
	defer a.fwdMu.Unlock()
	cur := a.currentLease()
	same := cur != nil && strings.TrimSpace(cur.LeaseID) == strings.TrimSpace(h.LeaseID)
	if err != nil {
		if a.log != nil {
			a.log.Warn("lease_start cleanup failed", "lease_id", h.LeaseID, "err", err)
		}
		a.stopLeaseForwarderLocked()
		if cur != nil && !same {
			return false
		}
		a.setLease(h)
		if saveErr := a.saveLease(h); saveErr != nil && a.log != nil {
			a.log.Warn("lease record not saved", "lease_id", h.LeaseID, "err", saveErr)
		}
		return true
	}
	if same {
		a.setLease(nil)
	}
	a.stopLeaseForwarderLocked()
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
