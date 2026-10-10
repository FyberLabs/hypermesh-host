package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
)

func writeRawSell(t *testing.T, a *Agent, raw string) {
	t.Helper()
	if err := os.WriteFile(a.sellPath(), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

func storedSell(t *testing.T, a *Agent) SellPosture {
	t.Helper()
	raw, err := os.ReadFile(a.sellPath())
	if err != nil {
		t.Fatal(err)
	}
	var p SellPosture
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("sell.json %s: %v", raw, err)
	}
	return p
}

func TestCertifiedActivationBecomesSellable(t *testing.T) {
	a := newOwnerTestAgent(t)
	// The installer writes this guard before starting the service.
	writeRawSell(t, a, `{"sell_state":"paused","source":"activation"}`)
	if got := a.effectiveSell(); got.SellState != api.SellPaused || got.Source != SellSourceActivation {
		t.Fatalf("guard not honored before release: %+v", got)
	}
	got, err := a.releaseActivationPause()
	if err != nil || got.SellState != api.SellSelling {
		t.Fatalf("release %+v %v", got, err)
	}
	if storedSell(t, a).SellState != api.SellSelling {
		t.Fatal("release not persisted")
	}
	// A repeated release is a no-op.
	if got, err := a.releaseActivationPause(); err != nil || got.SellState != api.SellSelling {
		t.Fatalf("second release %+v %v", got, err)
	}
	// With no sell.json at all the agent default is selling.
	b := newOwnerTestAgent(t)
	if b.effectiveSell().SellState != api.SellSelling {
		t.Fatal("fresh host is not selling")
	}
}

func TestActivationReleaseKeepsOwnerPause(t *testing.T) {
	a := newOwnerTestAgent(t)
	out := a.sellPause(api.Job{Kind: api.KindSellPause, DeviceID: testDevice})
	if !out.Passed || out.SellState != api.SellPaused {
		t.Fatalf("pause %+v", out)
	}
	got, err := a.releaseActivationPause()
	if err != nil || got.SellState != api.SellPaused || got.Source != SellSourceOwner {
		t.Fatalf("owner pause released by activation: %+v %v", got, err)
	}
}

func TestOwnerPauseOverActivationGuardIsOwnerPause(t *testing.T) {
	a := newOwnerTestAgent(t)
	writeRawSell(t, a, `{"sell_state":"paused","source":"activation"}`)
	out := a.sellPause(api.Job{Kind: api.KindSellPause, DeviceID: testDevice})
	if !out.Passed || storedSell(t, a).Source != SellSourceOwner {
		t.Fatalf("owner pause not recorded over guard: %+v %+v", out, storedSell(t, a))
	}
	if got, _ := a.releaseActivationPause(); got.SellState != api.SellPaused {
		t.Fatal("activation release cleared an owner pause")
	}
}

func TestUpgradeClearsActivationShapedPause(t *testing.T) {
	a := newOwnerTestAgent(t)
	// The exact bytes earlier installers wrote at activation.
	writeRawSell(t, a, `{"sell_state":"paused"}`)
	if got := a.effectiveSell(); got.SellState != api.SellSelling {
		t.Fatalf("activation pause kept after upgrade: %+v", got)
	}
	if storedSell(t, a).SellState != api.SellSelling {
		t.Fatal("cleared posture not persisted")
	}
}

func TestUpgradeKeepsOwnerPauses(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	t.Run("timed", func(t *testing.T) {
		a := newOwnerTestAgent(t)
		a.now = func() time.Time { return now }
		writeRawSell(t, a, "{\n  \"sell_state\": \"paused\",\n  \"until\": \"2026-10-08T00:00:00Z\"\n}\n")
		got := a.effectiveSell()
		if got.SellState != api.SellPaused || got.Until != "2026-10-08T00:00:00Z" {
			t.Fatalf("timed owner pause lost: %+v", got)
		}
	})
	t.Run("indefinite written by the agent", func(t *testing.T) {
		// An owner command, a control-plane sell_pause job and the old
		// maintenance pause all wrote this indented shape; keep it.
		a := newOwnerTestAgent(t)
		a.now = func() time.Time { return now }
		writeRawSell(t, a, "{\n  \"sell_state\": \"paused\"\n}\n")
		got := a.effectiveSell()
		if got.SellState != api.SellPaused || got.Source != SellSourceOwner {
			t.Fatalf("indefinite agent-written pause lost: %+v", got)
		}
		if storedSell(t, a).Source != SellSourceOwner {
			t.Fatal("owner source not persisted")
		}
		if got := a.effectiveSell(); got.SellState != api.SellPaused {
			t.Fatal("labeled pause not stable")
		}
	})
	t.Run("pause from a plane job before upgrade", func(t *testing.T) {
		a := newOwnerTestAgent(t)
		if err := writeJSON0600(a.sellPath(), struct {
			SellState api.SellState `json:"sell_state"`
		}{api.SellPaused}); err != nil {
			t.Fatal(err)
		}
		if got := a.effectiveSell(); got.SellState != api.SellPaused {
			t.Fatalf("plane pause cleared: %+v", got)
		}
	})
}

func TestOwnerCLIPauseThenResume(t *testing.T) {
	a := newOwnerTestAgent(t)
	startTestOwner(t, a)
	var out bytes.Buffer
	if err := runOwnerCLI(a.cfg.StateDir, []string{"sell-pause"}, &out); err != nil {
		t.Fatal(err)
	}
	if a.effectiveSell().SellState != api.SellPaused {
		t.Fatal("pause not applied")
	}
	out.Reset()
	if err := runOwnerCLI(a.cfg.StateDir, []string{"sell-resume"}, &out); err != nil {
		t.Fatal(err)
	}
	var body ownerSellResumeResponse
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatalf("%s: %v", out.String(), err)
	}
	if !body.Passed || body.SellState != api.SellSelling || body.DeviceID != testDevice {
		t.Fatalf("resume %+v", body)
	}
	if a.effectiveSell().SellState != api.SellSelling {
		t.Fatal("resume not applied")
	}
	raw, err := os.ReadFile(ownerEffectsPath(a.cfg.StateDir))
	if err != nil || !strings.Contains(string(raw), toolSellResume) {
		t.Fatalf("resume effect missing: %s %v", raw, err)
	}
	// A resume after a resume is a no-op that still passes.
	out.Reset()
	if err := runOwnerCLI(a.cfg.StateDir, []string{"sell-resume"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := runOwnerCLI(a.cfg.StateDir, []string{"sell-resume", "extra"}, &out); err == nil {
		t.Fatal("extra argument accepted")
	}
}

func TestEndedSellPauseJobResumes(t *testing.T) {
	a := newOwnerTestAgent(t)
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	if out := a.sellPause(api.Job{Kind: api.KindSellPause, DeviceID: testDevice}); !out.Passed {
		t.Fatalf("pause %+v", out)
	}
	// The plane resumes with a pause whose end is not after the host clock.
	refused := a.runJob(context.Background(), api.Job{Kind: api.KindSellPause, DeviceID: "other", Until: "2026-10-07T11:59:00Z"})
	if refused.Passed || refused.SellState != api.SellPaused {
		t.Fatalf("foreign resume %+v", refused)
	}
	out := a.runJob(context.Background(), api.Job{Kind: api.KindSellPause, DeviceID: testDevice, Until: "2026-10-07T11:59:00Z"})
	if !out.Passed || out.SellState != api.SellSelling || out.Until != "" {
		t.Fatalf("resume %+v", out)
	}
	if storedSell(t, a).SellState != api.SellSelling {
		t.Fatal("resume not persisted")
	}
	body := jobResultBody(api.KindSellPause, out)
	if body.SellState != api.SellSelling || body.DeviceID != testDevice || !body.Passed {
		t.Fatalf("result body %+v", body)
	}
	// A pause that ends in the future is still a pause.
	out = a.runJob(context.Background(), api.Job{Kind: api.KindSellPause, DeviceID: testDevice, Until: "2026-10-07T13:00:00Z"})
	if !out.Passed || out.SellState != api.SellPaused || out.Until != "2026-10-07T13:00:00Z" {
		t.Fatalf("timed pause %+v", out)
	}
	// During maintenance the ended pause is refused like any resume.
	if err := a.enterMaintenance(context.Background(), strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	out = a.runJob(context.Background(), api.Job{Kind: api.KindSellPause, DeviceID: testDevice, Until: "2026-10-07T11:59:00Z"})
	if out.Passed || out.Reason != "host maintenance hold" || out.SellState != api.SellPaused {
		t.Fatalf("resume during maintenance %+v", out)
	}
}

func TestMaintenanceRestoresSellingOrOwnerPause(t *testing.T) {
	sha := strings.Repeat("a", 64)
	release := func(t *testing.T, a *Agent) {
		t.Helper()
		if err := os.Remove(filepath.Join(a.cfg.StateDir, "maintenance.json")); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("selling host sells after release", func(t *testing.T) {
		a := newOwnerTestAgent(t)
		if err := a.enterMaintenance(context.Background(), sha); err != nil {
			t.Fatal(err)
		}
		// Earlier installers read this file strictly during resume.
		raw, _ := os.ReadFile(a.sellPath())
		var legacy struct {
			SellState string `json:"sell_state"`
			Until     string `json:"until,omitempty"`
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&legacy); err != nil || legacy.SellState != "paused" || legacy.Until != "" {
			t.Fatalf("maintenance pause shape changed: %s %v", raw, err)
		}
		if res := a.sellResume(testDevice); res.Passed || res.Reason != "host maintenance hold" {
			t.Fatalf("resume during maintenance %+v", res)
		}
		release(t, a)
		if got := a.effectiveSell(); got.SellState != api.SellSelling {
			t.Fatalf("host stayed paused after maintenance: %+v", got)
		}
	})
	t.Run("owner pause before maintenance survives", func(t *testing.T) {
		a := newOwnerTestAgent(t)
		a.now = func() time.Time { return time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC) }
		if out := a.sellPause(api.Job{Kind: api.KindSellPause, DeviceID: testDevice, Until: "2026-10-09T00:00:00Z"}); !out.Passed {
			t.Fatalf("pause %+v", out)
		}
		if err := a.enterMaintenance(context.Background(), sha); err != nil {
			t.Fatal(err)
		}
		release(t, a)
		got := a.effectiveSell()
		if got.SellState != api.SellPaused || got.Until != "2026-10-09T00:00:00Z" || got.Source != SellSourceOwner {
			t.Fatalf("owner pause lost across maintenance: %+v", got)
		}
		if _, err := os.Stat(a.sellOwnerPath()); !os.IsNotExist(err) {
			t.Fatal("kept owner pause not cleaned up")
		}
	})
	t.Run("owner pause during maintenance survives", func(t *testing.T) {
		a := newOwnerTestAgent(t)
		if err := a.enterMaintenance(context.Background(), sha); err != nil {
			t.Fatal(err)
		}
		if out := a.sellPause(api.Job{Kind: api.KindSellPause, DeviceID: testDevice}); !out.Passed || out.SellState != api.SellPaused {
			t.Fatalf("pause %+v", out)
		}
		if storedSell(t, a).Source != "" {
			t.Fatal("maintenance pause shape changed by owner pause")
		}
		release(t, a)
		if got := a.effectiveSell(); got.SellState != api.SellPaused || got.Source != SellSourceOwner {
			t.Fatalf("owner pause lost: %+v", got)
		}
	})
}

func TestMaintenancePauseSurvivesConcurrentPostureReads(t *testing.T) {
	for i := 0; i < 20; i++ {
		a := newOwnerTestAgent(t)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for j := 0; j < 50; j++ {
				a.effectiveSell()
			}
		}()
		if err := a.enterMaintenance(context.Background(), strings.Repeat("b", 64)); err != nil {
			t.Fatal(err)
		}
		<-done
		if got := storedSell(t, a); got.SellState != api.SellPaused || got.Source != "" {
			t.Fatalf("maintenance pause settled while entering maintenance: %+v", got)
		}
	}
}
