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
)

// synthDigest is a fake repo@digest used only in httptest. Not GHCR.
const synthDigest = "example.invalid/hypermesh/workload@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// TestSyntheticAgentLoop is the hermetic enroll → heartbeat → path_b →
// lease_start → lease_stop loop against an httptest control plane.
// Fake NVIDIA{Run} records docker-like argv. No real docker, no AGX,
// no nvidia-smi, no GHCR pull.
func TestSyntheticAgentLoop(t *testing.T) {
	var mu sync.Mutex
	var enrolls int
	var enrollAuth string
	var hbRaws [][]byte
	var results []synthResult
	var dockerCalls [][]string
	var sawOmitempty, sawLoaded, ownerUp bool
	pathbJob := api.Job{JobID: "syn-pathb", Kind: api.KindPathB, DeviceID: testDevice, SuiteID: "thin-v1"}
	startJob := api.Job{
		JobID: "syn-start", Kind: api.KindLeaseStart, DeviceID: testDevice,
		LeaseID: "lease-syn", CatalogID: "llama-3.1-8b-q4",
		ImageDigest: synthDigest, Runtime: "nvidia",
	}
	stopJob := api.Job{JobID: "syn-stop", Kind: api.KindLeaseStop, DeviceID: testDevice, LeaseID: "lease-syn"}
	issued := map[string]bool{}

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		enrolls++
		enrollAuth = r.Header.Get("Authorization")
		mu.Unlock()
		writeJSON(w, http.StatusOK, api.EnrollResponse{
			DeviceID:         testDevice,
			CredentialPrefix: "cafe0000",
			DeviceSecret:     testSecret,
		})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("heartbeat body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var hb api.HeartbeatRequest
		_ = json.Unmarshal(raw, &hb)
		mu.Lock()
		hbRaws = append(hbRaws, raw)
		if hb.LoadedModelHash == "" && !strings.Contains(string(raw), "loaded_model_hash") {
			sawOmitempty = true
		}
		if hb.LoadedModelHash == synthDigest {
			sawLoaded = true
		}
		mu.Unlock()
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{
			DeviceID:   testDevice,
			PathBState: api.PathBNeverRun,
			AcceptedAt: time.Now().UTC(),
		})
	})
	acceptTunnelPeer(mux)
	acceptTunnelAck(mux)
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, allocatedTunnel("lease-syn"))
	})
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		next, ok := nextSynthJob(issued, results, sawLoaded, pathbJob, startJob, stopJob)
		if !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		issued[next.JobID] = true
		writeJSON(w, http.StatusOK, next)
	})
	for _, id := range []string{"syn-pathb", "syn-start", "syn-stop"} {
		jobID := id
		mux.HandleFunc("/api/v1/hypermesh/agent/jobs/"+jobID+"/result", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+testSecret {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("result body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var body api.JobResultRequest
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("result json: %v", err)
			}
			mu.Lock()
			results = append(results, synthResult{ID: jobID, Raw: raw, Body: body})
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		})
	}

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("3.0 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var logBuf strings.Builder
	a := newTestAgent(t, srv.URL, dir, uptime, "one-time-token", "")
	wantGGUF := readyLeaseStart(t, a)
	a.log = slog.New(slog.NewTextHandler(&logBuf, nil))
	a.tunnels = &fakeTunnel{}
	a.pathb.Endpoint = "http://127.0.0.1:1/v1/chat/completions"
	a.pathb.HTTP = &http.Client{Timeout: 150 * time.Millisecond}
	a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		mu.Lock()
		dockerCalls = append(dockerCalls, append([]string{}, args...))
		mu.Unlock()
		return []byte("cid-synth\n"), nil
	}}

	waitLoop(t, a, func() bool {
		if _, err := os.Stat(ownerSockPath(dir)); err == nil {
			mu.Lock()
			ownerUp = true
			mu.Unlock()
		}
		mu.Lock()
		defer mu.Unlock()
		return enrolls == 1 && len(results) >= 3 && sawOmitempty && sawLoaded
	})

	mu.Lock()
	defer mu.Unlock()

	if enrollAuth != "" {
		t.Fatalf("enroll sent Authorization %q", enrollAuth)
	}
	if enrolls != 1 {
		t.Fatalf("enrolls %d", enrolls)
	}

	byID := map[string]synthResult{}
	for _, r := range results {
		byID[r.ID] = r
	}
	pathbRes, ok := byID["syn-pathb"]
	if !ok {
		t.Fatal("missing path_b result")
	}
	if !pathbRes.Body.Passed || strings.TrimSpace(pathbRes.Body.ImageHash) == "" {
		t.Fatalf("path_b must pass with non-empty image_hash: %+v", pathbRes.Body)
	}
	if pathbRes.Body.ImageHash != "sha256:fixture" {
		t.Fatalf("path_b must use host/telem hash, not invent: %q", pathbRes.Body.ImageHash)
	}

	startRes, ok := byID["syn-start"]
	if !ok {
		t.Fatal("missing lease_start result")
	}
	if !startRes.Body.Passed || startRes.Body.ImageHash != wantGGUF {
		t.Fatalf("lease_start must POST the GGUF hash: %+v want %q", startRes.Body, wantGGUF)
	}
	if startRes.Body.ImageHash == synthDigest {
		t.Fatal("must not echo the OCI pin as image_hash")
	}

	stopRes, ok := byID["syn-stop"]
	if !ok {
		t.Fatal("missing lease_stop result")
	}
	if !stopRes.Body.Passed {
		t.Fatalf("lease_stop match must pass: %+v", stopRes.Body)
	}
	if stopRes.Body.ImageHash != synthDigest && stopRes.Body.ImageHash != "sha256:fixture" {
		t.Fatalf("lease_stop must not invent a hash: %q", stopRes.Body.ImageHash)
	}

	for _, r := range results {
		s := string(r.Raw)
		if strings.Contains(s, "sell_state") || strings.Contains(s, "reason") || strings.Contains(s, "reason_code") {
			t.Fatalf("%s result shape must stay {passed, image_hash?}: %s", r.ID, s)
		}
		if strings.Contains(s, "ghcr.io") {
			t.Fatalf("%s used GHCR: %s", r.ID, s)
		}
	}

	if len(hbRaws) < 2 {
		t.Fatal("need heartbeat before lease and after lease_start")
	}
	firstHB := decodeHeartbeat(t, hbRaws[0])
	if firstHB.LoadedModelHash != "" {
		t.Fatalf("first heartbeat must omit loaded_model_hash, got %q", firstHB.LoadedModelHash)
	}
	if strings.Contains(string(hbRaws[0]), "loaded_model_hash") {
		t.Fatalf("omitempty must drop loaded_model_hash before lease: %s", hbRaws[0])
	}

	sawAfterLease := false
	for _, raw := range hbRaws[1:] {
		hb := decodeHeartbeat(t, raw)
		if hb.LoadedModelHash == synthDigest {
			sawAfterLease = true
			break
		}
	}
	if !sawAfterLease {
		t.Fatalf("heartbeat after lease_start must set loaded_model_hash=%q, got %s", synthDigest, formatHBRaws(hbRaws))
	}

	if len(dockerCalls) < 4 {
		t.Fatalf("expected pull/run/stop/rm, calls=%v", dockerCalls)
	}
	if dockerCalls[0][0] != "pull" || dockerCalls[0][1] != synthDigest {
		t.Fatalf("pull %v", dockerCalls[0])
	}
	runArgs := findDockerRun(dockerCalls)
	if runArgs == nil {
		t.Fatalf("missing docker run: %v", dockerCalls)
	}
	runJoined := strings.Join(runArgs, " ")
	if !strings.Contains(runJoined, "--runtime=nvidia") || strings.Contains(runJoined, "--gpus") {
		t.Fatalf("run %v", runArgs)
	}
	if !strings.Contains(runJoined, synthDigest) || strings.Contains(runJoined, "ghcr.io") {
		t.Fatalf("run ref %v", runArgs)
	}
	if strings.Contains(runJoined, "--network=host") || !strings.Contains(runJoined, runtime.WorkloadNetworkName) {
		t.Fatalf("workload jail network: %v", runArgs)
	}
	if !strings.Contains(runJoined, "10.77.0.2:8080:8080") {
		t.Fatalf("publish must be WG IP: %v", runArgs)
	}
	stopIdx, rmIdx := -1, -1
	for i, c := range dockerCalls {
		if len(c) > 0 && c[0] == "stop" {
			stopIdx = i
		}
		if len(c) > 0 && c[0] == "rm" {
			rmIdx = i
		}
	}
	if stopIdx < 0 {
		t.Fatalf("stop missing: %v", dockerCalls)
	}
	if rmIdx < 0 || dockerCalls[rmIdx][len(dockerCalls[rmIdx])-1] != "hypermesh-lease-syn" {
		t.Fatalf("rm named container %v", dockerCalls)
	}

	if !ownerUp {
		t.Fatal("Run must listen on owner.sock during the loop")
	}

	logs := logBuf.String()
	assertHermeticLogs(t, logs, testSecret, "one-time-token")
	for _, r := range results {
		assertHermeticLogs(t, string(r.Raw), testSecret, "one-time-token")
	}
	for _, raw := range hbRaws {
		assertHermeticLogs(t, string(raw), testSecret, "one-time-token")
	}
}

type synthResult struct {
	ID   string
	Raw  []byte
	Body api.JobResultRequest
}

func nextSynthJob(issued map[string]bool, results []synthResult, sawLoaded bool, pathb, start, stop api.Job) (api.Job, bool) {
	has := map[string]bool{}
	for _, r := range results {
		has[r.ID] = true
	}
	switch {
	case !issued[pathb.JobID]:
		return pathb, true
	case !has[pathb.JobID]:
		return api.Job{}, false
	case !issued[start.JobID]:
		return start, true
	case !has[start.JobID] || !sawLoaded:
		// Hold lease_stop until a heartbeat carries the running digest.
		return api.Job{}, false
	case !issued[stop.JobID]:
		return stop, true
	default:
		return api.Job{}, false
	}
}

func decodeHeartbeat(t *testing.T, raw []byte) api.HeartbeatRequest {
	t.Helper()
	var hb api.HeartbeatRequest
	if err := json.Unmarshal(raw, &hb); err != nil {
		t.Fatalf("heartbeat json %s: %v", raw, err)
	}
	return hb
}

func formatHBRaws(raws [][]byte) string {
	var b strings.Builder
	for i, r := range raws {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.Write(r)
	}
	return b.String()
}

func assertHermeticLogs(t *testing.T, s string, extra ...string) {
	t.Helper()
	leaks := []string{
		"PEERPRIVATE",
		"peer_private_key",
		"device_secret",
		"hm_dev_",
		"Reply with the single word ready.",
		"Reply with",
		"ghcr.io",
		"nvidia-smi",
	}
	leaks = append(leaks, extra...)
	for _, leak := range leaks {
		if leak != "" && strings.Contains(s, leak) {
			t.Fatalf("forbidden %q in logs/payload: %q", leak, s)
		}
	}
}
