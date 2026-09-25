package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
	"github.com/FyberLabs/hypermesh-host/internal/telem"
	"github.com/FyberLabs/hypermesh-host/internal/tunnel"
)

const (
	testTenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	testDevice = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	testSecret = "hm_dev_fixture_secret"
	testJob    = "cccccccc-cccc-cccc-cccc-cccccccccccc"
)

type fakeTunnel struct {
	mu        sync.Mutex
	applied   []tunnel.Session
	teardowns int
}

func (f *fakeTunnel) Apply(_ context.Context, sess tunnel.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, sess)
	return nil
}

func (f *fakeTunnel) Teardown(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.teardowns++
	return nil
}

func TestLoopEnrollBearerPersistAndPathB(t *testing.T) {
	var mu sync.Mutex
	var enrolls, heartbeats, pulls, results int
	var enrollAuth, hbAuth string
	var enrollBody api.EnrollRequest

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		enrolls++
		enrollAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&enrollBody)
		mu.Unlock()
		writeJSON(w, http.StatusOK, api.EnrollResponse{
			DeviceID:         testDevice,
			CredentialPrefix: "cafe0000",
			DeviceSecret:     testSecret,
		})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		heartbeats++
		hbAuth = r.Header.Get("Authorization")
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{
			DeviceID:   testDevice,
			PathBState: api.PathBNeverRun,
			AcceptedAt: time.Now().UTC(),
		})
	})
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		pulls++
		n := pulls
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if n == 1 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, api.Job{
			JobID:    testJob,
			Kind:     api.KindPathB,
			DeviceID: testDevice,
			SuiteID:  "thin-v1",
		})
	})
	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/"+testJob+"/result", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		results++
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body api.JobResultRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Passed {
			t.Errorf("stub should pass, got %+v", body)
		}
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("3.0 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := newTestAgent(t, srv.URL, dir, uptime, "one-time-token", "")
	ft := &fakeTunnel{}
	a.tunnels = ft

	waitLoop(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return enrolls == 1 && heartbeats >= 1 && pulls >= 2 && results >= 1
	})

	mu.Lock()
	defer mu.Unlock()
	if enrollAuth != "" {
		t.Fatalf("enroll sent Authorization %q", enrollAuth)
	}
	if hbAuth != "Bearer "+testSecret {
		t.Fatalf("heartbeat auth %q", hbAuth)
	}
	if enrollBody.EnrollToken != "one-time-token" {
		t.Fatal("enroll body token missing")
	}

	path := filepath.Join(dir, "device.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "one-time-token") {
		t.Fatal("enroll token written to state")
	}
	if !strings.Contains(string(raw), testSecret) {
		t.Fatal("device_secret not persisted")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("state mode %o", fi.Mode().Perm())
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.DeviceID != testDevice || st.DeviceSecret != testSecret {
		t.Fatalf("state %+v", st)
	}
}

func TestSkipsEnrollWhenDeviceAndSecretOnDisk(t *testing.T) {
	var heartbeats int
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("enroll must not run when device_id+secret are on disk")
		w.WriteHeader(http.StatusConflict)
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			t.Errorf("stored secret not sent")
		}
		mu.Lock()
		heartbeats++
		mu.Unlock()
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{
			DeviceID:   testDevice,
			PathBState: api.PathBGreen,
			AcceptedAt: time.Now().UTC(),
		})
	})
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "device.json"), []byte(`{"device_id":"`+testDevice+`","device_secret":"`+testSecret+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, srv.URL, dir, uptime, "must-not-be-sent", "")
	a.tunnels = &fakeTunnel{}
	waitLoop(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return heartbeats >= 1
	})
}

func TestTunnelApplyAckAndRevoke(t *testing.T) {
	var mu sync.Mutex
	acks := 0
	phase := 0

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.EnrollResponse{DeviceID: testDevice, DeviceSecret: testSecret})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{DeviceID: testDevice, PathBState: api.PathBGreen, AcceptedAt: time.Now().UTC()})
	})
	mux.HandleFunc(api.PathTunnelAck, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body api.TunnelAckRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Applied {
			t.Errorf("ack applied=false")
		}
		mu.Lock()
		acks++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	acceptTunnelPeer(mux)
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		p := phase
		mu.Unlock()
		if p == 0 {
			writeJSON(w, http.StatusOK, api.TunnelSession{
				TunnelID:        "tun-1",
				LeaseID:         "lease-1",
				Status:          api.TunnelAllocated,
				Endpoint:        "198.51.100.10:51820",
				ServerPublicKey: "serverpub",
				AllowedIPs:      "10.8.0.0/24",
				ClientAddress:   "10.8.0.2/32",
			})
			return
		}
		writeJSON(w, http.StatusOK, api.TunnelSession{
			TunnelID: "tun-1",
			LeaseID:  "lease-1",
			Status:   api.TunnelRevoked,
		})
	})
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, srv.URL, dir, uptime, "tok", "")
	ft := &fakeTunnel{}
	a.tunnels = ft

	waitLoop(t, a, func() bool {
		ft.mu.Lock()
		n := len(ft.applied)
		ft.mu.Unlock()
		mu.Lock()
		nAck := acks
		mu.Unlock()
		return n >= 1 && nAck >= 1
	})

	ft.mu.Lock()
	if len(ft.applied) < 1 || ft.applied[0].PeerPrivateKey == "" {
		ft.mu.Unlock()
		t.Fatalf("apply %+v", ft.applied)
	}
	ft.mu.Unlock()

	mu.Lock()
	phase = 1
	mu.Unlock()

	waitLoop(t, a, func() bool {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		return ft.teardowns >= 1
	})
}

func TestTwoAllocatedTicketsOneHostPeer(t *testing.T) {
	var mu sync.Mutex
	acks, gets := 0, 0

	samePeer := func(tun, lease string) api.TunnelSession {
		return api.TunnelSession{
			TunnelID:        tun,
			LeaseID:         lease,
			Status:          api.TunnelAllocated,
			Endpoint:        "198.51.100.10:51820",
			ServerPublicKey: "serverpub",
			AllowedIPs:      "10.8.0.0/24",
			ClientAddress:   "10.8.0.2/32",
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.EnrollResponse{DeviceID: testDevice, DeviceSecret: testSecret})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{DeviceID: testDevice, PathBState: api.PathBGreen, AcceptedAt: time.Now().UTC()})
	})
	mux.HandleFunc(api.PathTunnelAck, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body api.TunnelAckRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Applied {
			t.Errorf("ack applied=false")
		}
		mu.Lock()
		acks++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	acceptTunnelPeer(mux)
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		gets++
		n := gets
		mu.Unlock()
		if n == 1 {
			writeJSON(w, http.StatusOK, samePeer("tun-1", "lease-1"))
			return
		}
		writeJSON(w, http.StatusOK, samePeer("tun-2", "lease-2"))
	})
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, srv.URL, dir, uptime, "tok", "")

	var ops []string
	a.tunnels = &tunnel.Runner{
		ConfigDir: t.TempDir(),
		Quick: func(_ context.Context, args ...string) error {
			mu.Lock()
			ops = append(ops, args[0])
			mu.Unlock()
			return nil
		},
	}

	waitLoop(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return gets >= 2 && acks >= 1
	})

	mu.Lock()
	defer mu.Unlock()
	ups, downs := 0, 0
	for _, op := range ops {
		switch op {
		case "up":
			ups++
		case "down":
			downs++
		}
	}
	if ups != 1 {
		t.Fatalf("same host peer must wg-quick up once, ops=%v", ops)
	}
	if downs != 0 {
		t.Fatalf("lease/tunnel ticket change must not teardown, ops=%v", ops)
	}
	if acks < 1 {
		t.Fatal("expected at least one ack")
	}
}

func TestEmptyAndUnknownKindFail(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	empty := a.runJob(context.Background(), api.Job{JobID: "j-empty", DeviceID: testDevice})
	if empty.Passed || empty.Reason != "empty job kind" {
		t.Fatalf("empty kind %+v", empty)
	}
	unknown := a.runJob(context.Background(), api.Job{JobID: "j-bad", Kind: "not-a-kind", DeviceID: testDevice})
	if unknown.Passed || unknown.Reason != "unknown job kind" {
		t.Fatalf("unknown kind %+v", unknown)
	}
}

func TestLeaseStartEmptyDigestFails(t *testing.T) {
	var mu sync.Mutex
	var kinds []api.JobKind
	var startPassed *bool
	var dockerCalls int

	jobs := []api.Job{
		{JobID: "j-start", Kind: api.KindLeaseStart, DeviceID: testDevice, LeaseID: "lease-1", CatalogID: "llama-3.1-8b-q4", Runtime: "nvidia"},
		{JobID: "j-stop", Kind: api.KindLeaseStop, DeviceID: testDevice, LeaseID: "lease-1"},
	}

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.EnrollResponse{DeviceID: testDevice, DeviceSecret: testSecret})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{DeviceID: testDevice, PathBState: api.PathBGreen, AcceptedAt: time.Now().UTC()})
	})
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if len(jobs) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		j := jobs[0]
		jobs = jobs[1:]
		writeJSON(w, http.StatusOK, j)
	})
	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/j-start/result", func(w http.ResponseWriter, r *http.Request) {
		var body api.JobResultRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		startPassed = &body.Passed
		kinds = append(kinds, api.KindLeaseStart)
		mu.Unlock()
		if body.Passed {
			t.Errorf("empty-digest lease_start must not pass")
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/j-stop/result", func(w http.ResponseWriter, r *http.Request) {
		var body api.JobResultRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Passed {
			t.Errorf("lease_stop should pass")
		}
		mu.Lock()
		kinds = append(kinds, api.KindLeaseStop)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, srv.URL, dir, uptime, "tok", "")
	a.tunnels = &fakeTunnel{}
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		mu.Lock()
		dockerCalls++
		mu.Unlock()
		return []byte("cid\n"), nil
	}}

	waitLoop(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(kinds) >= 2
	})

	mu.Lock()
	defer mu.Unlock()
	if dockerCalls != 0 {
		t.Fatalf("empty digest must not invoke docker, calls=%d", dockerCalls)
	}
	if startPassed == nil || *startPassed {
		t.Fatalf("empty digest must POST passed=false")
	}
	if len(kinds) < 2 || kinds[0] != api.KindLeaseStart || kinds[1] != api.KindLeaseStop {
		t.Fatalf("kinds %v", kinds)
	}
}

func TestLeaseStartWithDigestUsesNVIDIA(t *testing.T) {
	const ref = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var mu sync.Mutex
	var calls [][]string
	var passed bool

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.EnrollResponse{DeviceID: testDevice, DeviceSecret: testSecret})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{DeviceID: testDevice, PathBState: api.PathBGreen, AcceptedAt: time.Now().UTC()})
	})
	acceptTunnelPeer(mux)
	acceptTunnelAck(mux)
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, allocatedTunnel("lease-2"))
	})
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		done := passed
		mu.Unlock()
		if done {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, api.Job{
			JobID:       "j-dig",
			Kind:        api.KindLeaseStart,
			DeviceID:    testDevice,
			LeaseID:     "lease-2",
			CatalogID:   "llama-3.1-8b-q4",
			ImageDigest: ref,
			Runtime:     "nvidia",
		})
	})
	var postedHash string
	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/j-dig/result", func(w http.ResponseWriter, r *http.Request) {
		var body api.JobResultRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		passed = body.Passed
		postedHash = body.ImageHash
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, srv.URL, dir, uptime, "tok", "")
	wantHash := readyLeaseStart(t, a)
	a.tunnels = &fakeTunnel{}
	a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, a ...string) ([]byte, error) {
		mu.Lock()
		calls = append(calls, append([]string{}, a...))
		mu.Unlock()
		return []byte("cid9\n"), nil
	}}

	waitLoop(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return passed && len(calls) >= 2
	})
	if calls[0][0] != "pull" || calls[0][1] != ref {
		t.Fatalf("pull %v", calls[0])
	}
	runArgs := findDockerRun(calls)
	if runArgs == nil {
		t.Fatalf("missing docker run: %+v", calls)
	}
	joined := strings.Join(runArgs, " ")
	if !strings.Contains(joined, "--runtime=nvidia") || strings.Contains(joined, "--gpus") {
		t.Fatalf("docker args %v", runArgs)
	}
	if !strings.Contains(joined, "-p") || !strings.Contains(joined, "10.77.0.2:8080:8080") {
		t.Fatalf("port publish must be WG IP only: %v", runArgs)
	}
	if strings.Contains(joined, "--network=host") || strings.Contains(joined, "--network host") {
		t.Fatalf("workload must not use host network: %v", runArgs)
	}
	if !strings.Contains(joined, "--network") || !strings.Contains(joined, runtime.WorkloadNetworkName) {
		t.Fatalf("want internal workload network: %v", runArgs)
	}
	if strings.Contains(joined, "docker.sock") {
		t.Fatalf("must not mount docker.sock: %v", runArgs)
	}
	if !strings.Contains(joined, "--cap-drop") || !strings.Contains(joined, "ALL") {
		t.Fatalf("want --cap-drop ALL: %v", runArgs)
	}
	if !strings.Contains(joined, "--read-only") {
		t.Fatalf("want --read-only: %v", runArgs)
	}
	if !strings.Contains(joined, "-v") || !strings.Contains(joined, a.cfg.GGUFHostPath()+":"+runtime.ContainerGGUFPath+":ro") {
		t.Fatalf("GGUF bind-mount %v", runArgs)
	}
	if !strings.Contains(joined, "hypermesh-lease-2") || !strings.Contains(joined, ref) {
		t.Fatalf("name/ref %v", runArgs)
	}
	assertLeaseLlamaServer(t, runArgs, "llama-3.1-8b-q4", "1", "16384", "1800")
	if postedHash != wantHash {
		t.Fatalf("lease_start result image_hash %q want GGUF %q", postedHash, wantHash)
	}
}

func assertLeaseLlamaServer(t *testing.T, args []string, catalog, jinja, ctxSize, timeout string) {
	t.Helper()
	if got := dockerEnvValue(args, "LLAMA_ARG_JINJA"); got != jinja {
		t.Fatalf("LLAMA_ARG_JINJA=%q want %q in %v", got, jinja, args)
	}
	if got := dockerEnvValue(args, "LLAMA_ARG_CTX_SIZE"); got != ctxSize {
		t.Fatalf("LLAMA_ARG_CTX_SIZE=%q want %q in %v", got, ctxSize, args)
	}
	if got := dockerEnvValue(args, "LLAMA_ARG_TIMEOUT"); got != timeout {
		t.Fatalf("LLAMA_ARG_TIMEOUT=%q want %q in %v", got, timeout, args)
	}
	if got := dockerEnvValue(args, runtime.ServeAliasEnv); got != catalog {
		t.Fatalf("alias %q want catalog %q in %v", got, catalog, args)
	}
	joined := strings.Join(args, "\n")
	if !strings.Contains(joined, "--entrypoint") || !strings.Contains(joined, "/bin/sh") {
		t.Fatalf("entrypoint override: %v", args)
	}
	if !strings.Contains(joined, "--alias") || strings.Contains(joined, "hypermesh-test") {
		t.Fatalf("alias must be the catalog id, not hypermesh-test: %v", args)
	}
}

func dockerEnvValue(args []string, key string) string {
	prefix := key + "="
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-e" && strings.HasPrefix(args[i+1], prefix) {
			return strings.TrimPrefix(args[i+1], prefix)
		}
	}
	return ""
}

func findDockerRun(calls [][]string) []string {
	for _, c := range calls {
		if len(c) > 0 && c[0] == "run" {
			return c
		}
	}
	return nil
}

func TestLeaseStartBackends(t *testing.T) {
	const ref = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cases := []struct {
		name       string
		backend    string
		wantPass   bool
		wantDocker bool
		wantReason string
	}{
		{name: "omitted", backend: "", wantPass: true, wantDocker: true, wantReason: "lease_start"},
		{name: "cuda-jetson", backend: "cuda-jetson", wantPass: true, wantDocker: true, wantReason: "lease_start"},
		{name: "cuda-sm87", backend: "cuda-sm87", wantPass: true, wantDocker: true, wantReason: "lease_start"},
		{name: "llama-cpp-cpu", backend: "llama-cpp-cpu", wantPass: false, wantDocker: false, wantReason: "This image does not run llama-cpp-cpu."},
		{name: "llama-cpp-vulkan", backend: "llama-cpp-vulkan", wantPass: false, wantDocker: false, wantReason: "This image does not run llama-cpp-vulkan."},
		{name: "hip-rocm", backend: "hip-rocm", wantPass: false, wantDocker: false, wantReason: "This image does not run hip-rocm."},
		{name: "disk-io", backend: "disk-io", wantPass: false, wantDocker: false, wantReason: "This image does not run disk-io."},
		{name: "unknown", backend: "not-a-backend", wantPass: false, wantDocker: false, wantReason: "This image does not run not-a-backend."},
	}
	for _, tc := range cases {
		if tc.backend == "llama-cpp-cpu" && runtime.MetalImageEnabled() {
			t.Run(tc.name, func(t *testing.T) {
				t.Skip("darwin image: llama-cpp-cpu is the Metal lab path")
			})
			continue
		}
		if tc.backend == "hip-rocm" && runtime.HIPImageEnabled() {
			t.Run(tc.name, func(t *testing.T) {
				t.Skip("rocm image: hip-rocm is the HIP path")
			})
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			uptime := filepath.Join(dir, "uptime")
			_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
			var dockerCalls [][]string
			a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
			if tc.wantPass {
				_ = readyLeaseStart(t, a)
			}
			wantJinja, wantCtx, wantTimeout := "1", "16384", "1800"
			if tc.name == "cuda-jetson" {
				a.cfg.LlamaJinja = "1"
				a.cfg.LlamaCtxSize = "32768"
				a.cfg.LlamaTimeout = "2400"
				wantCtx, wantTimeout = "32768", "2400"
			}
			a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				dockerCalls = append(dockerCalls, append([]string{}, args...))
				return []byte("cid\n"), nil
			}}
			got := a.runJob(context.Background(), api.Job{
				JobID:       "j-backend",
				Kind:        api.KindLeaseStart,
				DeviceID:    testDevice,
				LeaseID:     "lease-b",
				CatalogID:   "llama-3.1-8b-q4",
				ImageDigest: ref,
				Runtime:     "nvidia",
				Backend:     tc.backend,
			})
			if got.Passed != tc.wantPass || got.Reason != tc.wantReason {
				t.Fatalf("result %+v want pass=%v reason=%q", got, tc.wantPass, tc.wantReason)
			}
			if tc.wantDocker {
				if len(dockerCalls) < 2 || dockerCalls[0][0] != "pull" || dockerCalls[0][1] != ref {
					t.Fatalf("expected nvidia pull, calls=%v", dockerCalls)
				}
				runArgs := findDockerRun(dockerCalls)
				if runArgs == nil {
					t.Fatalf("missing docker run: %v", dockerCalls)
				}
				joined := strings.Join(runArgs, " ")
				if !strings.Contains(joined, "--runtime=nvidia") || strings.Contains(joined, "--gpus") {
					t.Fatalf("docker args %v", runArgs)
				}
				if strings.Contains(joined, "--network=host") || !strings.Contains(joined, runtime.WorkloadNetworkName) {
					t.Fatalf("workload jail network: %v", runArgs)
				}
				assertLeaseLlamaServer(t, runArgs, "llama-3.1-8b-q4", wantJinja, wantCtx, wantTimeout)
			} else if len(dockerCalls) != 0 {
				t.Fatalf("stub must not call docker, calls=%v", dockerCalls)
			}
		})
	}
}

func TestLeaseStopIdentity(t *testing.T) {
	const localID = "lease-running"
	handle := runtime.Handle{
		Name:        "hypermesh-lease-running",
		LeaseID:     localID,
		ContainerID: "cid-local",
	}

	cases := []struct {
		name       string
		handle     *runtime.Handle
		disk       bool
		jobLease   string
		wantPass   bool
		wantReason string
		wantDocker bool
		wantKeep   bool
	}{
		{
			name:       "match stops",
			handle:     &handle,
			jobLease:   localID,
			wantPass:   true,
			wantReason: "lease_stop",
			wantDocker: true,
		},
		{
			name:       "mismatch refuses",
			handle:     &handle,
			jobLease:   "lease-other",
			wantPass:   false,
			wantReason: "lease_id mismatch",
			wantKeep:   true,
		},
		{
			name:       "empty lease_id with handle refuses",
			handle:     &handle,
			jobLease:   "",
			wantPass:   false,
			wantReason: "lease_id mismatch",
			wantKeep:   true,
		},
		{
			name:       "whitespace lease_id with handle refuses",
			handle:     &handle,
			jobLease:   "   ",
			wantPass:   false,
			wantReason: "lease_id mismatch",
			wantKeep:   true,
		},
		{
			name:       "no handle soft no-op",
			jobLease:   "lease-anything",
			wantPass:   true,
			wantReason: "lease_stop",
		},
		{
			name:       "no handle empty lease_id still no-op",
			jobLease:   "",
			wantPass:   true,
			wantReason: "lease_stop",
		},
		{
			name:       "disk handle mismatch refuses",
			handle:     &handle,
			disk:       true,
			jobLease:   "lease-other",
			wantPass:   false,
			wantReason: "lease_id mismatch",
			wantKeep:   true,
		},
		{
			name:       "disk handle match stops",
			handle:     &handle,
			disk:       true,
			jobLease:   localID,
			wantPass:   true,
			wantReason: "lease_stop",
			wantDocker: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			uptime := filepath.Join(dir, "uptime")
			if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			var dockerCalls [][]string
			a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
			ft := &fakeTunnel{}
			a.tunnels = ft
			a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				dockerCalls = append(dockerCalls, append([]string{}, args...))
				return []byte("ok\n"), nil
			}}
			if tc.handle != nil {
				h := *tc.handle
				if tc.disk {
					if err := a.saveLease(&h); err != nil {
						t.Fatal(err)
					}
					a.lease = nil
				} else {
					a.lease = &h
				}
			}

			got := a.runJob(context.Background(), api.Job{
				JobID:    "j-stop",
				Kind:     api.KindLeaseStop,
				DeviceID: testDevice,
				LeaseID:  tc.jobLease,
			})
			if got.Passed != tc.wantPass || got.Reason != tc.wantReason {
				t.Fatalf("result %+v want pass=%v reason=%q", got, tc.wantPass, tc.wantReason)
			}
			if tc.wantDocker {
				assertDockerStopThenRM(t, dockerCalls, handle.Name)
			} else if len(dockerCalls) != 0 {
				t.Fatalf("docker must not run, calls=%v", dockerCalls)
			}
			ft.mu.Lock()
			teardowns := ft.teardowns
			ft.mu.Unlock()
			if teardowns != 0 {
				t.Fatalf("lease_stop must not bounce WireGuard, teardowns=%d", teardowns)
			}

			kept := a.lease != nil || a.loadLease() != nil
			if kept != tc.wantKeep {
				t.Fatalf("handle kept=%v want %v mem=%+v disk=%+v", kept, tc.wantKeep, a.lease, a.loadLease())
			}
		})
	}
}

func TestLeaseStopMismatchPostsPassedFalseOnly(t *testing.T) {
	var mu sync.Mutex
	var posted map[string]any
	var dockerCalls int

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.EnrollResponse{DeviceID: testDevice, DeviceSecret: testSecret})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{DeviceID: testDevice, PathBState: api.PathBGreen, AcceptedAt: time.Now().UTC()})
	})
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		done := posted != nil
		mu.Unlock()
		if done {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, api.Job{
			JobID:    "j-mismatch",
			Kind:     api.KindLeaseStop,
			DeviceID: testDevice,
			LeaseID:  "lease-other",
		})
	})
	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/j-mismatch/result", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("result body: %v", err)
		}
		mu.Lock()
		posted = body
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, srv.URL, dir, uptime, "tok", "")
	a.tunnels = &fakeTunnel{}
	if err := a.saveLease(&runtime.Handle{
		Name:        "hypermesh-lease-running",
		LeaseID:     "lease-running",
		ContainerID: "cid-local",
	}); err != nil {
		t.Fatal(err)
	}
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		mu.Lock()
		dockerCalls++
		mu.Unlock()
		return []byte("ok\n"), nil
	}}

	waitLoop(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return posted != nil
	})

	mu.Lock()
	defer mu.Unlock()
	if dockerCalls != 0 {
		t.Fatalf("mismatch must not docker stop, calls=%d", dockerCalls)
	}
	passed, _ := posted["passed"].(bool)
	if passed {
		t.Fatalf("mismatch must POST passed=false, body=%v", posted)
	}
	for _, extra := range []string{"reason", "reason_code", "lease_id", "sell_state", "path_b"} {
		if _, ok := posted[extra]; ok {
			t.Fatalf("result body must stay {passed, image_hash?}, extra %s in %v", extra, posted)
		}
	}
}

func assertDockerStopThenRM(t *testing.T, calls [][]string, name string) {
	t.Helper()
	if len(calls) != 2 || calls[0][0] != "stop" || calls[1][0] != "rm" {
		t.Fatalf("expected docker stop then rm, calls=%v", calls)
	}
	if name != "" && calls[1][len(calls[1])-1] != name {
		t.Fatalf("rm target %v want %s", calls[1], name)
	}
}

func dockerNameArg(args []string) string {
	for i, a := range args {
		if a == "--name" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestLeaseStopThenStartFreesExitedName(t *testing.T) {
	// Same leftover as AGX64-1 SWAP: stop left hypermesh-<lease_id> Exited,
	// next lease_start with that lease_id hit docker exit 125.
	const leaseID = "22b3f0d7-f4dc-4423-8250-58449f15c61d"
	name := runtime.ContainerName(leaseID)
	const digest = "example.invalid/hypermesh/workload@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	present := map[string]string{}
	var dockerCalls [][]string
	var teardowns int

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ft := &fakeTunnel{}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	ggufHash := readyLeaseStart(t, a)
	a.tunnels = ft
	a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		dockerCalls = append(dockerCalls, append([]string{}, args...))
		if len(args) == 0 {
			return nil, errors.New("empty docker")
		}
		switch args[0] {
		case "pull", "image":
			return []byte{}, nil
		case "run":
			got := dockerNameArg(args)
			if got == "" {
				got = name
			}
			if _, ok := present[got]; ok {
				return []byte(`Conflict: The container name "/` + got + `" is already in use`), errors.New("exit status 125")
			}
			present[got] = "running"
			return []byte("cid-reuse\n"), nil
		case "stop":
			target := args[len(args)-1]
			if target == "cid-reuse" || target == name {
				if _, ok := present[name]; ok {
					present[name] = "exited"
				}
			}
			return []byte{}, nil
		case "rm":
			target := args[len(args)-1]
			if target == "cid-reuse" || target == name {
				delete(present, name)
			} else {
				delete(present, target)
			}
			return []byte{}, nil
		default:
			return []byte{}, nil
		}
	}}

	startJob := api.Job{
		JobID: "j-start-1", Kind: api.KindLeaseStart, DeviceID: testDevice,
		LeaseID: leaseID, CatalogID: "llama-3.1-8b-q4",
		ImageDigest: digest, Runtime: "nvidia",
	}
	first := a.runJob(context.Background(), startJob)
	if !first.Passed {
		t.Fatalf("first lease_start %+v", first)
	}
	stop := a.runJob(context.Background(), api.Job{
		JobID: "j-stop-1", Kind: api.KindLeaseStop, DeviceID: testDevice, LeaseID: leaseID,
	})
	if !stop.Passed {
		t.Fatalf("lease_stop %+v", stop)
	}
	if stop.ImageHash != digest && stop.ImageHash != "sha256:fixture" && stop.ImageHash != ggufHash {
		t.Fatalf("lease_stop must not invent a hash: %q", stop.ImageHash)
	}
	if _, ok := present[name]; ok {
		t.Fatalf("matching stop must rm leftover Exited name, leftover=%v", present)
	}
	startJob.JobID = "j-start-2"
	second := a.runJob(context.Background(), startJob)
	if !second.Passed {
		t.Fatalf("same lease_id must not collide on leftover Exited name: %+v calls=%v", second, dockerCalls)
	}
	ft.mu.Lock()
	teardowns = ft.teardowns
	ft.mu.Unlock()
	if teardowns != 0 {
		t.Fatalf("lease_stop must not bounce WireGuard, teardowns=%d", teardowns)
	}
}

func TestLeaseStartCUDAJetsonEmptyDigestStillFails(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var dockerCalls int
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		dockerCalls++
		return []byte("cid\n"), nil
	}}
	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-empty",
		Kind:     api.KindLeaseStart,
		DeviceID: testDevice,
		LeaseID:  "lease-1",
		Runtime:  "nvidia",
		Backend:  "cuda-jetson",
	})
	if got.Passed || got.Reason != "no image_digest; no soak image" {
		t.Fatalf("empty digest must still fail: %+v", got)
	}
	if dockerCalls != 0 {
		t.Fatalf("empty digest must not invoke docker, calls=%d", dockerCalls)
	}
}

func TestLeaseStartRequiresCatalogAlias(t *testing.T) {
	const ref = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var dockerCalls int
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	_ = readyLeaseStart(t, a)
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		dockerCalls++
		return []byte("cid\n"), nil
	}}
	got := a.runJob(context.Background(), api.Job{
		JobID:       "j-noalias",
		Kind:        api.KindLeaseStart,
		DeviceID:    testDevice,
		LeaseID:     "lease-noalias",
		ImageDigest: ref,
		Runtime:     "nvidia",
		Backend:     "cuda-jetson",
	})
	if got.Passed || got.Reason != "catalog_id required for llama-server alias" {
		t.Fatalf("missing catalog id must fail closed: %+v", got)
	}
	if dockerCalls != 0 {
		t.Fatalf("missing catalog id must not invoke docker, calls=%d", dockerCalls)
	}
}

func TestLeaseStartBareDigestReasonUnchanged(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var dockerCalls int
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		dockerCalls++
		return []byte("cid\n"), nil
	}}
	got := a.runJob(context.Background(), api.Job{
		JobID:       "j-bare",
		Kind:        api.KindLeaseStart,
		DeviceID:    testDevice,
		LeaseID:     "lease-bare",
		CatalogID:   "llama-3.1-8b-q4",
		ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Runtime:     "nvidia",
	})
	if got.Passed || got.Reason != "image_digest is not repo@digest" {
		t.Fatalf("bare digest reason unchanged: %+v", got)
	}
	if got.ImageHash != "" {
		t.Fatalf("must not invent image_hash: %+v", got)
	}
	if dockerCalls != 0 {
		t.Fatalf("bare digest must not invoke docker, calls=%d", dockerCalls)
	}
}

func TestLeaseStartDockerErrorAppearsInReason(t *testing.T) {
	const ref = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const hook = "nvidia-cdi-hook cudacompat: slice bounds out of range [:73] with capacity 71"
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var logBuf strings.Builder
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	_ = readyLeaseStart(t, a)
	a.log = slog.New(slog.NewTextHandler(&logBuf, nil))
	a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "run" {
			return []byte(hook + "\n"), errors.New("exit status 1")
		}
		if len(args) > 0 && args[0] == "inspect" {
			return []byte("created\n"), nil
		}
		return []byte{}, nil
	}}
	got := a.runJob(context.Background(), api.Job{
		JobID:       "j-cdi",
		Kind:        api.KindLeaseStart,
		DeviceID:    testDevice,
		LeaseID:     "lease-cdi",
		CatalogID:   "llama-3.1-8b-q4",
		ImageDigest: ref,
		Runtime:     "nvidia",
	})
	if got.Passed {
		t.Fatalf("start must fail: %+v", got)
	}
	if got.ImageHash != "" {
		t.Fatalf("must not invent image_hash: %+v", got)
	}
	if !strings.HasPrefix(got.Reason, "runtime start failed:") {
		t.Fatalf("reason must wrap Start() err, got %q", got.Reason)
	}
	if !strings.Contains(got.Reason, "hypermesh runtime: docker start") || !strings.Contains(got.Reason, "nvidia-cdi-hook") {
		t.Fatalf("reason missing wrapped docker error: %q", got.Reason)
	}
	if got.Reason == "runtime start failed" {
		t.Fatal("opaque runtime start failed must not be posted")
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "lease_start failed") || !strings.Contains(logs, "nvidia-cdi-hook") {
		t.Fatalf("journal must include wrapped Start() error: %s", logs)
	}
	body := jobResultBody(api.KindLeaseStart, got)
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"reason"`) || strings.Contains(string(raw), "reason_code") {
		t.Fatalf("HTTP result must stay {passed, image_hash?}: %s", raw)
	}
}

func TestLeaseStartLocalImageAfterPullUnauthorized(t *testing.T) {
	const ref = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	wantHash := readyLeaseStart(t, a)
	a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch {
		case len(args) > 0 && args[0] == "pull":
			return []byte("Error response from daemon: unauthorized"), errors.New("exit status 1")
		case len(args) >= 2 && args[0] == "image" && args[1] == "inspect":
			return []byte("sha256:localpin\n"), nil
		case len(args) > 0 && args[0] == "network":
			return []byte{}, nil
		case len(args) > 0 && args[0] == "run":
			return []byte("cid-local\n"), nil
		default:
			t.Fatalf("unexpected docker %v", args)
			return nil, nil
		}
	}}
	got := a.runJob(context.Background(), api.Job{
		JobID:       "j-local",
		Kind:        api.KindLeaseStart,
		DeviceID:    testDevice,
		LeaseID:     "lease-local",
		CatalogID:   "llama-3.1-8b-q4",
		ImageDigest: ref,
		Runtime:     "nvidia",
	})
	if !got.Passed || got.Reason != "lease_start" {
		t.Fatalf("local pin must not fail Start: %+v", got)
	}
	if got.ImageHash != wantHash {
		t.Fatalf("must POST GGUF hash, not OCI pin: %+v", got)
	}
}

func TestHeartbeatLogsNVIDIABackend(t *testing.T) {
	var mu sync.Mutex
	var heartbeats int
	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.EnrollResponse{DeviceID: testDevice, DeviceSecret: testSecret})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		heartbeats++
		mu.Unlock()
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{DeviceID: testDevice, PathBState: api.PathBGreen, AcceptedAt: time.Now().UTC()})
	})
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var logBuf strings.Builder
	a := newTestAgent(t, srv.URL, dir, uptime, "tok", "")
	a.log = slog.New(slog.NewTextHandler(&logBuf, nil))
	a.tunnels = &fakeTunnel{}

	waitLoop(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return heartbeats >= 1
	})
	out := logBuf.String()
	if !strings.Contains(out, "backend="+runtime.HostLogLabel()) {
		t.Fatalf("heartbeat must log %s, got %q", runtime.HostLogLabel(), out)
	}
	for _, leak := range []string{"tok/s", "TOPS", "tokens/sec"} {
		if strings.Contains(out, leak) {
			t.Fatalf("do not invent %s: %q", leak, out)
		}
	}
}

func TestHeartbeatLoadedModelHashFromLease(t *testing.T) {
	const digest = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	cases := []struct {
		name   string
		memory bool
		disk   bool
	}{
		{name: "memory handle", memory: true},
		{name: "disk handle", disk: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got api.HeartbeatRequest
			mux := http.NewServeMux()
			mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Errorf("heartbeat decode: %v", err)
				}
				writeJSON(w, http.StatusOK, api.HeartbeatResponse{
					DeviceID:   testDevice,
					PathBState: api.PathBGreen,
					AcceptedAt: time.Now().UTC(),
				})
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			dir := t.TempDir()
			uptime := filepath.Join(dir, "uptime")
			if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			a := newTestAgent(t, srv.URL, dir, uptime, "", "")
			a.state.DeviceID = testDevice
			a.client.DeviceSecret = testSecret
			h := &runtime.Handle{
				Name:        "hypermesh-lease-1",
				LeaseID:     "lease-1",
				ImageDigest: digest,
				ContainerID: "cid-1",
			}
			if tc.memory {
				a.lease = h
			}
			if tc.disk {
				if err := a.saveLease(h); err != nil {
					t.Fatal(err)
				}
				a.lease = nil
			}

			if err := a.heartbeat(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got.LoadedModelHash != digest {
				t.Fatalf("loaded_model_hash %q want handle ImageDigest %q", got.LoadedModelHash, digest)
			}
		})
	}
}

func TestHeartbeatOmitsLoadedModelHashWithoutLease(t *testing.T) {
	cases := []struct {
		name   string
		handle *runtime.Handle
	}{
		{name: "no lease"},
		{name: "empty digest", handle: &runtime.Handle{LeaseID: "lease-1", ImageDigest: "   "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw []byte
			mux := http.NewServeMux()
			mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
				var err error
				raw, err = io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read body: %v", err)
				}
				writeJSON(w, http.StatusOK, api.HeartbeatResponse{
					DeviceID:   testDevice,
					PathBState: api.PathBGreen,
					AcceptedAt: time.Now().UTC(),
				})
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			dir := t.TempDir()
			uptime := filepath.Join(dir, "uptime")
			if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			a := newTestAgent(t, srv.URL, dir, uptime, "", "")
			a.state.DeviceID = testDevice
			a.client.DeviceSecret = testSecret
			a.lease = tc.handle

			if err := a.heartbeat(context.Background()); err != nil {
				t.Fatal(err)
			}
			var got api.HeartbeatRequest
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.LoadedModelHash != "" {
				t.Fatalf("loaded_model_hash must be empty, got %q", got.LoadedModelHash)
			}
			if strings.Contains(string(raw), "loaded_model_hash") {
				t.Fatalf("omitempty must omit loaded_model_hash: %s", raw)
			}
		})
	}
}

func TestLeaseStartResultImageHashIsGGUF(t *testing.T) {
	const ref = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	wantHash := readyLeaseStart(t, a)
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("cid\n"), nil
	}}
	got := a.runJob(context.Background(), api.Job{
		JobID:       "j-hash",
		Kind:        api.KindLeaseStart,
		DeviceID:    testDevice,
		LeaseID:     "lease-hash",
		CatalogID:   "llama-3.1-8b-q4",
		ImageDigest: ref,
		Runtime:     "nvidia",
	})
	if !got.Passed || got.ImageHash != wantHash {
		t.Fatalf("lease_start must POST GGUF sha256 as image_hash: %+v", got)
	}
	if got.ImageHash == ref {
		t.Fatal("must not echo the OCI pin as image_hash")
	}
	body := jobResultBody(api.KindLeaseStart, got)
	if body.ImageHash != wantHash || !body.Passed {
		t.Fatalf("result body %+v", body)
	}
}

func TestPathBPassedRequiresNonEmptyImageHash(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.pathb.Endpoint = "http://127.0.0.1:1/v1/chat/completions"
	a.pathb.HTTP = &http.Client{Timeout: 150 * time.Millisecond}

	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-pathb-hash",
		Kind:     api.KindPathB,
		DeviceID: testDevice,
		SuiteID:  "thin-v1",
	})
	if !got.Passed || strings.TrimSpace(got.ImageHash) == "" {
		t.Fatalf("path_b passed=true requires non-empty image_hash: %+v", got)
	}
}

func TestPathBFailsWhenEmptyTelemAndNoLease(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.cfg.ImageHash = ""
	a.pathb.Endpoint = "http://127.0.0.1:1/v1/chat/completions"
	a.pathb.HTTP = &http.Client{Timeout: 150 * time.Millisecond}

	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-pathb-empty",
		Kind:     api.KindPathB,
		DeviceID: testDevice,
		SuiteID:  "thin-v1",
	})
	if got.Passed || got.Reason != "no image_hash" {
		t.Fatalf("empty telem + no lease must fail path_b: %+v", got)
	}
	if got.ImageHash != "" {
		t.Fatalf("must not invent image_hash: %+v", got)
	}
}

func TestPathBUsesLeaseDigestWhenTelemHashEmpty(t *testing.T) {
	const digest = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.cfg.ImageHash = ""
	a.lease = &runtime.Handle{LeaseID: "lease-1", ImageDigest: digest}
	a.pathb.Endpoint = "http://127.0.0.1:1/v1/chat/completions"
	a.pathb.HTTP = &http.Client{Timeout: 150 * time.Millisecond}

	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-pathb-lease",
		Kind:     api.KindPathB,
		DeviceID: testDevice,
		SuiteID:  "thin-v1",
	})
	if !got.Passed || got.ImageHash != digest {
		t.Fatalf("path_b must use lease ImageDigest when telem hash is empty: %+v", got)
	}
}

func TestStubBackendFailsPathBAndInstanceCheck(t *testing.T) {
	const completion = "UNIQUE_COMPLETION_STUB"
	var probes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": completion}},
			},
		})
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.pathb.Endpoint = srv.URL
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("stub must not call docker")
		return nil, nil
	}}

	backends := []string{"llama-cpp-cpu", "llama-cpp-vulkan", "hip-rocm", "disk-io"}
	kinds := []api.JobKind{api.KindPathB, api.KindInstanceCheck}
	for _, kind := range kinds {
		for _, backend := range backends {
			if backend == "llama-cpp-cpu" && runtime.MetalImageEnabled() {
				continue
			}
			if backend == "hip-rocm" && runtime.HIPImageEnabled() {
				continue
			}
			got := a.runJob(context.Background(), api.Job{
				JobID:    "j-stub",
				Kind:     kind,
				DeviceID: testDevice,
				SuiteID:  "thin-v1",
				Runtime:  "nvidia",
				Backend:  backend,
			})
			want := "This image does not run " + backend + "."
			if got.Passed || got.Reason != want {
				t.Fatalf("%s %s: %+v want %q", kind, backend, got, want)
			}
		}
	}
	if probes != 0 {
		t.Fatalf("stub instance_check must not probe, probes=%d", probes)
	}
}

func TestPathBOmittedBackendStillRunsThinV1(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.pathb.Endpoint = "http://127.0.0.1:1/v1/chat/completions"
	a.pathb.HTTP = &http.Client{Timeout: 150 * time.Millisecond}

	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-pathb",
		Kind:     api.KindPathB,
		DeviceID: testDevice,
		SuiteID:  "thin-v1",
	})
	if !got.Passed || strings.TrimSpace(got.ImageHash) == "" {
		t.Fatalf("omitted backend is cuda-jetson; path_b telem must still pass with image_hash: %+v", got)
	}
}

func TestInstanceCheckFailsWhenEndpointDown(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var logBuf strings.Builder
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.log = slog.New(slog.NewTextHandler(&logBuf, nil))
	a.pathb.Endpoint = "http://127.0.0.1:1/v1/chat/completions"
	a.pathb.HTTP = &http.Client{Timeout: 150 * time.Millisecond}

	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-check",
		Kind:     api.KindInstanceCheck,
		DeviceID: testDevice,
		SuiteID:  "thin-v1",
	})
	if got.Passed {
		t.Fatalf("instance_check must fail when completions are down: %+v", got)
	}
	assertNoPromptInLogs(t, logBuf.String(), got.Reason)
}

func TestInstanceCheckPassesOnFakeCompletions(t *testing.T) {
	const completion = "UNIQUE_COMPLETION_XYZ"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": completion}},
			},
		})
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var logBuf strings.Builder
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.log = slog.New(slog.NewTextHandler(&logBuf, nil))
	a.pathb.Endpoint = srv.URL

	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-check",
		Kind:     api.KindInstanceCheck,
		DeviceID: testDevice,
		SuiteID:  "thin-v1",
	})
	if !got.Passed || strings.TrimSpace(got.ImageHash) == "" {
		t.Fatalf("instance_check should pass with a real choice and image_hash: %+v", got)
	}
	out := logBuf.String() + " " + got.Reason
	assertNoPromptInLogs(t, out, got.Reason)
	if strings.Contains(out, completion) {
		t.Fatal("completion text leaked into logs")
	}
}

func TestPathBStillPassesWhenCompletionsDown(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var logBuf strings.Builder
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.log = slog.New(slog.NewTextHandler(&logBuf, nil))
	a.pathb.Endpoint = "http://127.0.0.1:1/v1/chat/completions"
	a.pathb.HTTP = &http.Client{Timeout: 150 * time.Millisecond}

	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-pathb",
		Kind:     api.KindPathB,
		DeviceID: testDevice,
		SuiteID:  "thin-v1",
	})
	if !got.Passed || strings.TrimSpace(got.ImageHash) == "" {
		t.Fatalf("path_b must still pass telem with image_hash when inference is down: %+v", got)
	}
	assertNoPromptInLogs(t, logBuf.String(), got.Reason)
}

func TestPathBFailsWhenBootIDChanged(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var logBuf strings.Builder
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.log = slog.New(slog.NewTextHandler(&logBuf, nil))
	a.pathb.Endpoint = "http://127.0.0.1:1/v1/chat/completions"
	a.pathb.HTTP = &http.Client{Timeout: 150 * time.Millisecond}

	if err := os.WriteFile(filepath.Join(dir, "boot.json"), []byte(`{"boot_id":"previous-boot","uptime_s":99}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-pathb",
		Kind:     api.KindPathB,
		DeviceID: testDevice,
		SuiteID:  "thin-v1",
	})
	if got.Passed || got.Reason != "reboot" {
		t.Fatalf("path_b must fail on reboot: %+v", got)
	}
	assertNoPromptInLogs(t, logBuf.String(), got.Reason)
}

func TestDefaultControlPlaneURL(t *testing.T) {
	t.Setenv("HYPERMESH_CONTROL_PLANE_URL", "")
	t.Setenv("HYPERMESH_LISTEN_PORT", "")
	t.Setenv("HYPERMESH_COMPLETIONS_URL", "")
	t.Setenv("HYPERMESH_GGUF_PATH", "")
	cfg := FromEnv()
	if cfg.ControlPlaneURL != "https://api.test.hyperme.sh" {
		t.Fatalf("default control plane %q", cfg.ControlPlaneURL)
	}
	if cfg.PublishPort() != "8080" {
		t.Fatalf("default listen port %q", cfg.PublishPort())
	}
	if cfg.CompletionsEndpoint() != "http://127.0.0.1:8080/v1/chat/completions" {
		t.Fatalf("default completions %q", cfg.CompletionsEndpoint())
	}
	if cfg.GGUFHostPath() != "" {
		t.Fatalf("unset GGUF path must be empty, got %q", cfg.GGUFHostPath())
	}
	if cfg.LlamaJinja != "1" || cfg.LlamaCtxSize != "16384" || cfg.LlamaTimeout != "1800" {
		t.Fatalf("llama defaults %+v", cfg)
	}
}

func TestLlamaServerEnvOverrides(t *testing.T) {
	t.Setenv("HYPERMESH_LLAMA_JINJA", "1")
	t.Setenv("HYPERMESH_LLAMA_CTX_SIZE", "32768")
	t.Setenv("HYPERMESH_LLAMA_TIMEOUT", "900")
	cfg := FromEnv()
	if cfg.LlamaJinja != "1" || cfg.LlamaCtxSize != "32768" || cfg.LlamaTimeout != "900" {
		t.Fatalf("llama env %+v", cfg)
	}
}

func assertNoPromptInLogs(t *testing.T, parts ...string) {
	t.Helper()
	joined := strings.Join(parts, "\n")
	for _, leak := range []string{
		"Reply with the single word ready.",
		"Reply with",
		"renter",
	} {
		if strings.Contains(joined, leak) {
			t.Fatalf("prompt leaked into logs/reason: %q in %q", leak, joined)
		}
	}
}

func TestValidateTenant(t *testing.T) {
	cfg := FromEnv()
	cfg.TenantID = "not-a-uuid"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected tenant uuid error")
	}
}

func TestDeviceIDWithoutSecretErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "device.json"), []byte(`{"device_id":"`+testDevice+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ControlPlaneURL: "http://127.0.0.1:8010",
		TenantID:        testTenant,
		StateDir:        dir,
		HeartbeatEvery:  time.Hour,
		JobEvery:        time.Hour,
	}
	a, err := New(cfg, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	err = a.ensureEnrolled(context.Background())
	if err == nil || !strings.Contains(err.Error(), "enroll-token") {
		t.Fatalf("got %v", err)
	}
}

func writeTestGGUF(t *testing.T, dir string) (path, hash string) {
	t.Helper()
	path = filepath.Join(dir, "model.gguf")
	body := []byte("fixture-gguf-weights")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return path, "sha256:" + hex.EncodeToString(sum[:])
}

func readyLeaseStart(t *testing.T, a *Agent) string {
	t.Helper()
	path, hash := writeTestGGUF(t, a.cfg.StateDir)
	a.cfg.GGUFPath = path
	a.bindIP = "10.77.0.2/32"
	return hash
}

func allocatedTunnel(leaseID string) api.TunnelSession {
	return api.TunnelSession{
		TunnelID:        "tun-live",
		LeaseID:         leaseID,
		Status:          api.TunnelAllocated,
		Endpoint:        "wg.test.hyperme.sh:51820",
		ServerPublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		AllowedIPs:      "10.77.0.0/24",
		ClientAddress:   "10.77.0.2/32",
	}
}

func acceptTunnelAck(mux *http.ServeMux) {
	mux.HandleFunc(api.PathTunnelAck, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func newTestAgent(t *testing.T, url, dir, uptime, enroll, image string) *Agent {
	t.Helper()
	cfg := Config{
		ControlPlaneURL: url,
		TenantID:        testTenant,
		EnrollToken:     enroll,
		StateDir:        dir,
		HeartbeatEvery:  40 * time.Millisecond,
		JobEvery:        40 * time.Millisecond,
		ImageHash:       image,
	}
	if image == "" {
		cfg.ImageHash = "sha256:fixture"
	}
	a, err := New(cfg, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	boot := filepath.Join(dir, "proc-boot-id")
	if err := os.WriteFile(boot, []byte("test-boot-id\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.telem = telem.Source{
		UptimePath: uptime,
		BootIDPath: boot,
		LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
	}
	a.pathb.Collector = a.telem
	a.keyDir = t.TempDir()
	return a
}

func acceptTunnelPeer(mux *http.ServeMux) {
	mux.HandleFunc(api.PathTunnelPeer, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func waitLoop(t *testing.T, a *Agent, ok func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			cancel()
			<-done
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("loop did not reach expected state")
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
