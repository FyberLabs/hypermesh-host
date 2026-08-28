package agent

import (
	"context"
	"encoding/json"
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
				PeerPrivateKey:  "PEERPRIVATE",
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
	if len(ft.applied) < 1 || ft.applied[0].PeerPrivateKey != "PEERPRIVATE" {
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
			PeerPrivateKey:  "PEERPRIVATE",
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
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
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
	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/j-dig/result", func(w http.ResponseWriter, r *http.Request) {
		var body api.JobResultRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		passed = body.Passed
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
	joined := strings.Join(calls[1], " ")
	if !strings.Contains(joined, "--runtime=nvidia") || strings.Contains(joined, "--gpus") {
		t.Fatalf("docker args %v", calls[1])
	}
	if !strings.Contains(joined, "-p") || !strings.Contains(joined, "8080:8080") {
		t.Fatalf("port publish %v", calls[1])
	}
	if !strings.Contains(joined, "hypermesh-lease-2") || !strings.Contains(joined, ref) {
		t.Fatalf("name/ref %v", calls[1])
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
	if !got.Passed {
		t.Fatalf("instance_check should pass with a real choice: %+v", got)
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
	if !got.Passed {
		t.Fatalf("path_b must still pass telem when inference is down: %+v", got)
	}
	assertNoPromptInLogs(t, logBuf.String(), got.Reason)
}

func TestDefaultControlPlaneURL(t *testing.T) {
	t.Setenv("HYPERMESH_CONTROL_PLANE_URL", "")
	t.Setenv("HYPERMESH_LISTEN_PORT", "")
	t.Setenv("HYPERMESH_COMPLETIONS_URL", "")
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
	a.telem = telem.Source{
		UptimePath: uptime,
		LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
	}
	a.pathb.Collector = a.telem
	return a
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
