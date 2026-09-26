package agent

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/runtime"
	"github.com/FyberLabs/hypermesh-host/internal/update"
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

func TestUpdateLoops(t *testing.T) {
	body, err := os.ReadFile("agent.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	heart := funcBody(t, text, "func (a *Agent) heartbeatLoop")
	jobs := funcBody(t, text, "func (a *Agent) jobLoop")
	watch := strings.Index(heart, "startUpdateWatch")
	beat := strings.Index(heart, "a.heartbeat(")
	if watch < 0 || beat < watch {
		t.Fatal("rollback watch is not started before the first heartbeat")
	}
	if strings.Contains(heart, "maybeSelfUpdate") {
		t.Fatal("heartbeat loop runs the self-update check")
	}
	if strings.Index(jobs, "maybeSelfUpdate") < 0 {
		t.Fatal("job loop does not check for a self-update")
	}
	if strings.Contains(jobs, "startUpdateWatch") {
		t.Fatal("job loop owns the rollback watch")
	}
	self, err := os.ReadFile("selfupdate.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(self), "StillBusy: a.Busy") {
		t.Fatal("install does not recheck Busy")
	}
}

func funcBody(t *testing.T, text, sig string) string {
	t.Helper()
	start := strings.Index(text, sig)
	if start < 0 {
		t.Fatalf("missing %s", sig)
	}
	rest := text[start+len(sig):]
	next := strings.Index(rest, "\nfunc ")
	if next < 0 {
		return rest
	}
	return rest[:next]
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

func TestUpdateLogUpToDateIsDebug(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logUpdate(log, update.Plan{Action: update.ActionSkip, Reason: "up to date"})
	if !strings.Contains(buf.String(), "level=DEBUG") || strings.Contains(buf.String(), "level=INFO") {
		t.Fatalf("up to date log:\n%s", buf.String())
	}
	buf.Reset()
	logUpdate(log, update.Plan{Action: update.ActionReject, Reason: "build is not newer"})
	if !strings.Contains(buf.String(), "level=INFO") || strings.Contains(buf.String(), "level=DEBUG") {
		t.Fatalf("reject log:\n%s", buf.String())
	}
}

func TestUpdateFingerprintsFromEnv(t *testing.T) {
	t.Setenv("HYPERMESH_TENANT_ID", "11111111-1111-1111-1111-111111111111")
	t.Setenv("HYPERMESH_UPDATE_FINGERPRINTS", "0123456789abcdef0123456789abcdef01234567, abcdefabcdefabcdefabcdefabcdefabcdefabcd")
	got := FromEnv()
	if got.UpdateFingerprintsInvalid || len(got.UpdateFingerprints) != 2 {
		t.Fatalf("fingerprints %+v invalid %v", got.UpdateFingerprints, got.UpdateFingerprintsInvalid)
	}
	if got.UpdateFingerprints[0] != "0123456789ABCDEF0123456789ABCDEF01234567" {
		t.Fatalf("normalized %q", got.UpdateFingerprints[0])
	}
	t.Setenv("HYPERMESH_UPDATE_FINGERPRINTS", "not-a-fingerprint")
	got = FromEnv()
	if err := got.Validate(); err == nil || !strings.Contains(err.Error(), "FINGERPRINTS") {
		t.Fatal(err)
	}
}
