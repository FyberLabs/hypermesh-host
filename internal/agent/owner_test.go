package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

func TestOwnerTokenAndSockMode0600(t *testing.T) {
	a := newOwnerTestAgent(t)
	startTestOwner(t, a)

	for _, name := range []string{ownerTokenFile, ownerSockFile} {
		fi, err := os.Stat(filepath.Join(a.cfg.StateDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %o", name, fi.Mode().Perm())
		}
	}
	tok, err := os.ReadFile(ownerTokenPath(a.cfg.StateDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(string(tok))) < 64 {
		t.Fatalf("token too short: %q", tok)
	}
}

func TestOwnerUnauthorized401(t *testing.T) {
	a := newOwnerTestAgent(t)
	startTestOwner(t, a)

	cases := []struct {
		name   string
		header string
	}{
		{name: "missing"},
		{name: "wrong", header: "Bearer not-the-token"},
		{name: "not-bearer", header: "Basic abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := ownerDo(t, a.cfg.StateDir, http.MethodPost, "/v0/sell_pause", tc.header, map[string]any{})
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status %d", resp.StatusCode)
			}
			if raw, _ := os.ReadFile(ownerEffectsPath(a.cfg.StateDir)); len(raw) != 0 {
				t.Fatalf("401 must not write effects: %s", raw)
			}
		})
	}
}

func TestOwnerSellPauseViaSocketPausesAndEffects(t *testing.T) {
	a := newOwnerTestAgent(t)
	now := time.Date(2026, 9, 7, 22, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	startTestOwner(t, a)

	resp := ownerDoAuth(t, a, http.MethodPost, "/v0/sell_pause", map[string]any{
		"until": "2026-09-08T00:00:00Z",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var body ownerSellPauseResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Passed || body.SellState != api.SellPaused {
		t.Fatalf("response %+v", body)
	}
	if body.Passed && body.SellState == api.SellNA {
		t.Fatal("never passed=true with sell_state=n/a")
	}
	if body.Until != "2026-09-08T00:00:00Z" || body.DeviceID != testDevice {
		t.Fatalf("echo %+v", body)
	}
	if a.effectiveSell().SellState != api.SellPaused {
		t.Fatalf("sell.json not paused: %+v", a.effectiveSell())
	}

	line := lastEffectsLine(t, a.cfg.StateDir)
	if line.Tool != toolSellPause || !line.Passed || line.SellState != api.SellPaused {
		t.Fatalf("effects %+v", line)
	}
	if line.TS != "2026-09-07T22:00:00Z" {
		t.Fatalf("ts %q", line.TS)
	}
	if line.Until != "2026-09-08T00:00:00Z" || line.DeviceID != testDevice {
		t.Fatalf("effects fields %+v", line)
	}
	if line.Args["until"] != "2026-09-08T00:00:00Z" {
		t.Fatalf("args %+v", line.Args)
	}
}

func TestOwnerSellPauseForeignDeviceRejected(t *testing.T) {
	a := newOwnerTestAgent(t)
	startTestOwner(t, a)

	resp := ownerDoAuth(t, a, http.MethodPost, "/v0/sell_pause", map[string]any{
		"device_id": "dddddddd-dddd-dddd-dddd-dddddddddddd",
		"until":     "2026-09-08T00:00:00Z",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var body ownerSellPauseResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Passed || body.SellState != api.SellSelling {
		t.Fatalf("foreign device must fail: %+v", body)
	}
	if a.effectiveSell().SellState != api.SellSelling {
		t.Fatalf("must not pause: %+v", a.effectiveSell())
	}
	line := lastEffectsLine(t, a.cfg.StateDir)
	if line.Tool != toolSellPause || line.Passed {
		t.Fatalf("effects %+v", line)
	}
}

func TestOwnerLeaseStopMatchMismatchSameAsH1(t *testing.T) {
	const localID = "lease-running"
	handle := runtime.Handle{
		Name:        "hypermesh-lease-running",
		LeaseID:     localID,
		ContainerID: "cid-local",
	}
	cases := []struct {
		name       string
		handle     *runtime.Handle
		disk       bool
		leaseID    string
		wantPass   bool
		wantDocker bool
		wantKeep   bool
	}{
		{name: "match stops", handle: &handle, leaseID: localID, wantPass: true, wantDocker: true},
		{name: "mismatch refuses", handle: &handle, leaseID: "lease-other", wantPass: false, wantKeep: true},
		{name: "no handle soft no-op", leaseID: "lease-anything", wantPass: true},
		{name: "disk handle mismatch refuses", handle: &handle, disk: true, leaseID: "lease-other", wantPass: false, wantKeep: true},
		{name: "disk handle match stops", handle: &handle, disk: true, leaseID: localID, wantPass: true, wantDocker: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newOwnerTestAgent(t)
			var dockerCalls [][]string
			a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				dockerCalls = append(dockerCalls, append([]string{}, args...))
				return []byte("ok\n"), nil
			}}
			if tc.handle != nil {
				h := *tc.handle
				if tc.disk {
					if err := a.saveLease(&h); err != nil {
						t.Fatal(err)
					}
					a.lease = nil
				} else {
					a.lease = &h
				}
			}
			startTestOwner(t, a)

			resp := ownerDoAuth(t, a, http.MethodPost, "/v0/lease_stop", map[string]any{
				"lease_id": tc.leaseID,
			})
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d", resp.StatusCode)
			}
			var body ownerLeaseStopResponse
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Passed != tc.wantPass {
				t.Fatalf("passed=%v want %v body=%+v", body.Passed, tc.wantPass, body)
			}
			if tc.wantPass && body.ImageHash == "" {
				t.Fatal("match/no-op must echo image_hash")
			}
			if tc.wantDocker {
				if len(dockerCalls) != 1 || dockerCalls[0][0] != "stop" {
					t.Fatalf("expected docker stop, calls=%v", dockerCalls)
				}
			} else if len(dockerCalls) != 0 {
				t.Fatalf("docker must not run, calls=%v", dockerCalls)
			}
			kept := a.lease != nil || a.loadLease() != nil
			if kept != tc.wantKeep {
				t.Fatalf("handle kept=%v want %v mem=%+v disk=%+v", kept, tc.wantKeep, a.lease, a.loadLease())
			}
			line := lastEffectsLine(t, a.cfg.StateDir)
			if line.Tool != toolLeaseStop || line.Passed != tc.wantPass || line.LeaseID != tc.leaseID {
				t.Fatalf("effects %+v", line)
			}
		})
	}
}

func TestOwnerCLISellPause(t *testing.T) {
	a := newOwnerTestAgent(t)
	// Fixture until is 2026-09-08T00:00:00Z; freeze before that so expiry is not the test.
	a.now = func() time.Time { return time.Date(2026, 9, 7, 23, 0, 0, 0, time.UTC) }
	startTestOwner(t, a)

	var out bytes.Buffer
	if err := runOwnerCLI(a.cfg.StateDir, []string{"sell-pause", "--until=2026-09-08T00:00:00Z"}, &out); err != nil {
		t.Fatal(err)
	}
	var body ownerSellPauseResponse
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatalf("cli json %q: %v", out.String(), err)
	}
	if !body.Passed || body.SellState != api.SellPaused {
		t.Fatalf("cli %+v", body)
	}
	if a.effectiveSell().SellState != api.SellPaused {
		t.Fatal("cli must apply the same actuator")
	}
}

func TestOwnerLeaseStopRequiresLeaseID(t *testing.T) {
	a := newOwnerTestAgent(t)
	startTestOwner(t, a)
	resp := ownerDoAuth(t, a, http.MethodPost, "/v0/lease_stop", map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func newOwnerTestAgent(t *testing.T) *Agent {
	t.Helper()
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.state.DeviceID = testDevice
	a.tunnels = &fakeTunnel{}
	return a
}

func startTestOwner(t *testing.T, a *Agent) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := a.startOwner(ctx); err != nil {
		t.Fatal(err)
	}
}

func ownerDoAuth(t *testing.T, a *Agent, method, path string, body any) *http.Response {
	t.Helper()
	tok, err := readOwnerToken(a.cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	return ownerDo(t, a.cfg.StateDir, method, path, "Bearer "+tok, body)
}

func ownerDo(t *testing.T, dir, method, path, auth string, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", ownerSockPath(dir))
			},
		},
		Timeout: 3 * time.Second,
	}
	req, err := http.NewRequest(method, "http://owner"+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func lastEffectsLine(t *testing.T, dir string) ownerEffect {
	t.Helper()
	raw, err := os.ReadFile(ownerEffectsPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatal("no effects lines")
	}
	var e ownerEffect
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &e); err != nil {
		t.Fatalf("effects json %q: %v", lines[len(lines)-1], err)
	}
	return e
}

func TestRunStartsOwnerSocket(t *testing.T) {
	var heartbeats atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.EnrollResponse{DeviceID: testDevice, DeviceSecret: testSecret})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		heartbeats.Add(1)
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{DeviceID: testDevice, PathBState: api.PathBGreen, AcceptedAt: time.Now().UTC()})
	})
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, srv.URL, dir, uptime, "tok", "")
	a.tunnels = &fakeTunnel{}
	waitLoop(t, a, func() bool {
		_, err := os.Stat(ownerSockPath(dir))
		return err == nil && heartbeats.Load() >= 1
	})
	fi, err := os.Stat(ownerTokenPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("token mode %o", fi.Mode().Perm())
	}
}

func TestOwnerReusesExistingToken(t *testing.T) {
	a := newOwnerTestAgent(t)
	const existing = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := os.WriteFile(ownerTokenPath(a.cfg.StateDir), []byte(existing+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	startTestOwner(t, a)
	fi, err := os.Stat(ownerTokenPath(a.cfg.StateDir))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("existing token not tightened to 0600: %o", fi.Mode().Perm())
	}
	got, err := readOwnerToken(a.cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if got != existing {
		t.Fatalf("token rewritten: %q", got)
	}
	resp := ownerDo(t, a.cfg.StateDir, http.MethodPost, "/v0/sell_pause", "Bearer "+existing, map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
}
