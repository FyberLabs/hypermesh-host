package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

func TestSupervisorHostRefusesLeaseStart(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.cfg.HostType = HostTypeSupervisor
	a.state.DeviceID = testDevice

	dockerCalled := false
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		dockerCalled = true
		return []byte("cid\n"), nil
	}}

	got := a.leaseStart(context.Background(), api.Job{
		JobID:       "sup-lease",
		Kind:        api.KindLeaseStart,
		DeviceID:    testDevice,
		LeaseID:     "lease-supervisor",
		CatalogID:   "llama-3.1-8b-q4",
		ImageDigest: "ghcr.io/fyberlabs/hypermesh-test@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Runtime:     "nvidia",
		Backend:     "cuda-jetson",
	})
	if got.Passed {
		t.Fatalf("supervisor host accepted a weight lease: %+v", got)
	}
	if got.Reason != SupervisorLeaseRefuse {
		t.Fatalf("reason %q want %q", got.Reason, SupervisorLeaseRefuse)
	}
	if got.ImageHash != "" {
		t.Fatalf("supervisor host invented an image hash: %q", got.ImageHash)
	}
	if dockerCalled {
		t.Fatal("docker ran for a supervisor host lease_start")
	}
}

func TestModelHostTypeStillReachesLeaseStart(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.state.DeviceID = testDevice
	got := a.leaseStart(context.Background(), api.Job{
		JobID:    "model-empty",
		Kind:     api.KindLeaseStart,
		DeviceID: testDevice,
		LeaseID:  "lease-model",
	})
	if got.Reason == SupervisorLeaseRefuse {
		t.Fatalf("model host used the supervisor refusal: %+v", got)
	}
}

func TestUnknownHostTypeRejected(t *testing.T) {
	cfg := Config{
		ControlPlaneURL: "https://api.test.hyperme.sh",
		TenantID:        testTenant,
		StateDir:        t.TempDir(),
		HeartbeatEvery:  defaultHeartbeat,
		JobEvery:        defaultJobInterval,
		HostType:        "weight",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected unknown host type to fail")
	}
}

func TestSupervisorHostTypeOnEnrollAndHeartbeat(t *testing.T) {
	var enrollBody, hbBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		enrollBody = b
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(api.EnrollResponse{
			DeviceID:         testDevice,
			CredentialPrefix: "cafe",
			DeviceSecret:     testSecret,
		})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		hbBody = b
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(api.HeartbeatResponse{
			DeviceID:   testDevice,
			PathBState: api.PathBNeverRun,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dropped := false
	a := newTestAgent(t, srv.URL, dir, uptime, "one-time-token", "sha256:fixture")
	a.cfg.HostType = HostTypeSupervisor
	a.OnEnrolled = func() { dropped = true }

	if err := a.ensureEnrolled(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.cfg.EnrollToken != "" {
		t.Fatal("enroll token kept after enroll")
	}
	if !dropped {
		t.Fatal("enroll token drop hook did not run")
	}
	var enroll api.EnrollRequest
	if err := json.Unmarshal(enrollBody, &enroll); err != nil {
		t.Fatal(err)
	}
	if enroll.HostType != HostTypeSupervisor {
		t.Fatalf("enroll host_type %q body %s", enroll.HostType, enrollBody)
	}
	if enroll.EnrollToken != "one-time-token" {
		t.Fatalf("enroll token %q", enroll.EnrollToken)
	}

	if err := a.heartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	var hb api.HeartbeatRequest
	if err := json.Unmarshal(hbBody, &hb); err != nil {
		t.Fatal(err)
	}
	if hb.HostType != HostTypeSupervisor {
		t.Fatalf("heartbeat host_type %q body %s", hb.HostType, hbBody)
	}
	if hb.Health == nil {
		t.Fatal("supervisor heartbeat dropped the health object")
	}
}

func TestModelHeartbeatOmitsHostType(t *testing.T) {
	var hbBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		hbBody = b
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(api.HeartbeatResponse{DeviceID: testDevice, PathBState: api.PathBNeverRun})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, srv.URL, dir, uptime, "", "sha256:fixture")
	a.state.DeviceID = testDevice
	a.client.DeviceSecret = testSecret
	if err := a.heartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(hbBody, &top); err != nil {
		t.Fatal(err)
	}
	if _, ok := top["host_type"]; ok {
		t.Fatalf("model heartbeat sent host_type: %s", hbBody)
	}
}
