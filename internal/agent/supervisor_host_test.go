package agent

import (
	"context"
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
