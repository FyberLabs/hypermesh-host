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
	case "stop", "rm":
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
