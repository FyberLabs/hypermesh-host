package muse

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/agent"
	"github.com/FyberLabs/hypermesh-host/internal/api"
)

func TestOutsideDoesNotPersistSecrets(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var starts int
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	s := &Supervisor{
		Placement: PlacementOutside,
		StateDir:  dir,
		Creds: Credentials{
			APIKey:      "hm_renter_secret",
			EnrollToken: "one-time-token",
		},
		Backoff: time.Millisecond,
		HTTP:    srv.Client(),
		Agent:   agent.Config{ControlPlaneURL: srv.URL},
		Runtime: func(context.Context) error {
			starts++
			if starts >= 2 {
				cancel()
			}
			return errors.New("runtime exited")
		},
	}
	err := s.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run: %v", err)
	}
	if starts < 2 {
		t.Fatalf("restarts %d", starts)
	}
	if hits != 0 {
		t.Fatalf("outside placement called the plane %d times", hits)
	}
	info, err := os.Stat(filepath.Join(dir, credFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode %o", info.Mode().Perm())
	}
	raw, err := os.ReadFile(filepath.Join(dir, credFile))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "hm_renter_secret") || strings.Contains(text, "one-time-token") || strings.Contains(text, "api_key") || strings.Contains(text, "enroll_token") {
		t.Fatalf("secrets written to muse.json\n%s", text)
	}
	loaded, err := LoadCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.APIKey != "" || loaded.EnrollToken != "" || loaded.Placement != PlacementOutside {
		t.Fatalf("loaded %+v", loaded)
	}
}

func TestDiskTokenKeptUntilEnrollThenDropped(t *testing.T) {
	dir := t.TempDir()
	old := diskCreds{
		Placement:   PlacementHost,
		EnrollToken: "disk-token",
		APIKey:      "hm_renter_secret",
		TenantID:    "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
	}
	body, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, credFile), body, 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Supervisor{Placement: PlacementHost, StateDir: dir}
	if err := s.prepareState(); err != nil {
		t.Fatal(err)
	}
	if s.Creds.APIKey != "hm_renter_secret" || s.Creds.EnrollToken != "disk-token" {
		t.Fatalf("memory creds %+v", s.Creds)
	}
	raw, err := os.ReadFile(filepath.Join(dir, credFile))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "hm_renter_secret") || strings.Contains(text, "api_key") {
		t.Fatalf("renter key rewritten\n%s", text)
	}
	if !strings.Contains(text, "disk-token") {
		t.Fatalf("disk enroll token dropped before enroll\n%s", text)
	}
	s.dropEnrollToken()
	raw, err = os.ReadFile(filepath.Join(dir, credFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "disk-token") || strings.Contains(string(raw), "enroll_token") {
		t.Fatalf("enroll token kept after drop\n%s", raw)
	}
	if s.Creds.EnrollToken != "" {
		t.Fatal("memory token kept after drop")
	}
}

func TestRenterKeyFromEnvFileOrCredential(t *testing.T) {
	got, err := LoadRenterKey("from-env", "", "")
	if err != nil || got != "from-env" {
		t.Fatalf("env %q %v", got, err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	if err := os.WriteFile(path, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = LoadRenterKey("", path, "")
	if err != nil || got != "from-file" {
		t.Fatalf("file %q %v", got, err)
	}
	credDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(credDir, RenterCredential), []byte("from-cred\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = LoadRenterKey("", "", credDir)
	if err != nil || got != "from-cred" {
		t.Fatalf("credential %q %v", got, err)
	}
	if _, err := LoadRenterKey("from-env", path, ""); err == nil {
		t.Fatal("mismatched renter keys were accepted")
	}
	token, err := LoadEnrollToken("tok", "")
	if err != nil || token != "tok" {
		t.Fatalf("token %q %v", token, err)
	}
}

func TestRenterTalksToChatGatewayNotStub(t *testing.T) {
	var gotPath, gotKey, gotLease string
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("X-Api-Key")
		gotLease = r.Header.Get("X-Hypermesh-Lease-Id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer gw.Close()

	s := &Supervisor{
		Placement: PlacementRenter,
		HTTP:      gw.Client(),
		Creds: Credentials{
			APIKey:   "hm_renter_key",
			TenantID: "8f8e8d8c-7b6a-4a59-8c3d-2e1f0a9b8c7d",
			LeaseID:  "lease_1",
			ChatBase: gw.URL,
		},
	}
	raw, err := s.PostChat(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != ChatCompletionsPath {
		t.Fatalf("path %s", gotPath)
	}
	if gotKey != "hm_renter_key" || gotLease != "lease_1" {
		t.Fatalf("headers key=%q lease=%q", gotKey, gotLease)
	}
	if !strings.Contains(string(raw), "ok") {
		t.Fatalf("body %s", raw)
	}

	stubHit := false
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stubHit = true
		w.WriteHeader(http.StatusConflict)
	}))
	defer stub.Close()
	s.Creds.ChatBase = stub.URL + ControlPlaneStubPath
	if _, err := s.PostChat(context.Background(), "hi"); err == nil {
		t.Fatal("stub was accepted")
	}
	if stubHit {
		t.Fatal("control-plane stub was called")
	}
}

func TestHostPlacementReportsSupervisorAndDropsToken(t *testing.T) {
	var mu sync.Mutex
	var enrolls, heartbeats int
	var enrollHost, hbHost string
	var result api.JobResultRequest
	var resultN int

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		var req api.EnrollRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		enrolls++
		enrollHost = req.HostType
		mu.Unlock()
		writeJSON(w, api.EnrollResponse{
			DeviceID:         "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
			CredentialPrefix: "cafe",
			DeviceSecret:     "hm_dev_fixture_secret",
		})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		var req api.HeartbeatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		heartbeats++
		if req.Health != nil {
			hbHost = req.Health.HostType
		}
		mu.Unlock()
		writeJSON(w, api.HeartbeatResponse{DeviceID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", PathBState: api.PathBNeverRun})
	})
	mux.HandleFunc(api.PathTunnelPeer, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Tunnel not found"}`))
	})
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if resultN > 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, api.Job{
			JobID:       "job-lease",
			Kind:        api.KindLeaseStart,
			DeviceID:    "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
			LeaseID:     "lease-weight",
			CatalogID:   "llama-3.1-8b-q4",
			ImageDigest: "ghcr.io/fyberlabs/hypermesh-test@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Runtime:     "nvidia",
			Backend:     "cuda-jetson",
		})
	})
	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/job-lease/result", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewDecoder(r.Body).Decode(&result)
		resultN++
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Supervisor{
		Placement: PlacementHost,
		StateDir:  dir,
		Backoff:   time.Hour,
		Creds: Credentials{
			TenantID:    "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
			EnrollToken: "one-time",
			APIKey:      "hm_renter_secret",
		},
		Agent: agent.Config{
			ControlPlaneURL: srv.URL,
			TenantID:        "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
			EnrollToken:     "one-time",
			StateDir:        dir,
			HeartbeatEvery:  20 * time.Millisecond,
			JobEvery:        20 * time.Millisecond,
			ImageHash:       "sha256:fixture",
		},
		Runtime: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	errCh := make(chan error, 1)
	go func() { errCh <- s.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := enrolls >= 1 && heartbeats >= 1 && resultN >= 1
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if enrolls < 1 || heartbeats < 1 {
		t.Fatalf("enrolls %d heartbeats %d", enrolls, heartbeats)
	}
	if enrollHost != agent.HostTypeSupervisor || hbHost != agent.HostTypeSupervisor {
		t.Fatalf("enroll host_type %q heartbeat host_type %q", enrollHost, hbHost)
	}
	if resultN < 1 || result.Passed {
		t.Fatalf("lease result n=%d passed=%v", resultN, result.Passed)
	}
	if result.ImageHash != "" {
		t.Fatalf("weight hash %q", result.ImageHash)
	}
	raw, err := os.ReadFile(filepath.Join(dir, credFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "one-time") || strings.Contains(string(raw), "hm_renter_secret") || strings.Contains(string(raw), "enroll_token") || strings.Contains(string(raw), "api_key") {
		t.Fatalf("secrets left in muse.json\n%s", raw)
	}
}

func TestDockerfileIsAlpine(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "deploy", "muse", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if strings.Count(text, "\nFROM ")+boolTo(strings.HasPrefix(text, "FROM ")) != 2 {
		t.Fatalf("want a build stage and one runtime image\n%s", text)
	}
	if !strings.Contains(text, "hypermesh-muse") {
		t.Fatal("image does not carry hypermesh-muse")
	}
	if !strings.Contains(text, "alpine:") {
		t.Fatal("runtime image is not Alpine")
	}
	if strings.Contains(text, "ubuntu:") {
		t.Fatal("runtime image is still Ubuntu")
	}
	if !strings.Contains(text, "CGO_ENABLED=0") {
		t.Fatal("binary is not built with CGO_ENABLED=0")
	}
	if !strings.Contains(text, PlacementOutside) || !strings.Contains(text, PlacementHost) || !strings.Contains(text, PlacementRenter) {
		t.Fatal("image comment should name the three placements")
	}
	src, err := os.ReadFile("muse.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "Meta Muse") {
		t.Fatal("package comment still calls this a copy of Meta Muse")
	}
	mainSrc, err := os.ReadFile(filepath.Join("..", "..", "cmd", "hypermesh-muse", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	mainText := string(mainSrc)
	if strings.Contains(mainText, `"api-key",`) || strings.Contains(mainText, `"enroll-token",`) {
		t.Fatal("renter key or enroll token is a command-line flag")
	}
}

func TestPackageCommentDoesNotCertify(t *testing.T) {
	b, err := os.ReadFile("muse.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(b)), "certified") {
		t.Fatal("muse claims a host is certified")
	}
}

func boolTo(v bool) int {
	if v {
		return 1
	}
	return 0
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
