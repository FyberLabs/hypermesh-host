package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	"github.com/FyberLabs/hypermesh-host/internal/agent"
	"github.com/FyberLabs/hypermesh-host/internal/api"
)

func TestOutsideRestartsRuntimeAndDoesNotEnroll(t *testing.T) {
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
		Creds:     Credentials{APIKey: "hm_renter_secret"},
		Backoff:   time.Millisecond,
		HTTP:      srv.Client(),
		Agent:     agent.Config{ControlPlaneURL: srv.URL},
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
	loaded, err := LoadCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, credFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hm_renter_secret") || strings.Contains(string(raw), "api_key") {
		t.Fatalf("supervisor.json stored the renter key\n%s", raw)
	}
	if loaded.APIKey != "" || loaded.Placement != PlacementOutside {
		t.Fatalf("loaded %+v", loaded)
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

func TestHostPlacementEnrollsHeartbeatsAndRefusesLease(t *testing.T) {
	var mu sync.Mutex
	var enrolls, heartbeats int
	var enrollBody api.EnrollRequest
	var beatBody api.HeartbeatRequest
	var result api.JobResultRequest
	var resultN int

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		enrolls++
		_ = json.NewDecoder(r.Body).Decode(&enrollBody)
		writeJSON(w, api.EnrollResponse{
			DeviceID:         "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
			CredentialPrefix: "cafe",
			DeviceSecret:     "hm_dev_fixture_secret",
		})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		heartbeats++
		_ = json.NewDecoder(r.Body).Decode(&beatBody)
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
	if resultN < 1 || result.Passed {
		t.Fatalf("lease result n=%d passed=%v", resultN, result.Passed)
	}
	if result.ImageHash != "" {
		t.Fatalf("weight hash %q", result.ImageHash)
	}
	if enrollBody.HostType != agent.HostTypeSupervisor {
		t.Fatalf("enroll host_type %q", enrollBody.HostType)
	}
	if beatBody.Health == nil || beatBody.Health.HostType != agent.HostTypeSupervisor {
		t.Fatalf("heartbeat health %+v", beatBody.Health)
	}
	raw, err := os.ReadFile(filepath.Join(dir, credFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "one-time") || strings.Contains(string(raw), "enroll_token") {
		t.Fatalf("enroll token remained in supervisor.json\n%s", raw)
	}
}

func TestDockerfileIsOneImage(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "deploy", "supervisor", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if strings.Count(text, "\nFROM ")+boolTo(strings.HasPrefix(text, "FROM ")) != 2 {
		t.Fatalf("want a build stage and one runtime image\n%s", text)
	}
	if !strings.Contains(text, "hypermesh-supervisor") {
		t.Fatal("image does not carry hypermesh-supervisor")
	}
	if !strings.Contains(text, "golang:1.22-bookworm") {
		t.Fatal("build stage should stay on the golang bookworm image")
	}
	if !strings.Contains(text, "CGO_ENABLED=0") {
		t.Fatal("binary is not built with CGO_ENABLED=0")
	}
	if !strings.Contains(text, "alpine:") {
		t.Fatal("runtime image is not Alpine")
	}
	if strings.Contains(text, "ubuntu:") {
		t.Fatal("runtime image is still Ubuntu")
	}
	if !strings.Contains(text, PlacementOutside) || !strings.Contains(text, PlacementHost) || !strings.Contains(text, PlacementRenter) {
		t.Fatal("image comment should name the three placements")
	}
}

func TestSecretsStayOutOfSupervisorJSON(t *testing.T) {
	dir := t.TempDir()
	legacy := "{\n  \"placement\": \"outside\",\n  \"enroll_token\": \"legacy-token\",\n  \"api_key\": \"hm_renter_secret\"\n}\n"
	if err := os.WriteFile(filepath.Join(dir, credFile), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Supervisor{
		Placement: PlacementOutside,
		StateDir:  dir,
		Creds:     Credentials{APIKey: "hm_renter_secret"},
		Backoff:   time.Hour,
		Runtime: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	var raw []byte
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(filepath.Join(dir, credFile))
		if err == nil && strings.Contains(string(b), "legacy-token") && !strings.Contains(string(b), "api_key") {
			raw = b
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if raw == nil {
		t.Fatal("legacy enroll token was not kept until enroll succeeded")
	}
	if strings.Contains(string(raw), "hm_renter_secret") {
		t.Fatalf("api key rewritten into supervisor.json\n%s", raw)
	}
	secret := `{"device_id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb","device_secret":"hm_dev_fixture"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, deviceFile), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCredentials(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, credFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "legacy-token") || strings.Contains(string(raw), "enroll_token") {
		t.Fatalf("enroll token remained after enroll\n%s", raw)
	}
	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor did not stop")
	}
}

func TestRenterKeyFromEnvironmentOrCredential(t *testing.T) {
	t.Setenv("HYPERMESH_API_KEY", "hm_from_env")
	t.Setenv("HYPERMESH_ENROLL_TOKEN", "env-token")
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	s := &Supervisor{
		Placement: PlacementOutside,
		StateDir:  dir,
		Backoff:   time.Hour,
		Runtime: func(context.Context) error {
			cancel()
			return errors.New("stop")
		},
	}
	if err := s.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("run: %v", err)
	}
	if s.Creds.APIKey != "hm_from_env" || s.Creds.EnrollToken != "env-token" {
		t.Fatalf("memory creds %+v", s.Creds)
	}
	raw, err := os.ReadFile(filepath.Join(dir, credFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hm_from_env") || strings.Contains(string(raw), "env-token") {
		t.Fatalf("env secrets were written into supervisor.json\n%s", raw)
	}

	credDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(credDir, credAPIKey), []byte("hm_from_cred\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HYPERMESH_API_KEY", "")
	t.Setenv("CREDENTIALS_DIRECTORY", credDir)
	got, err := readCredential("HYPERMESH_API_KEY", credAPIKey)
	if err != nil || got != "hm_from_cred" {
		t.Fatalf("credential %q %v", got, err)
	}
}

func TestListenHandlerForwardsCallerAPIKeyWithSupervisorLease(t *testing.T) {
	const fixtureKey = "org_fixture_ok"
	var gotPath, gotKey, gotLease, gotLeaseAlt, gotTenant, gotBody string
	var hits int
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotPath = r.URL.Path
		gotKey = r.Header.Get("X-Api-Key")
		gotLease = r.Header.Get("X-Hypermesh-Lease-Id")
		gotLeaseAlt = r.Header.Get("X-Lease-Id")
		gotTenant = r.Header.Get("X-Tenant-ID")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"echo org_fixture_ok"}}]}`))
	}))
	defer gw.Close()

	var logs bytes.Buffer
	s := &Supervisor{
		Placement: PlacementRenter,
		HTTP:      gw.Client(),
		Log:       slog.New(slog.NewTextHandler(&logs, nil)),
		Creds: Credentials{
			APIKey:   "stored_fixture_key",
			TenantID: "8f8e8d8c-7b6a-4a59-8c3d-2e1f0a9b8c7d",
			LeaseID:  "lease_1",
			ChatBase: gw.URL,
		},
	}
	req := httptest.NewRequest(http.MethodPost, ChatCompletionsPath, strings.NewReader(`{"messages":[{"role":"user","content":"count the sheep"}]}`))
	req.Header.Set("X-Api-Key", fixtureKey)
	req.Header.Set("X-Lease-Id", "caller_lease")
	req.Header.Set("X-Hypermesh-Lease-Id", "caller_hm_lease")
	req.Header.Set("X-Tenant-ID", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if hits != 1 {
		t.Fatalf("upstream hits %d", hits)
	}
	if gotPath != ChatCompletionsPath {
		t.Fatalf("path %s", gotPath)
	}
	if gotKey != fixtureKey {
		t.Fatalf("upstream key %q", gotKey)
	}
	if gotKey == s.Creds.APIKey {
		t.Fatal("forward used the stored renter key")
	}
	if gotLease != "lease_1" || gotLeaseAlt != "lease_1" {
		t.Fatalf("lease headers hypermesh=%q lease=%q", gotLease, gotLeaseAlt)
	}
	if gotTenant != s.Creds.TenantID {
		t.Fatalf("tenant %q", gotTenant)
	}
	if strings.Contains(gotBody, fixtureKey) || strings.Contains(gotBody, "llama-3.1-8b-q4") {
		t.Fatalf("forwarded body %s", gotBody)
	}
	if !strings.Contains(gotBody, "count the sheep") {
		t.Fatalf("forwarded body %s", gotBody)
	}
	if strings.Contains(rec.Body.String(), fixtureKey) || strings.Contains(rec.Body.String(), "stored_fixture_key") {
		t.Fatalf("response leaked a key\n%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "echo ***") {
		t.Fatalf("response %s", rec.Body.String())
	}
	if strings.Contains(logs.String(), fixtureKey) || strings.Contains(logs.String(), "stored_fixture_key") {
		t.Fatalf("logs leaked a key\n%s", logs.String())
	}
	if s.Creds.APIKey != "stored_fixture_key" {
		t.Fatalf("stored key was replaced: %q", s.Creds.APIKey)
	}
}

func TestListenHandlerRejectsMissingOrForbiddenAPIKey(t *testing.T) {
	hits := 0
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer gw.Close()

	var logs bytes.Buffer
	s := &Supervisor{
		Placement: PlacementRenter,
		HTTP:      gw.Client(),
		Log:       slog.New(slog.NewTextHandler(&logs, nil)),
		Creds: Credentials{
			APIKey:   "stored_fixture_key",
			TenantID: "8f8e8d8c-7b6a-4a59-8c3d-2e1f0a9b8c7d",
			LeaseID:  "lease_1",
			ChatBase: gw.URL,
		},
	}

	cases := []struct {
		name   string
		set    bool
		key    string
		want   string
		absent string
	}{
		{name: "missing", want: "api key is required"},
		{name: "empty", set: true, key: "", want: "api key is required"},
		{name: "blank", set: true, key: "   ", want: "api key is required"},
		{name: "dev", set: true, key: "hm_dev_fixture_tail", want: "hm_dev_ is not a renter identity", absent: "fixture_tail"},
		{name: "router", set: true, key: "hm_rtr_fixture_tail", want: "hm_rtr_ is not a renter identity", absent: "fixture_tail"},
		{name: "site", set: true, key: "hm_site_fixture_tail", want: "hm_site_ is not a renter identity", absent: "fixture_tail"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, ChatCompletionsPath, strings.NewReader(`{"messages":[{"role":"user","content":"count the sheep"}]}`))
			if tc.set {
				req.Header.Set("X-Api-Key", tc.key)
			}
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("body %q", rec.Body.String())
			}
			if tc.absent != "" && strings.Contains(rec.Body.String(), tc.absent) {
				t.Fatalf("response contained %q\n%s", tc.absent, rec.Body.String())
			}
			if tc.key != "" && strings.Contains(rec.Body.String(), tc.key) {
				t.Fatalf("response contained the key\n%s", rec.Body.String())
			}
		})
	}
	if hits != 0 {
		t.Fatalf("upstream was called %d times", hits)
	}
	if strings.Contains(logs.String(), "fixture_tail") || strings.Contains(logs.String(), "stored_fixture_key") {
		t.Fatalf("logs leaked a key\n%s", logs.String())
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
