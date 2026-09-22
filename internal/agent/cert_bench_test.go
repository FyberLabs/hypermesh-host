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

func TestPathBStartsTheInspectedImageAndPostsTheSample(t *testing.T) {
	const pin = "ghcr.io/fyberlabs/hypermesh-test@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var mu sync.Mutex
	var score api.ScorecardReport
	var posted bool
	var cmds []string

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.Job{
			JobID: testJob, Kind: api.KindPathB, DeviceID: testDevice, SuiteID: "thin-v1",
		})
	})
	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/"+testJob+"/result", func(w http.ResponseWriter, r *http.Request) {
		var body api.JobResultRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.ImageHash == pin {
			t.Errorf("path_b image_hash must stay telem, got %q", body.ImageHash)
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(api.PathScorecard, func(w http.ResponseWriter, r *http.Request) {
		var body api.ScorecardReport
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		score = body
		posted = true
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	plane := httptest.NewServer(mux)
	t.Cleanup(plane.Close)
	completions := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "ready"}}},
			"usage":   map[string]any{"completion_tokens": 4},
		})
	}))
	t.Cleanup(completions.Close)

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	gguf := filepath.Join(dir, "model.gguf")
	_ = os.WriteFile(gguf, []byte("weights"), 0o644)
	a := newTestAgent(t, plane.URL, dir, uptime, "", "")
	a.state.DeviceID = testDevice
	a.client.DeviceSecret = testSecret
	a.cfg.GGUFPath = gguf
	a.pathb.Endpoint = completions.URL
	a.inspectCatalog = func(context.Context, string) string { return pin }
	a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 {
			mu.Lock()
			cmds = append(cmds, args[0])
			mu.Unlock()
		}
		return []byte("cid-cert\n"), nil
	}}

	if err := a.pullAndRun(context.Background()); err != nil {
		t.Fatalf("pull: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !posted {
		t.Fatal("measured cert must post a scorecard")
	}
	if score.ImageDigest != pin || score.DecodeTokS <= 0 || score.SampleMs < 1 {
		t.Fatalf("scorecard %+v", score)
	}
	if score.ImageHash == pin || score.ImageHash == "" {
		t.Fatalf("image_hash %q", score.ImageHash)
	}
	if a.lease != nil {
		t.Fatal("cert container must not become the lease")
	}
	if !strings.Contains(strings.Join(cmds, " "), "run") || !strings.Contains(strings.Join(cmds, " "), "stop") {
		t.Fatalf("docker cmds %v", cmds)
	}
}

func TestPathBSkipDoesNotPostAScorecard(t *testing.T) {
	const pin = "ghcr.io/fyberlabs/hypermesh-test@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var posts int
	mux := http.NewServeMux()
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.Job{
			JobID: testJob, Kind: api.KindPathB, DeviceID: testDevice, SuiteID: "thin-v1",
		})
	})
	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/"+testJob+"/result", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(api.PathScorecard, func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.WriteHeader(http.StatusOK)
	})
	plane := httptest.NewServer(mux)
	t.Cleanup(plane.Close)
	completions := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(completions.Close)

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	gguf := filepath.Join(dir, "model.gguf")
	_ = os.WriteFile(gguf, []byte("weights"), 0o644)
	a := newTestAgent(t, plane.URL, dir, uptime, "", "")
	a.state.DeviceID = testDevice
	a.client.DeviceSecret = testSecret
	a.cfg.GGUFPath = gguf
	a.pathb.Endpoint = completions.URL
	a.certReadyWait = 20 * time.Millisecond
	a.sleep = func(time.Duration) {}
	a.inspectCatalog = func(context.Context, string) string { return pin }
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("cid-cert\n"), nil
	}}

	if err := a.pullAndRun(context.Background()); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if posts != 0 {
		t.Fatalf("inference skip must not post a scorecard, posts=%d", posts)
	}
}
