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

// TestHeartbeatCadenceDuringLongJob is the incident: a multi-gigabyte
// lab_pull used to run on the same select as the heartbeat, so the plane
// marked the host stale after 180s. Heartbeats must keep the configured
// cadence while that job is still inside fetch, and loaded_model_hash
// must stay the running lease.
func TestHeartbeatCadenceDuringLongJob(t *testing.T) {
	const digest = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	interval := 40 * time.Millisecond

	var mu sync.Mutex
	var beats []time.Time
	var hashes []string
	var results int
	jobEntered := make(chan struct{})
	releaseJob := make(chan struct{})
	var once sync.Once

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		var body api.HeartbeatRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		beats = append(beats, time.Now())
		hashes = append(hashes, body.LoadedModelHash)
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
		mu.Lock()
		defer mu.Unlock()
		if results > 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, api.Job{
			JobID:       "lab-pull-long",
			Kind:        api.KindLabPull,
			DeviceID:    testDevice,
			CatalogID:   "qwen2.5-32b",
			ImageDigest: "sha256:" + strings.Repeat("ab", 32),
			SourceRef:   "bartowski/Qwen2.5-32B-Instruct-GGUF@main/qwen2.5-32b-instruct-q4_k_m.gguf",
			SizeBytes:   1024,
		})
	})
	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/lab-pull-long/result", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		results++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "device.json"), []byte(`{"device_id":"`+testDevice+`","device_secret":"`+testSecret+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("10.0 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, srv.URL, dir, uptime, "", "")
	a.cfg.HeartbeatEvery = interval
	a.cfg.JobEvery = 20 * time.Millisecond
	a.tunnels = &fakeTunnel{}
	if err := a.saveLease(&runtime.Handle{LeaseID: "lease-live", ImageDigest: digest}); err != nil {
		t.Fatal(err)
	}
	a.statFree = func(string) (int64, error) { return 1 << 40, nil }
	a.cfg.GGUFPath = filepath.Join(dir, "models", "model.gguf")
	a.fetchWeights = func(ctx context.Context, _, _, _ string) error {
		once.Do(func() { close(jobEntered) })
		select {
		case <-releaseJob:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// The success path waits exactly HeartbeatEvery and does not take the
	// failure-hold wake. Driving that wait from the test counts beats while
	// fetch is still blocked, without a wall-clock sleep that loses the race
	// when heartbeat work (health, disk, cache) exceeds a few intervals.
	type hbWaitCall struct {
		d         time.Duration
		interrupt bool
	}
	waiting := make(chan hbWaitCall)
	releaseWait := make(chan struct{})
	a.hbWait = func(ctx context.Context, d time.Duration, interrupt bool) bool {
		select {
		case <-ctx.Done():
			return false
		case waiting <- hbWaitCall{d: d, interrupt: interrupt}:
		}
		select {
		case <-ctx.Done():
			return false
		case <-releaseWait:
			return true
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("agent did not stop")
		}
	})

	select {
	case <-jobEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("long job did not start")
	}

	mu.Lock()
	base := len(beats)
	mu.Unlock()

	const want = 4
	releaseInterval := func(i int) {
		t.Helper()
		select {
		case call := <-waiting:
			if call.d != interval || call.interrupt {
				t.Fatalf("heartbeat wait %d = %s interrupt %v, want %s on the success path", i, call.d, call.interrupt, interval)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("heartbeat %d did not reach its interval while the job was blocked", i)
		}
		select {
		case releaseWait <- struct{}{}:
		case <-time.After(2 * time.Second):
			t.Fatalf("heartbeat %d did not leave its interval", i)
		}
	}
	for i := 0; i < want; i++ {
		releaseInterval(i)
	}
	// The last release starts another beat. Parking on the next wait means
	// that beat has already been recorded, and the job is still in fetch.
	select {
	case call := <-waiting:
		if call.d != interval || call.interrupt {
			t.Fatalf("heartbeat wait %d = %s interrupt %v, want %s on the success path", want, call.d, call.interrupt, interval)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("heartbeat %d did not reach its interval while the job was blocked", want)
	}

	mu.Lock()
	extra := len(beats) - base
	during := append([]string(nil), hashes[base:]...)
	finished := results
	mu.Unlock()

	if finished != 0 {
		t.Fatalf("job result posted while fetch was still blocked")
	}
	if extra < want {
		t.Fatalf("heartbeats while job blocked: %d (base %d)", extra, base)
	}
	for _, h := range during {
		if h != digest {
			t.Fatalf("loaded_model_hash %q want %q while job runs", h, digest)
		}
	}

	close(releaseJob)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := results
		mu.Unlock()
		if n >= 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job result was not posted")
}
