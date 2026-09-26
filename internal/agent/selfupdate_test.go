package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

func TestBusyLeaseAndUnacked(t *testing.T) {
	a := &Agent{}
	if a.Busy() {
		t.Fatal("idle agent is busy")
	}
	a.lease = &runtime.Handle{LeaseID: "lease-1"}
	if !a.Busy() {
		t.Fatal("lease must defer an update")
	}
	a.lease = nil
	a.pendingResult.Store(true)
	if !a.Busy() {
		t.Fatal("unacked job must defer an update")
	}
	a.pendingResult.Store(false)
	if a.Busy() {
		t.Fatal("acked job is still busy")
	}
}

func TestMaybeSelfUpdatePassesBusy(t *testing.T) {
	a := &Agent{cfg: Config{
		UpdateChannel: "test",
		UpdateEvery:   time.Hour,
		TenantID:      "11111111-1111-1111-1111-111111111111",
	}}
	a.lease = &runtime.Handle{LeaseID: "lease-1"}
	var sawBusy bool
	var calls int
	a.selfUpdate = func(_ context.Context, busy bool) error {
		calls++
		sawBusy = busy
		return nil
	}
	a.log = discardLog()
	a.maybeSelfUpdate(context.Background())
	a.maybeSelfUpdate(context.Background())
	if calls != 1 || !sawBusy {
		t.Fatalf("calls %d busy %v", calls, sawBusy)
	}
}

func TestUpdateChannelValidate(t *testing.T) {
	cfg := Config{
		ControlPlaneURL: "https://api.test.hyperme.sh",
		TenantID:        "11111111-1111-1111-1111-111111111111",
		StateDir:        t.TempDir(),
		HeartbeatEvery:  time.Second,
		JobEvery:        time.Second,
		UpdateChannel:   "nope",
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "test or prod") {
		t.Fatal(err)
	}
	t.Setenv("HYPERMESH_UPDATE_CHANNEL", "prod")
	t.Setenv("HYPERMESH_UPDATE_EVERY", "15m")
	got := FromEnv()
	if got.UpdateChannel != "prod" || got.UpdateEvery != 15*time.Minute {
		t.Fatalf("channel %q every %s", got.UpdateChannel, got.UpdateEvery)
	}
}
