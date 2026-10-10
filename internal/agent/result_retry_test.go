package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

func TestPostResultRetriesAfterLeaseStart(t *testing.T) {
	const ref = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var mu sync.Mutex
	var posts, dockerRuns int

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.Job{
			JobID:       testJob,
			Kind:        api.KindLeaseStart,
			DeviceID:    testDevice,
			LeaseID:     "lease-retry",
			CatalogID:   "llama-3.1-8b-q4",
			ImageDigest: ref,
			FileHash:    fixtureGGUFHash(),
			Runtime:     "nvidia",
		})
	})
	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/"+testJob+"/result", func(w http.ResponseWriter, r *http.Request) {
		var body api.JobResultRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		posts++
		n := posts
		mu.Unlock()
		if n < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if !body.Passed || body.ImageHash == ref {
			t.Errorf("retry must POST GGUF hash, got %+v", body)
		}
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, srv.URL, dir, uptime, "", "")
	wantHash := readyLeaseStart(t, a)
	a.state.DeviceID = testDevice
	a.client.DeviceSecret = testSecret
	a.resultTries = 3
	a.resultRetryWait = time.Millisecond
	a.sleep = func(time.Duration) {}
	a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "run" {
			mu.Lock()
			dockerRuns++
			mu.Unlock()
		}
		return []byte("cid-retry\n"), nil
	}}

	if err := a.pullAndRun(context.Background()); err != nil {
		t.Fatalf("third post must succeed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 3 {
		t.Fatalf("posts %d want 3", posts)
	}
	if dockerRuns < 1 {
		t.Fatal("expected one docker start")
	}
	if a.lease == nil || a.lease.FileHash != wantHash {
		t.Fatalf("handle must keep GGUF hash: %+v", a.lease)
	}
}

func TestFailedPostClearsBusy(t *testing.T) {
	var posts int
	var sawBusy bool
	mux := http.NewServeMux()
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.Job{
			JobID:    testJob,
			Kind:     "not-a-kind",
			DeviceID: testDevice,
		})
	})
	var agent *Agent
	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/"+testJob+"/result", func(w http.ResponseWriter, r *http.Request) {
		posts++
		if agent != nil && agent.Busy() {
			sawBusy = true
		}
		w.WriteHeader(http.StatusBadGateway)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	agent = newTestAgent(t, srv.URL, dir, uptime, "", "")
	agent.state.DeviceID = testDevice
	agent.client.DeviceSecret = testSecret
	agent.resultTries = 3
	agent.resultRetryWait = time.Millisecond
	agent.sleep = func(time.Duration) {}
	if err := agent.pullAndRun(context.Background()); err == nil {
		t.Fatal("exhausted result posts must fail")
	}
	if posts != 3 {
		t.Fatalf("posts %d want 3", posts)
	}
	if !sawBusy {
		t.Fatal("host was not busy while the result post was retrying")
	}
	if agent.Busy() {
		t.Fatal("exhausted result posts left the host busy")
	}
}

func TestFailedPostResultDoesNotStartSecondContainer(t *testing.T) {
	const ref = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var mu sync.Mutex
	var posts, dockerRuns int

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.Job{
			JobID:       testJob,
			Kind:        api.KindLeaseStart,
			DeviceID:    testDevice,
			LeaseID:     "lease-stuck",
			CatalogID:   "llama-3.1-8b-q4",
			ImageDigest: ref,
			FileHash:    fixtureGGUFHash(),
			Runtime:     "nvidia",
		})
	})
	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/"+testJob+"/result", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		posts++
		n := posts
		mu.Unlock()
		if n <= 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, srv.URL, dir, uptime, "", "")
	wantHash := readyLeaseStart(t, a)
	a.state.DeviceID = testDevice
	a.client.DeviceSecret = testSecret
	a.resultTries = 3
	a.resultRetryWait = time.Millisecond
	a.sleep = func(time.Duration) {}
	a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "run" {
			mu.Lock()
			dockerRuns++
			mu.Unlock()
		}
		return []byte("cid-stuck\n"), nil
	}}

	if err := a.pullAndRun(context.Background()); err == nil {
		t.Fatal("exhausted retries must fail the pull")
	}
	if a.localLease() == nil {
		t.Fatal("successful start must keep the handle after a failed POST")
	}
	if err := a.pullAndRun(context.Background()); err != nil {
		t.Fatalf("same lease retry must POST without a second start: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if dockerRuns != 1 {
		t.Fatalf("exclusive writer: docker starts %d want 1", dockerRuns)
	}
	if posts != 4 {
		t.Fatalf("posts %d want 3 fail + 1 success", posts)
	}
	if a.lease == nil || a.lease.FileHash != wantHash {
		t.Fatalf("handle %+v want hash %q", a.lease, wantHash)
	}
}
