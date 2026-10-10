package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/pathb"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

func TestLeaseReadyTimeoutFromEnv(t *testing.T) {
	t.Setenv("HYPERMESH_LEASE_START_READY_TIMEOUT_S", "42")
	cfg := FromEnv()
	if cfg.LeaseReadyWait != 42*time.Second {
		t.Fatalf("timeout %s", cfg.LeaseReadyWait)
	}
	t.Setenv("HYPERMESH_LEASE_START_READY_TIMEOUT_S", "0")
	cfg = FromEnv()
	cfg.TenantID = testTenant
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "HYPERMESH_LEASE_START_READY_TIMEOUT_S") {
		t.Fatal(err)
	}
}

func TestLeaseStartHealthyForwarderAndRemint(t *testing.T) {
	a, hash, listen, hits := newIngressAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	docker := &scriptedDocker{ip: "172.18.0.2", logsErr: true}
	a.runtime = &runtime.NVIDIA{Run: docker.run}
	var logs strings.Builder
	a.log = slog.New(slog.NewTextHandler(&logs, nil))

	got := a.leaseStart(context.Background(), ingressJob("lease-ready", hash))
	if !got.Passed || got.Reason != "lease_start" || got.ImageHash != hash {
		t.Fatalf("%+v", got)
	}
	if hits.Load() < 2 {
		t.Fatalf("health hits %d, want container and forwarded path", hits.Load())
	}
	if !strings.Contains(logs.String(), "load_s") {
		t.Fatalf("load time not logged: %s", logs.String())
	}
	resp, err := http.Get("http://" + listen + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("forwarded health %d", resp.StatusCode)
	}
	before := hits.Load()
	again := a.leaseStart(context.Background(), ingressJob("lease-ready", hash))
	if !again.Passed || again.Reason != "lease_start" || hits.Load() != before || docker.runs != 1 {
		t.Fatalf("remint %+v hits %d->%d runs %d", again, before, hits.Load(), docker.runs)
	}

	stopped := a.leaseStop(context.Background(), api.Job{Kind: api.KindLeaseStop, LeaseID: "lease-ready"})
	if !stopped.Passed {
		t.Fatalf("stop %+v", stopped)
	}
	if a.currentLease() != nil || a.forwarderAlive() {
		t.Fatal("forwarder still up after stop")
	}
	if conn, err := net.DialTimeout("tcp", listen, 200*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("listener still accepting")
	}
	if !docker.saw("stop") || !docker.saw("rm") {
		t.Fatalf("cleanup calls %v", docker.arg0())
	}
}

func TestLeaseStartContainerExitDuringWait(t *testing.T) {
	a, hash, listen, _ := newIngressAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	a.cfg.LeaseReadyWait = 2 * time.Second
	docker := &scriptedDocker{
		ip:        "172.18.0.2",
		exitAfter: 2,
		logs:      "No GGUF at /models/model.gguf\ntoken=hm_dev_supersecret\n",
	}
	a.runtime = &runtime.NVIDIA{Run: docker.run}
	start := time.Now()
	got := a.leaseStart(context.Background(), ingressJob("lease-exit", hash))
	if got.Passed || time.Since(start) > time.Second {
		t.Fatalf("exit not reported quickly: %+v after %s", got, time.Since(start))
	}
	if !strings.Contains(got.Reason, "container exited") || !strings.Contains(got.Reason, "exit 1") {
		t.Fatalf("reason %q", got.Reason)
	}
	if !strings.Contains(got.Reason, "No GGUF") || !strings.Contains(got.Reason, "fix HYPERMESH_GGUF_PATH") {
		t.Fatalf("reason %q", got.Reason)
	}
	if strings.Contains(got.Reason, "hm_dev_supersecret") {
		t.Fatalf("secret in reason %q", got.Reason)
	}
	assertLeaseClean(t, a, listen, docker)
}

func TestLeaseStartTimeoutCleansUpAndRetryWorks(t *testing.T) {
	var ready atomic.Bool
	a, hash, listen, _ := newIngressAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	a.cfg.LeaseReadyWait = 250 * time.Millisecond
	docker := &scriptedDocker{ip: "172.18.0.2", logsErr: true}
	a.runtime = &runtime.NVIDIA{Run: docker.run}
	got := a.leaseStart(context.Background(), ingressJob("lease-timeout", hash))
	if got.Passed || !strings.Contains(got.Reason, "model not ready") || !strings.Contains(got.Reason, "HYPERMESH_LEASE_START_READY_TIMEOUT_S") {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got.Reason, "container log unavailable (log driver none)") {
		t.Fatalf("reason %q", got.Reason)
	}
	assertLeaseClean(t, a, listen, docker)
	ready.Store(true)
	docker.logsErr = false
	again := a.leaseStart(context.Background(), ingressJob("lease-timeout", hash))
	if !again.Passed || docker.runs != 2 {
		t.Fatalf("retry %+v runs %d", again, docker.runs)
	}
	a.stopLeaseForwarder()
}

func TestLeaseStartUnreachableIngress(t *testing.T) {
	a, hash, listen, _ := newIngressAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	a.cfg.LeaseReadyWait = 400 * time.Millisecond
	// Direct /health answers. The forwarder dials a closed port.
	direct := a.bridgeDial
	a.containerHealth = func(string) string { return "http://" + direct("") + "/health" }
	a.bridgeDial = func(string) string { return "127.0.0.1:1" }
	docker := &scriptedDocker{ip: "172.18.0.2", logsErr: true}
	a.runtime = &runtime.NVIDIA{Run: docker.run}
	got := a.leaseStart(context.Background(), ingressJob("lease-ingress", hash))
	if got.Passed || !strings.Contains(got.Reason, "ingress unreachable") || !strings.Contains(got.Reason, "HYPERMESH_LISTEN_PORT") {
		t.Fatalf("%+v", got)
	}
	assertLeaseClean(t, a, listen, docker)
}

func TestLeaseStartRedownloadsMissingModelThenStarts(t *testing.T) {
	a, cache := newCacheAgent(t)
	a.leaseReady = nil
	a.ingressIface = "lo"
	a.modelFit = func(context.Context, int64) error { return nil }
	port := freeLocalPort(t)
	a.cfg.ListenPort = port
	a.bindIP = "127.0.0.1/32"
	a.cfg.LeaseReadyWait = time.Second
	a.leaseReadyEvery = 15 * time.Millisecond
	a.leaseProbeTimeout = 200 * time.Millisecond
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			hits.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	a.bridgeDial = func(string) string { return strings.TrimPrefix(srv.URL, "http://") }
	body := bytes.Repeat([]byte{'m'}, 100)
	pin := shaPin(body)
	var fetches atomic.Int32
	a.fetchWeights = func(_ context.Context, dest, _, _ string) error {
		fetches.Add(1)
		return os.WriteFile(dest, body, 0o600)
	}
	docker := &scriptedDocker{ip: "172.18.0.2"}
	docker.onInspect = func(d *scriptedDocker) {
		if d.runs != 1 {
			return
		}
		d.status = "exited"
		d.exitCode = 1
		_ = filepath.Walk(filepath.Dir(cache), func(path string, info os.FileInfo, err error) error {
			if err == nil && strings.HasSuffix(path, ".gguf") {
				_ = os.Remove(path)
			}
			return nil
		})
	}
	a.runtime = &runtime.NVIDIA{Run: docker.run}
	job := ingressJob("lease-redl", pin)
	job.SourceRef = "bartowski/model@" + strings.Repeat("b", 40) + "/model.gguf"
	job.SizeBytes = 100
	job.CatalogID = "selected"
	got := a.leaseStart(context.Background(), job)
	if !got.Passed || got.ImageHash != pin || fetches.Load() != 2 || docker.runs != 2 {
		t.Fatalf("result=%+v fetches=%d runs=%d", got, fetches.Load(), docker.runs)
	}
	if hits.Load() < 2 {
		t.Fatalf("health hits %d", hits.Load())
	}
	a.stopLeaseForwarder()
}

func TestLeaseForwarderRestartsWithoutHealthChange(t *testing.T) {
	a, hash, listen, _ := newIngressAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	docker := &scriptedDocker{ip: "172.18.0.2"}
	a.runtime = &runtime.NVIDIA{Run: docker.run}
	if got := a.leaseStart(context.Background(), ingressJob("lease-fwd", hash)); !got.Passed {
		t.Fatal(got)
	}
	a.stopLeaseForwarder()
	if a.forwarderAlive() {
		t.Fatal("listener stayed up")
	}
	a.ensureLeaseForwarder(context.Background())
	if !a.forwarderAlive() {
		t.Fatal("listener was not restarted")
	}
	if a.hbFail != nil || a.syncFail != nil {
		t.Fatalf("health changed hb=%v sync=%v", a.hbFail, a.syncFail)
	}
	conn, err := net.DialTimeout("tcp", listen, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	a.stopLeaseForwarder()
}

func TestEnsureForwarderRebindsTunnelAddress(t *testing.T) {
	a, hash, listen, hits := newIngressAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	})
	docker := &scriptedDocker{ip: "172.18.0.2"}
	a.runtime = &runtime.NVIDIA{Run: docker.run}
	if got := a.leaseStart(context.Background(), ingressJob("lease-rebind", hash)); !got.Passed {
		t.Fatal(got)
	}
	if !a.forwarderAlive() {
		t.Fatal("listener was not up")
	}
	h := a.currentLease()
	if h == nil {
		t.Fatal("missing lease")
	}
	h.PublishAddr = listen
	next := freeLocalPort(t)
	a.cfg.ListenPort = next
	want := net.JoinHostPort("127.0.0.1", next)
	a.ensureLeaseForwarder(context.Background())
	if a.currentLease().PublishAddr != want {
		t.Fatalf("publish %q want %s", a.currentLease().PublishAddr, want)
	}
	if conn, err := net.DialTimeout("tcp", listen, 200*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("old tunnel address still accepting")
	}
	resp, err := http.Get("http://" + want + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || hits.Load() < 3 {
		t.Fatalf("rebound health %d hits %d", resp.StatusCode, hits.Load())
	}
	a.stopLeaseForwarder()
}

func TestLeaseStartCleanupFailureKeepsHandle(t *testing.T) {
	a, hash, listen, _ := newIngressAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	a.cfg.LeaseReadyWait = 200 * time.Millisecond
	a.modelFit = func(context.Context, int64) error { return nil }
	body, err := os.ReadFile(a.cfg.GGUFPath)
	if err != nil {
		t.Fatal(err)
	}
	var fetches atomic.Int32
	a.fetchWeights = func(_ context.Context, dest, _, _ string) error {
		fetches.Add(1)
		return os.WriteFile(dest, body, 0o600)
	}
	docker := &scriptedDocker{ip: "172.18.0.2", logsErr: true, stopErr: errors.New("stop timed out")}
	docker.onInspect = func(d *scriptedDocker) {
		if d.runs != 1 {
			return
		}
		_ = filepath.Walk(filepath.Dir(a.cfg.GGUFPath), func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.HasSuffix(path, ".gguf") {
				_ = os.Remove(path)
			}
			return nil
		})
	}
	a.runtime = &runtime.NVIDIA{Run: docker.run}
	job := ingressJob("lease-keep", hash)
	job.SourceRef = "bartowski/model@" + strings.Repeat("b", 40) + "/model.gguf"
	job.SizeBytes = int64(len(body))
	got := a.leaseStart(context.Background(), job)
	if got.Passed || docker.runs != 1 || fetches.Load() != 1 {
		t.Fatalf("result=%+v runs=%d fetches=%d", got, docker.runs, fetches.Load())
	}
	h := a.currentLease()
	if h == nil || h.LeaseID != "lease-keep" || strings.TrimSpace(h.ContainerID) == "" {
		t.Fatalf("handle %+v", h)
	}
	raw, err := os.ReadFile(filepath.Join(a.cfg.StateDir, leaseFile))
	if err != nil || !strings.Contains(string(raw), "lease-keep") || !strings.Contains(string(raw), h.ContainerID) {
		t.Fatalf("lease file err=%v body=%s", err, raw)
	}
	if h.Serving() || h.State != runtime.HandleCleanupFailed || !strings.Contains(string(raw), `"state": "cleanup_failed"`) {
		t.Fatalf("kept handle state %q, record %s", h.State, raw)
	}
	// A handle whose cleanup failed never passes readiness: the retry must
	// not post passed=true, must not start a second container, and keeps the
	// handle for a later stop.
	again := a.leaseStart(context.Background(), job)
	if again.Passed || docker.runs != 1 || a.currentLease() == nil || a.currentLease().Serving() {
		t.Fatalf("retry %+v runs %d lease %+v", again, docker.runs, a.currentLease())
	}
	a.ensureLeaseForwarder(context.Background())
	if a.forwarderAlive() {
		t.Fatal("listener started for a handle that is not serving")
	}
	docker.mu.Lock()
	docker.stopErr = nil
	docker.mu.Unlock()
	stopped := a.leaseStop(context.Background(), api.Job{Kind: api.KindLeaseStop, LeaseID: "lease-keep"})
	if !stopped.Passed {
		t.Fatalf("stop %+v", stopped)
	}
	assertLeaseClean(t, a, listen, docker)
}

func TestEnsureForwarderDoesNotOutliveLeaseStop(t *testing.T) {
	a, hash, listen, _ := newIngressAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	})
	docker := &scriptedDocker{ip: "172.18.0.2"}
	a.runtime = &runtime.NVIDIA{Run: docker.run}
	if got := a.leaseStart(context.Background(), ingressJob("lease-race", hash)); !got.Passed {
		t.Fatal(got)
	}
	a.stopLeaseForwarder()
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	started := make(chan struct{})
	var startOnce sync.Once
	a.workloadState = func(context.Context, string) (runtime.WorkloadState, error) {
		startOnce.Do(func() { close(started) })
		<-release
		return runtime.WorkloadState{Running: true, IP: "172.18.0.2"}, nil
	}
	done := make(chan struct{})
	go func() {
		a.ensureLeaseForwarder(context.Background())
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("forwarder restart did not reach inspect")
	}
	stopped := a.leaseStop(context.Background(), api.Job{Kind: api.KindLeaseStop, LeaseID: "lease-race"})
	releaseOnce.Do(func() { close(release) })
	if !stopped.Passed {
		t.Fatalf("stop %+v", stopped)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("forwarder restart did not finish")
	}
	if a.currentLease() != nil || a.forwarderAlive() {
		t.Fatal("listener or lease survived lease_stop")
	}
	if conn, err := net.DialTimeout("tcp", listen, 200*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("listener still accepting after lease_stop")
	}
}

func TestRedactLeaseTextBoundsAndSecrets(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	b.WriteString("authorization: Bearer hm_dev_secretvalue\n")
	out := redactLeaseText(b.String())
	if strings.Contains(out, "hm_dev_secretvalue") || strings.Contains(out, "line 0\n") {
		t.Fatalf("not bounded or not redacted: %q", out)
	}
	if !strings.Contains(out, "[redacted]") || !strings.Contains(out, "line 49") {
		t.Fatalf("tail missing: %q", out)
	}
}

type scriptedDocker struct {
	mu        sync.Mutex
	calls     [][]string
	status    string
	exitCode  int
	ip        string
	logs      string
	logsErr   bool
	removed   bool
	runs      int
	inspects  int
	exitAfter int
	stopErr   error
	onInspect func(*scriptedDocker)
}

func (d *scriptedDocker) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, append([]string{}, args...))
	if len(args) == 0 {
		return []byte("ok"), nil
	}
	switch args[0] {
	case "pull", "network":
		return []byte("ok"), nil
	case "run":
		d.runs++
		d.inspects = 0
		d.removed = false
		d.status = "running"
		d.exitCode = 0
		return []byte("cid-lease\n"), nil
	case "inspect":
		if d.removed {
			return []byte("no such container"), errors.New("exit status 1")
		}
		d.inspects++
		if d.onInspect != nil {
			d.onInspect(d)
		}
		if d.exitAfter > 0 && d.inspects >= d.exitAfter {
			d.status = "exited"
			d.exitCode = 1
		}
		running := d.status == "running"
		body := fmt.Sprintf(`{"State":{"Running":%t,"Status":%q,"ExitCode":%d},"NetworkSettings":{"Networks":{"hypermesh-workload":{"IPAddress":%q}}}}`,
			running, d.status, d.exitCode, d.ip)
		return []byte(body), nil
	case "logs":
		if d.logsErr {
			return []byte("configured logging driver does not support reading"), errors.New("exit status 1")
		}
		return []byte(d.logs), nil
	case "stop":
		if d.stopErr != nil {
			return []byte("stop failed"), d.stopErr
		}
		d.removed = true
		d.status = "exited"
		return []byte("cid-lease"), nil
	case "rm":
		d.removed = true
		d.status = "exited"
		return []byte("cid-lease"), nil
	default:
		return []byte("ok"), nil
	}
}

func (d *scriptedDocker) saw(cmd string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, args := range d.calls {
		if len(args) > 0 && args[0] == cmd {
			return true
		}
	}
	return false
}

func (d *scriptedDocker) arg0() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, args := range d.calls {
		if len(args) > 0 {
			out = append(out, args[0])
		}
	}
	return out
}

func newIngressAgent(t *testing.T, handler http.HandlerFunc) (*Agent, string, string, *atomic.Int32) {
	t.Helper()
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.leaseReady = nil
	// Tests listen on loopback, so the device bind is to lo.
	a.ingressIface = "lo"
	port := freeLocalPort(t)
	a.cfg.ListenPort = port
	a.bindIP = "127.0.0.1/32"
	a.cfg.LeaseReadyWait = 800 * time.Millisecond
	a.leaseReadyEvery = 15 * time.Millisecond
	a.leaseProbeTimeout = 200 * time.Millisecond
	path, hash := writeTestGGUF(t, dir)
	a.cfg.GGUFPath = path
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			hits.Add(1)
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	a.bridgeDial = func(string) string { return strings.TrimPrefix(srv.URL, "http://") }
	return a, hash, net.JoinHostPort("127.0.0.1", port), &hits
}

func ingressJob(leaseID, hash string) api.Job {
	return api.Job{
		JobID:       "job-" + leaseID,
		Kind:        api.KindLeaseStart,
		DeviceID:    testDevice,
		LeaseID:     leaseID,
		CatalogID:   "llama-3.1-8b-q4",
		ImageDigest: "example.invalid/hypermesh/workload@sha256:" + strings.Repeat("a", 64),
		FileHash:    hash,
		Runtime:     "nvidia",
	}
}

func assertLeaseClean(t *testing.T, a *Agent, listen string, docker *scriptedDocker) {
	t.Helper()
	if a.currentLease() != nil || a.forwarderAlive() {
		t.Fatal("lease or listener left behind")
	}
	if _, err := os.Stat(filepath.Join(a.cfg.StateDir, leaseFile)); !os.IsNotExist(err) {
		t.Fatal("lease record remains", err)
	}
	if conn, err := net.DialTimeout("tcp", listen, 200*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("listener still accepting")
	}
	if !docker.saw("stop") || !docker.saw("rm") {
		t.Fatalf("cleanup calls %v", docker.arg0())
	}
}

func freeLocalPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return fmt.Sprintf("%d", ln.Addr().(*net.TCPAddr).Port)
}

// A docker that never answers inspect or logs must not hold lease_start past
// HYPERMESH_LEASE_START_READY_TIMEOUT_S. Inspection and the log tail both
// run under the readiness deadline.
func TestLeaseStartHungDockerStaysWithinReadyTimeout(t *testing.T) {
	a, hash, listen, _ := newIngressAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	a.cfg.LeaseReadyWait = 400 * time.Millisecond
	docker := &scriptedDocker{ip: "172.18.0.2"}
	var hung atomic.Int32
	a.runtime = &runtime.NVIDIA{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 0 && (args[0] == "inspect" || args[0] == "logs") {
			hung.Add(1)
			if _, ok := ctx.Deadline(); !ok {
				t.Errorf("docker %s ran without a deadline", args[0])
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return docker.run(ctx, name, args...)
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	got := a.leaseStart(ctx, ingressJob("lease-hung", hash))
	elapsed := time.Since(start)
	if got.Passed {
		t.Fatalf("hung docker passed readiness: %+v", got)
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("lease_start took %s with a 400ms readiness timeout; result %+v", elapsed, got)
	}
	if strings.Contains(got.Reason, "canceled") || hung.Load() == 0 {
		t.Fatalf("reason %q inspected %d", got.Reason, hung.Load())
	}
	if a.currentLease() != nil {
		t.Fatal("lease kept after a readiness failure with working cleanup")
	}
	if conn, err := net.DialTimeout("tcp", listen, 100*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("listener left behind")
	}
}

// A same-lease retry after a failed cleanup removes the old container first
// and then starts a fresh one that has to pass readiness itself.
func TestLeaseStartRecoversCleanupFailedHandleWithFreshStart(t *testing.T) {
	var healthy atomic.Bool
	a, hash, listen, _ := newIngressAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		if healthy.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	a.cfg.LeaseReadyWait = 200 * time.Millisecond
	docker := &scriptedDocker{ip: "172.18.0.2", logsErr: true, stopErr: errors.New("stop timed out")}
	a.runtime = &runtime.NVIDIA{Run: docker.run}
	job := ingressJob("lease-recover", hash)
	if got := a.leaseStart(context.Background(), job); got.Passed || docker.runs != 1 {
		t.Fatalf("first start %+v runs %d", got, docker.runs)
	}
	if h := a.currentLease(); h == nil || h.State != runtime.HandleCleanupFailed {
		t.Fatalf("handle %+v", h)
	}
	docker.mu.Lock()
	docker.stopErr = nil
	docker.mu.Unlock()
	healthy.Store(true)
	got := a.leaseStart(context.Background(), job)
	if !got.Passed || docker.runs != 2 {
		t.Fatalf("recovery start %+v runs %d", got, docker.runs)
	}
	h := a.currentLease()
	if h == nil || !h.Serving() || !a.forwarderAlive() {
		t.Fatalf("handle %+v alive %v", h, a.forwarderAlive())
	}
	calls := docker.arg0()
	stopAt, secondRun, runs := -1, -1, 0
	for i, c := range calls {
		switch c {
		case "stop":
			if stopAt < 0 {
				stopAt = i
			}
		case "run":
			runs++
			if runs == 2 {
				secondRun = i
			}
		}
	}
	if stopAt < 0 || secondRun < stopAt {
		t.Fatalf("old container was not removed before the new start: %v", calls)
	}
	if !a.leaseStop(context.Background(), api.Job{Kind: api.KindLeaseStop, LeaseID: "lease-recover"}).Passed {
		t.Fatal("stop failed")
	}
	assertLeaseClean(t, a, listen, docker)
}

// The container is in memory and in lease.json while the model loads, and
// the record changes to the serving form only after /health passes.
func TestLeaseStartRecordsContainerBeforeReadiness(t *testing.T) {
	a, hash, _, _ := newIngressAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	docker := &scriptedDocker{ip: "172.18.0.2"}
	var seenMem, seenFile atomic.Bool
	docker.onInspect = func(*scriptedDocker) {
		if h := a.currentLease(); h != nil && h.State == runtime.HandleStarting && h.ContainerID == "cid-lease" {
			seenMem.Store(true)
		}
		raw, err := os.ReadFile(filepath.Join(a.cfg.StateDir, leaseFile))
		if err == nil && strings.Contains(string(raw), `"state": "starting"`) && strings.Contains(string(raw), "cid-lease") {
			seenFile.Store(true)
		}
	}
	a.runtime = &runtime.NVIDIA{Run: docker.run}
	if got := a.leaseStart(context.Background(), ingressJob("lease-record", hash)); !got.Passed {
		t.Fatal(got)
	}
	if !seenMem.Load() || !seenFile.Load() {
		t.Fatalf("container recorded before readiness: memory=%v file=%v", seenMem.Load(), seenFile.Load())
	}
	raw, err := os.ReadFile(filepath.Join(a.cfg.StateDir, leaseFile))
	if err != nil || strings.Contains(string(raw), `"state"`) || !a.currentLease().Serving() {
		t.Fatalf("serving record err=%v body=%s", err, raw)
	}
	a.stopLeaseForwarder()
}

// An agent that restarts while the model loads leaves the container running
// and the starting record on disk. The new process can stop it with
// lease_stop, and a retry of the same lease removes it before starting again.
func TestRestartDuringReadinessWaitRecoversContainer(t *testing.T) {
	a, hash, _, _ := newIngressAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	a.cfg.LeaseReadyWait = 150 * time.Millisecond
	docker := &scriptedDocker{ip: "172.18.0.2"}
	var snapshot atomic.Value
	docker.onInspect = func(*scriptedDocker) {
		if raw, err := os.ReadFile(filepath.Join(a.cfg.StateDir, leaseFile)); err == nil {
			snapshot.CompareAndSwap(nil, raw)
		}
	}
	a.runtime = &runtime.NVIDIA{Run: docker.run}
	if got := a.leaseStart(context.Background(), ingressJob("lease-crash", hash)); got.Passed {
		t.Fatal(got)
	}
	raw, _ := snapshot.Load().([]byte)
	if len(raw) == 0 {
		t.Fatal("no record on disk during the readiness wait")
	}
	for _, retry := range []bool{false, true} {
		b, bhash, _, _ := newIngressAgent(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		if err := os.MkdirAll(b.cfg.StateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(b.cfg.StateDir, leaseFile), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		b.setLease(b.loadLease())
		bd := &scriptedDocker{ip: "172.18.0.2"}
		b.runtime = &runtime.NVIDIA{Run: bd.run}
		if b.currentLease() == nil || b.currentLease().Serving() {
			t.Fatalf("loaded handle %+v", b.currentLease())
		}
		if retry {
			if got := b.leaseStart(context.Background(), ingressJob("lease-crash", bhash)); !got.Passed || bd.runs != 1 {
				t.Fatalf("retry %+v runs %d", got, bd.runs)
			}
			if !bd.saw("stop") {
				t.Fatalf("old container was not stopped: %v", bd.arg0())
			}
			b.stopLeaseForwarder()
			continue
		}
		got := b.leaseStop(context.Background(), api.Job{Kind: api.KindLeaseStop, LeaseID: "lease-crash"})
		if !got.Passed || !bd.saw("stop") || !bd.saw("rm") || b.currentLease() != nil {
			t.Fatalf("stop %+v calls %v", got, bd.arg0())
		}
	}
}

// A workload that answers /health with a redirect is not ready, and the agent
// does not request the redirect target.
func TestHealthProbeDoesNotFollowRedirects(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	a, hash, _, _ := newIngressAgent(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/health", http.StatusFound)
	})
	a.cfg.LeaseReadyWait = 300 * time.Millisecond
	docker := &scriptedDocker{ip: "172.18.0.2", logsErr: true}
	a.runtime = &runtime.NVIDIA{Run: docker.run}
	got := a.leaseStart(context.Background(), ingressJob("lease-redirect", hash))
	if got.Passed {
		t.Fatalf("redirecting /health passed readiness: %+v", got)
	}
	if targetHits.Load() != 0 {
		t.Fatalf("redirect target was requested %d times", targetHits.Load())
	}
	if a.currentLease() != nil {
		t.Fatal("lease kept")
	}
}

// A lease_stop that finishes while the readiness wait is ending must win: the
// start fails, and the stopped container's record is not written back.
func TestLeaseStopDuringStartIsNotUndone(t *testing.T) {
	a, hash, listen, _ := newIngressAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	docker := &scriptedDocker{ip: "172.18.0.2"}
	a.runtime = &runtime.NVIDIA{Run: docker.run}
	var stopped atomic.Bool
	a.leaseReady = func(ctx context.Context, _ *runtime.Handle) error {
		got := a.leaseStop(ctx, api.Job{Kind: api.KindLeaseStop, LeaseID: "lease-race"})
		stopped.Store(got.Passed)
		return nil
	}
	got := a.leaseStart(context.Background(), ingressJob("lease-race", hash))
	if !stopped.Load() {
		t.Fatal("lease_stop did not run during the wait")
	}
	if got.Passed || !strings.Contains(got.Reason, "stopped during start") {
		t.Fatalf("result %+v", got)
	}
	assertLeaseClean(t, a, listen, docker)
}

// When client_address changes while the model is still loading, the listener
// moves to the new address and the ingress probe follows it.
func TestStartingLeaseRebindsWhenTunnelAddressChanges(t *testing.T) {
	var ready atomic.Bool
	a, hash, oldListen, _ := newIngressAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		if ready.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	a.cfg.LeaseReadyWait = 5 * time.Second
	docker := &scriptedDocker{ip: "172.18.0.2"}
	a.runtime = &runtime.NVIDIA{Run: docker.run}
	done := make(chan pathb.Result, 1)
	go func() { done <- a.leaseStart(context.Background(), ingressJob("lease-moving", hash)) }()
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatal("timed out waiting for " + what)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	accepts := func(addr string) bool {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}
	waitFor("first listener", func() bool { return accepts(oldListen) })
	_, port, _ := net.SplitHostPort(oldListen)
	newListen := net.JoinHostPort("127.0.0.2", port)
	a.setBindIP("127.0.0.2/32")
	waitFor("rebound listener", func() bool { return accepts(newListen) })
	if accepts(oldListen) {
		t.Fatal("old tunnel address still accepting")
	}
	ready.Store(true)
	select {
	case got := <-done:
		if !got.Passed {
			t.Fatalf("start %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("start did not finish")
	}
	h := a.currentLease()
	if h == nil || !h.Serving() || h.PublishAddr != newListen {
		t.Fatalf("handle %+v, want publish %s", h, newListen)
	}
	a.stopLeaseForwarder()
}
