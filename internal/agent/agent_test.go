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
	"github.com/FyberLabs/hypermesh-host/internal/telem"
)

func TestLoopEnrollHeartbeat204ThenJob(t *testing.T) {
	const tenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const device = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	const jobID = "cccccccc-cccc-cccc-cccc-cccccccccccc"

	var mu sync.Mutex
	var enrolls, heartbeats, pulls, results int
	var enrollBody api.EnrollRequest
	var seenAuth string

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		enrolls++
		_ = json.NewDecoder(r.Body).Decode(&enrollBody)
		seenAuth = r.Header.Get("Authorization")
		mu.Unlock()
		writeJSON(w, http.StatusOK, api.EnrollResponse{DeviceID: device, CredentialPrefix: "cafe0000"})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		heartbeats++
		mu.Unlock()
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{
			DeviceID:   device,
			PathBState: api.PathBNeverRun,
			AcceptedAt: time.Now().UTC(),
		})
	})
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		pulls++
		n := pulls
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, api.CertJob{
			ID:        jobID,
			TenantID:  tenant,
			DeviceID:  device,
			SuiteID:   "stub-suite-v1",
			Kind:      api.KindPathB,
			Status:    api.StatusPending,
			CreatedAt: time.Now().UTC(),
		})
	})
	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/"+jobID+"/result", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		results++
		mu.Unlock()
		var body api.JobResultRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Passed {
			t.Errorf("stub should pass, got %+v", body)
		}
		now := time.Now().UTC()
		writeJSON(w, http.StatusOK, api.CertJob{
			ID:          jobID,
			Status:      api.StatusPassed,
			SuiteID:     "stub-suite-v1",
			CompletedAt: &now,
			ImageHash:   body.ImageHash,
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("3.0 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		ControlPlaneURL: srv.URL,
		TenantID:        tenant,
		EnrollToken:     "one-time-token",
		StateDir:        dir,
		HeartbeatEvery:  40 * time.Millisecond,
		JobEvery:        40 * time.Millisecond,
		ImageHash:       "sha256:fixture",
	}
	a, err := New(cfg, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	a.telem = telem.Source{
		UptimePath:    uptime,
		ImageHashFile: filepath.Join(dir, "no-such-hash"),
		LookPath:      func(string) (string, error) { return "", os.ErrNotExist },
	}
	a.pathb.Collector = a.telem

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		ok := enrolls == 1 && heartbeats >= 1 && pulls >= 2 && results >= 1
		mu.Unlock()
		if ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if enrolls != 1 {
		t.Fatalf("enrolls %d", enrolls)
	}
	if heartbeats < 1 || pulls < 2 || results < 1 {
		t.Fatalf("hb=%d pulls=%d results=%d", heartbeats, pulls, results)
	}
	if enrollBody.EnrollToken != "one-time-token" {
		t.Fatalf("enroll body token missing")
	}
	if seenAuth != "" {
		t.Fatalf("must not invent Bearer, got %q", seenAuth)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "device.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "one-time-token") {
		t.Fatal("enroll token written to state")
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.DeviceID != device || st.CredentialPrefix != "cafe0000" {
		t.Fatalf("state %+v", st)
	}
}

func TestSkipsEnrollWhenDeviceOnDisk(t *testing.T) {
	const tenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const device = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("enroll must not run when device_id is on disk")
		w.WriteHeader(http.StatusConflict)
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{
			DeviceID:   device,
			PathBState: api.PathBGreen,
			AcceptedAt: time.Now().UTC(),
		})
	})
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "device.json"), []byte(`{"device_id":"`+device+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "uptime"), []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		ControlPlaneURL: srv.URL,
		TenantID:        tenant,
		EnrollToken:     "must-not-be-sent",
		StateDir:        dir,
		HeartbeatEvery:  time.Hour,
		JobEvery:        time.Hour,
	}
	a, err := New(cfg, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	a.telem = telem.Source{
		UptimePath: filepath.Join(dir, "uptime"),
		LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
	}
	a.pathb.Collector = a.telem

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
}

func TestValidateTenant(t *testing.T) {
	cfg := FromEnv()
	cfg.TenantID = "not-a-uuid"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected tenant uuid error")
	}
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
