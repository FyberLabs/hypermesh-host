package agent

import (
	"context"
	"encoding/json"
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
)

const sellTestDigest = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestSellPauseAppliesAndHeartbeatReports(t *testing.T) {
	var mu sync.Mutex
	var hbBodies []api.HeartbeatRequest
	var result api.JobResultRequest
	var results int

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.EnrollResponse{DeviceID: testDevice, DeviceSecret: testSecret})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		var body api.HeartbeatRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		hbBodies = append(hbBodies, body)
		mu.Unlock()
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{
			DeviceID:   testDevice,
			PathBState: api.PathBGreen,
			SellState:  body.SellState,
			AcceptedAt: time.Now().UTC(),
		})
	})
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		done := results > 0
		mu.Unlock()
		if done {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, api.Job{
			JobID:    "j-pause",
			Kind:     api.KindSellPause,
			DeviceID: testDevice,
			Until:    "2026-09-08T00:00:00Z",
		})
	})
	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/j-pause/result", func(w http.ResponseWriter, r *http.Request) {
		var body api.JobResultRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		result = body
		results++
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

	waitLoop(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		if results < 1 {
			return false
		}
		for _, hb := range hbBodies {
			if hb.SellState == api.SellPaused {
				return true
			}
		}
		return false
	})

	mu.Lock()
	defer mu.Unlock()
	if len(hbBodies) == 0 || hbBodies[0].SellState != api.SellSelling {
		t.Fatalf("first heartbeat must report selling, got %+v", hbBodies)
	}
	if !result.Passed || result.SellState != api.SellPaused {
		t.Fatalf("sell_pause result %+v", result)
	}
	if result.SellState == api.SellNA {
		t.Fatal("never POST passed=true with sell_state=n/a")
	}
	if result.Until != "2026-09-08T00:00:00Z" || result.DeviceID != testDevice {
		t.Fatalf("echo until/device_id %+v", result)
	}
	sawPausedHB := false
	for _, hb := range hbBodies {
		if hb.SellState == api.SellPaused {
			sawPausedHB = true
		}
		if string(hb.SellState) == "green" || string(hb.SellState) == "never-run" {
			t.Fatalf("must not copy path_b_state into sell_state: %+v", hb)
		}
	}
	if !sawPausedHB {
		t.Fatal("heartbeat after apply must include sell_state=paused")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "sell.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"paused"`) {
		t.Fatalf("sell.json %s", raw)
	}
}

func TestSellPauseDeviceMismatchDoesNotPause(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.state.DeviceID = testDevice

	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-mis",
		Kind:     api.KindSellPause,
		DeviceID: "dddddddd-dddd-dddd-dddd-dddddddddddd",
		Until:    "2026-09-08T00:00:00Z",
	})
	if got.Passed || got.Reason != "device_id mismatch" {
		t.Fatalf("mismatch result %+v", got)
	}
	if got.SellState != api.SellSelling {
		t.Fatalf("report current sell_state, got %q", got.SellState)
	}
	if a.effectiveSell().SellState != api.SellSelling {
		t.Fatalf("must not flip to paused: %+v", a.effectiveSell())
	}
}

func TestSellPauseAlreadyPausedNoop(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.state.DeviceID = testDevice

	first := a.runJob(context.Background(), api.Job{
		JobID:    "j-1",
		Kind:     api.KindSellPause,
		DeviceID: testDevice,
	})
	second := a.runJob(context.Background(), api.Job{
		JobID:    "j-2",
		Kind:     api.KindSellPause,
		DeviceID: testDevice,
	})
	if !first.Passed || first.SellState != api.SellPaused {
		t.Fatalf("first %+v", first)
	}
	if !second.Passed || second.SellState != api.SellPaused || second.Reason != "sell_pause" {
		t.Fatalf("already paused must no-op pass: %+v", second)
	}
	if a.effectiveSell().SellState != api.SellPaused {
		t.Fatalf("still paused: %+v", a.effectiveSell())
	}
}

func TestLeaseStartRefusedWhilePaused(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var dockerCalls int
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.state.DeviceID = testDevice
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		dockerCalls++
		return []byte("cid\n"), nil
	}}
	pause := a.runJob(context.Background(), api.Job{
		JobID:    "j-pause",
		Kind:     api.KindSellPause,
		DeviceID: testDevice,
	})
	if !pause.Passed {
		t.Fatalf("pause %+v", pause)
	}
	got := a.runJob(context.Background(), api.Job{
		JobID:       "j-start",
		Kind:        api.KindLeaseStart,
		DeviceID:    testDevice,
		LeaseID:     "lease-b",
		CatalogID:   "llama-3.1-8b-q4",
		ImageDigest: sellTestDigest,
		Runtime:     "nvidia",
	})
	if got.Passed || got.Reason != "sell paused" {
		t.Fatalf("lease_start while paused: %+v", got)
	}
	if dockerCalls != 0 {
		t.Fatalf("must not start docker while paused, calls=%d", dockerCalls)
	}
}

func TestSellUntilExpiryReturnsSelling(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.state.DeviceID = testDevice
	now := time.Date(2026, 9, 7, 20, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-until",
		Kind:     api.KindSellPause,
		DeviceID: testDevice,
		Until:    "2026-09-07T21:00:00Z",
	})
	if !got.Passed || got.SellState != api.SellPaused {
		t.Fatalf("apply %+v", got)
	}
	if a.effectiveSell().SellState != api.SellPaused {
		t.Fatal("before until must stay paused")
	}

	now = time.Date(2026, 9, 7, 21, 0, 0, 0, time.UTC)
	if p := a.effectiveSell(); p.SellState != api.SellSelling || p.Until != "" {
		t.Fatalf("expiry must return selling: %+v", p)
	}
}

func TestSellPauseResultJSONIncludesSellStateNeverTrueNA(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.state.DeviceID = testDevice

	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-json",
		Kind:     api.KindSellPause,
		DeviceID: testDevice,
	})
	body := jobResultBody(api.KindSellPause, got)
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"sell_state":"paused"`) {
		t.Fatalf("sell_pause result JSON must include sell_state: %s", raw)
	}
	if body.Passed && body.SellState == api.SellNA {
		t.Fatalf("never passed=true with n/a: %+v", body)
	}
	if body.SellState == "" {
		t.Fatal("missing sell_state is invalid on sell_pause")
	}

	lease := jobResultBody(api.KindLeaseStart, jobOutcome{})
	leaseRaw, err := json.Marshal(lease)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(leaseRaw), "sell_state") {
		t.Fatalf("lease_start must not leak sell fields: %s", leaseRaw)
	}
}

func TestPathBUnchangedAndNotUsedAsSell(t *testing.T) {
	var mu sync.Mutex
	var posted map[string]any
	var hb api.HeartbeatRequest

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.EnrollResponse{DeviceID: testDevice, DeviceSecret: testSecret})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		var body api.HeartbeatRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		hb = body
		mu.Unlock()
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{
			DeviceID:   testDevice,
			PathBState: api.PathBGreen,
			SellState:  body.SellState,
			AcceptedAt: time.Now().UTC(),
		})
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
			JobID:    testJob,
			Kind:     api.KindPathB,
			DeviceID: testDevice,
			SuiteID:  "thin-v1",
		})
	})
	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/"+testJob+"/result", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		posted = body
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
	a.pathb.Endpoint = "http://127.0.0.1:1/v1/chat/completions"
	a.pathb.HTTP = &http.Client{Timeout: 150 * time.Millisecond}

	waitLoop(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return posted != nil
	})

	mu.Lock()
	defer mu.Unlock()
	passed, _ := posted["passed"].(bool)
	if !passed {
		t.Fatalf("path_b should still pass telem: %v", posted)
	}
	for _, extra := range []string{"sell_state", "until"} {
		if _, ok := posted[extra]; ok {
			t.Fatalf("path_b result must not include %s: %v", extra, posted)
		}
	}
	if hb.SellState != api.SellSelling {
		t.Fatalf("path_b must not invent paused sell_state, heartbeat %+v", hb)
	}
	if a.effectiveSell().SellState != api.SellSelling {
		t.Fatalf("path_b must not change sell posture: %+v", a.effectiveSell())
	}
}

func TestSellPauseDoesNotStopExistingLease(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var dockerCalls int
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.state.DeviceID = testDevice
	a.lease = &runtime.Handle{Name: "hypermesh-lease-1", LeaseID: "lease-1", ContainerID: "cid"}
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		dockerCalls++
		return []byte("ok\n"), nil
	}}
	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-pause",
		Kind:     api.KindSellPause,
		DeviceID: testDevice,
	})
	if !got.Passed || got.SellState != api.SellPaused {
		t.Fatalf("%+v", got)
	}
	if dockerCalls != 0 {
		t.Fatalf("sell_pause must not docker stop, calls=%d", dockerCalls)
	}
	if a.lease == nil || a.lease.LeaseID != "lease-1" {
		t.Fatalf("existing lease must stay: %+v", a.lease)
	}
}
