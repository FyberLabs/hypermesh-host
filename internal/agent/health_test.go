package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/buildinfo"
	"github.com/FyberLabs/hypermesh-host/internal/metrics"
	"github.com/FyberLabs/hypermesh-host/internal/pull"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
	"github.com/FyberLabs/hypermesh-host/internal/tunnel"
)

func TestHeartbeatHealthShape(t *testing.T) {
	prev := buildinfo.Commit
	t.Cleanup(func() { buildinfo.Commit = prev })
	buildinfo.Commit = "abc123deadbeef"

	const (
		digest   = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		priv     = "LEAKED-WG-PRIVATE"
		pub      = "LEAKED-WG-PUBLIC"
		planeMsg = `{"detail":"hm_dev_from_plane renter-prompt-do-not-send"}`
	)
	var raw []byte
	var postedKey string
	mux := http.NewServeMux()
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		var err error
		raw, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read heartbeat: %v", err)
		}
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{
			DeviceID:   testDevice,
			PathBState: api.PathBGreen,
			AcceptedAt: time.Date(2026, 9, 25, 12, 0, 1, 0, time.UTC),
		})
	})
	mux.HandleFunc(api.PathTunnelPeer, func(w http.ResponseWriter, r *http.Request) {
		var body api.TunnelPeerRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		postedKey = body.PeerPublicKey
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(planeMsg))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("12.0 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, srv.URL, dir, uptime, "", "")
	a.cfg.HostName = "reported-test-host"
	a.state.DeviceID = testDevice
	a.client.DeviceSecret = testSecret
	a.lease = &runtime.Handle{
		Name:        "hypermesh-lease-1",
		LeaseID:     "lease-1",
		ImageDigest: digest,
	}
	a.countLeases = func(context.Context) (int, bool) { return 2, true }
	syncAt := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return syncAt }
	hs := time.Unix(1_700_000_000, 0)
	a.tunnels = &tunnel.Runner{
		Iface:     "hypermesh",
		Userspace: true,
		Now:       func() time.Time { return hs },
		FindPID:   func(string) (int, bool) { return 4242, true },
		Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "wg" {
				body := strings.Join([]string{
					strings.Join([]string{priv, "ifacepub", "51820", "off"}, "\t"),
					strings.Join([]string{pub, "(none)", "10.77.0.1:51820", "10.77.0.0/24", "1699999900", "0", "0", "25"}, "\t"),
				}, "\n") + "\n"
				return []byte(body), nil
			}
			if name == "ip" {
				return []byte(""), nil
			}
			return nil, errors.New("unexpected " + name)
		},
	}

	a.recordSync(nil)
	a.syncAndRecord(context.Background())
	if err := a.heartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("no heartbeat body")
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	wantTop := []string{"host_name", "device_id", "image_hash", "loaded_model_hash", "uptime_s", "sell_state", "health"}
	if !sameKeys(top, wantTop) {
		t.Fatalf("top-level keys %s", raw)
	}
	var hb api.HeartbeatRequest
	if err := json.Unmarshal(raw, &hb); err != nil {
		t.Fatal(err)
	}
	if hb.DeviceID != testDevice || hb.UptimeS != 12 || hb.ImageHash != "sha256:fixture" || hb.SellState != api.SellSelling || hb.LoadedModelHash != digest {
		t.Fatalf("old fields changed: %+v", hb)
	}
	if hb.HostName != "reported-test-host" { t.Fatalf("host name %q", hb.HostName) }
	if hb.Health == nil {
		t.Fatal("health missing")
	}
	var health map[string]json.RawMessage
	if err := json.Unmarshal(top["health"], &health); err != nil {
		t.Fatal(err)
	}
	wantHealth := []string{
		"tunnel_state", "last_sync_at", "last_sync_error_class", "last_sync_http_status",
		"handshake_age_s", "wg_pid", "wg_port", "lease_container_count", "build_commit",
		"host_type",
	}
	if !sameKeys(health, wantHealth) {
		t.Fatalf("health keys %s", top["health"])
	}
	if hb.Health.TunnelState != tunnel.StateUp {
		t.Fatalf("tunnel_state %q", hb.Health.TunnelState)
	}
	if hb.Health.HostType != HostTypeModel {
		t.Fatalf("host_type %q", hb.Health.HostType)
	}
	if hb.Health.LastSyncAt != "2026-09-25T12:00:00Z" {
		t.Fatalf("last_sync_at %q", hb.Health.LastSyncAt)
	}
	if hb.Health.LastSyncErrorClass != api.ErrClassHTTP5xx || hb.Health.LastSyncHTTPStatus != 503 {
		t.Fatalf("sync error %+v", hb.Health)
	}
	if hb.Health.HandshakeAgeS == nil || *hb.Health.HandshakeAgeS != 100 {
		t.Fatalf("handshake_age_s %v", hb.Health.HandshakeAgeS)
	}
	if hb.Health.WGPID == nil || *hb.Health.WGPID != 4242 {
		t.Fatalf("wg_pid %v", hb.Health.WGPID)
	}
	if hb.Health.WGPort == nil || *hb.Health.WGPort != 51820 {
		t.Fatalf("wg_port %v", hb.Health.WGPort)
	}
	if hb.Health.LeaseContainerCount == nil || *hb.Health.LeaseContainerCount != 2 {
		t.Fatalf("lease_container_count %v", hb.Health.LeaseContainerCount)
	}
	if hb.Health.BuildCommit != "abc123deadbeef" {
		t.Fatalf("build_commit %q", hb.Health.BuildCommit)
	}
	for _, leak := range []string{
		priv, pub, "hm_dev_", "renter-prompt", "device_secret", "peer_private_key",
		"private_key", "public_key", "enroll_token", "lease-1", planeMsg, "10.77.0.1",
	} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("heartbeat leaked %q: %s", leak, raw)
		}
	}
	if postedKey == "" || strings.Contains(string(raw), postedKey) {
		t.Fatalf("host public key must stay out of the heartbeat")
	}

	text := string(a.metricsText())
	for _, name := range []string{
		"hypermesh_heartbeat_success_total",
		"hypermesh_heartbeat_failure_total",
		"hypermesh_tunnel_sync_success_total",
		"hypermesh_tunnel_sync_failure_total",
		"hypermesh_tunnel_state",
		"hypermesh_handshake_age_seconds",
		"hypermesh_wg_pid",
		"hypermesh_wg_port",
		"hypermesh_lease_container_count",
		"hypermesh_last_sync_timestamp_seconds",
		"hypermesh_last_sync_http_status",
		"hypermesh_last_sync_error_class",
		"hypermesh_build_info",
	} {
		if !strings.Contains(text, name) {
			t.Fatalf("metrics missing %s\n%s", name, text)
		}
	}
	if !strings.Contains(text, "hypermesh_tunnel_sync_failure_total{class=\"http_5xx\"} 1") {
		t.Fatalf("503 must count as http_5xx\n%s", text)
	}
	if !strings.Contains(text, "hypermesh_heartbeat_success_total 1") {
		t.Fatalf("heartbeat success counter\n%s", text)
	}
	if !strings.Contains(text, "hypermesh_tunnel_state{state=\"up\"} 1") {
		t.Fatalf("tunnel state gauge\n%s", text)
	}
	for _, leak := range []string{priv, pub, "hm_dev_", "renter-prompt", "lease-1"} {
		if strings.Contains(text, leak) {
			t.Fatalf("metrics leaked %q: %s", leak, text)
		}
	}
}

func TestHeartbeatSurvivesHealthCollectionFailure(t *testing.T) {
	var raw []byte
	mux := http.NewServeMux()
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		var err error
		raw, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read: %v", err)
		}
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{DeviceID: testDevice, PathBState: api.PathBGreen, AcceptedAt: time.Now().UTC()})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, srv.URL, dir, uptime, "", "")
	a.state.DeviceID = testDevice
	a.client.DeviceSecret = testSecret
	a.countLeases = func(context.Context) (int, bool) { return 0, false }
	a.tunnels = panicThenErrTunnel{}

	if err := a.heartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "LEAKED-WG-PRIVATE") || strings.Contains(string(raw), "tunnel_state") {
		t.Fatalf("failed collection leaked into heartbeat: %s", raw)
	}
	var hb api.HeartbeatRequest
	if err := json.Unmarshal(raw, &hb); err != nil {
		t.Fatal(err)
	}
	if hb.DeviceID != testDevice || hb.Health == nil || hb.Health.BuildCommit == "" {
		t.Fatalf("heartbeat must still carry old fields and build_commit: %s", raw)
	}
	if strings.Contains(string(raw), "lease_container_count") || strings.Contains(string(raw), "handshake_age_s") {
		t.Fatalf("unknown fields must be omitted: %s", raw)
	}
}

func TestClassifySyncError(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		class  string
		status int
	}{
		{name: "nil", class: "", status: 0},
		{name: "503", err: &api.StatusError{StatusCode: 503}, class: api.ErrClassHTTP5xx, status: 503},
		{name: "404 sentinel", err: api.ErrUnknownDevice, class: api.ErrClassHTTP4xx, status: 404},
		{name: "401", err: api.ErrUnauthorized, class: api.ErrClassHTTP4xx, status: 401},
		{name: "decode", err: &api.DecodeError{Err: errors.New("bad")}, class: api.ErrClassDecode, status: 0},
		{name: "timeout", err: context.DeadlineExceeded, class: api.ErrClassTimeout, status: 0},
		{name: "network", err: &netTimeout{timeout: false}, class: api.ErrClassNetwork, status: 0},
		{name: "net timeout", err: &netTimeout{timeout: true}, class: api.ErrClassTimeout, status: 0},
		{name: "local", err: errors.New("wg syncconf failed"), class: "", status: 0},
		{name: "wrapped 503 keeps class and drops text", err: fmt.Errorf("renter-prompt: %w", &api.StatusError{StatusCode: 500}), class: api.ErrClassHTTP5xx, status: 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			class, status := classifyErr(tc.err)
			if class != tc.class || status != tc.status {
				t.Fatalf("got %s %d", class, status)
			}
		})
	}
}

func TestCountLeaseNames(t *testing.T) {
	out := "hypermesh-lease-a\n\n/hypermesh-cert-bench\nnginx\nhypermesh-\n"
	if n := countLeaseNames(out); n != 2 {
		t.Fatalf("count %d", n)
	}
}

func TestMetricsAddrOverride(t *testing.T) {
	t.Setenv("HYPERMESH_METRICS_ADDR", "127.0.0.1:19064")
	t.Setenv("HYPERMESH_METRICS_ALLOW_NON_LOOPBACK", "1")
	cfg := FromEnv()
	if cfg.MetricsAddr != "127.0.0.1:19064" || !cfg.MetricsAllowNonLoopback {
		t.Fatalf("addr %q allow %v", cfg.MetricsAddr, cfg.MetricsAllowNonLoopback)
	}
	if err := metrics.AuthorizeBind(cfg.MetricsAddr, ""); err != nil {
		t.Fatal(err)
	}
	if err := metrics.AuthorizeBind("0.0.0.0:9464", "10.77.0.2"); err == nil {
		t.Fatal("unspecified address must be refused")
	}
	if err := metrics.AuthorizeBind("8.8.8.8:9464", "8.8.8.8"); err == nil {
		t.Fatal("public address must be refused")
	}
	if err := metrics.AuthorizeBind("10.77.0.2:9464", ""); err == nil {
		t.Fatal("tunnel address must be refused without a WireGuard host")
	}
	if err := metrics.AuthorizeBind("10.77.0.2:9464", "10.77.0.2"); err != nil {
		t.Fatal(err)
	}
	got, refused := metrics.ResolveAddr("0.0.0.0:9464", "10.77.0.2")
	if got != "10.77.0.2:9464" || !refused {
		t.Fatalf("fallback %s refused %v", got, refused)
	}
}

type panicThenErrTunnel struct{}

func (panicThenErrTunnel) Apply(context.Context, tunnel.Session) error { return nil }
func (panicThenErrTunnel) Teardown(context.Context) error              { return nil }
func (panicThenErrTunnel) Snapshot(context.Context) (tunnel.Snapshot, error) {
	panic("LEAKED-WG-PRIVATE")
}

type netTimeout struct{ timeout bool }

func (n *netTimeout) Error() string   { return "net" }
func (n *netTimeout) Timeout() bool   { return n.timeout }
func (n *netTimeout) Temporary() bool { return true }

func TestHeartbeatDiskFreeBytes(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "model.gguf")
	if err := os.WriteFile(file, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "weights", "model.gguf")
	const want int64 = 424242
	cases := []struct {
		name     string
		gguf     string
		statPath string
	}{
		{name: "existing file", gguf: file, statPath: file},
		{name: "missing file walks up", gguf: missing, statPath: dir},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			raw, hb, text := heartbeatHealth(t, func(a *Agent) {
				a.cfg.GGUFPath = tc.gguf
				a.statFree = func(path string) (int64, error) {
					gotPath = path
					return want, nil
				}
			})
			if hb.Health == nil || hb.Health.DiskFreeBytes == nil {
				t.Fatalf("disk_free_bytes missing: %s", raw)
			}
			if *hb.Health.DiskFreeBytes != want {
				t.Fatalf("disk_free_bytes %d want %d", *hb.Health.DiskFreeBytes, want)
			}
			if gotPath != tc.statPath {
				t.Fatalf("statfs path %q want %q", gotPath, tc.statPath)
			}
			if !strings.Contains(string(raw), `"disk_free_bytes":`+strconv.FormatInt(want, 10)) {
				t.Fatalf("heartbeat JSON %s", raw)
			}
			line := "hypermesh_disk_free_bytes " + strconv.FormatInt(want, 10)
			if !strings.Contains(text, line) {
				t.Fatalf("metrics missing %s\n%s", line, text)
			}
			if !strings.Contains(text, "hypermesh_disk_size_bytes ") {
				t.Fatalf("metrics missing disk_size\n%s", text)
			}
			freeLabels, freeOK := seriesLabels(text, "hypermesh_disk_free_bytes")
			sizeLabels, sizeOK := seriesLabels(text, "hypermesh_disk_size_bytes")
			if !freeOK || !sizeOK || freeLabels != sizeLabels {
				t.Fatalf("label parity free %q (%v) size %q (%v)", freeLabels, freeOK, sizeLabels, sizeOK)
			}
		})
	}
}

func TestStatfsFreeIsBavailTimesBsize(t *testing.T) {
	dir := t.TempDir()
	// Free space moves while other tests run. Sample before and after
	// FreeBytes so a single Bavail change still matches one of the readings.
	var got int64
	var matched bool
	for i := 0; i < 20 && !matched; i++ {
		var before, after syscall.Statfs_t
		if err := syscall.Statfs(dir, &before); err != nil {
			t.Fatal(err)
		}
		n, err := pull.FreeBytes(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Statfs(dir, &after); err != nil {
			t.Fatal(err)
		}
		got = n
		b := int64(before.Bavail) * int64(before.Bsize)
		a := int64(after.Bavail) * int64(after.Bsize)
		matched = got == b || got == a
	}
	if !matched || got < 0 {
		t.Fatalf("FreeBytes %d did not match Bavail*Bsize around the call", got)
	}
}

func TestHeartbeatOmitsDiskFreeBytes(t *testing.T) {
	t.Run("unset path", func(t *testing.T) {
		raw, hb, text := heartbeatHealth(t, func(a *Agent) {
			a.cfg.GGUFPath = "  "
			a.statFree = func(string) (int64, error) {
				t.Fatal("statfs called without a GGUF path")
				return 0, nil
			}
		})
		if hb.Health == nil {
			t.Fatal("health missing")
		}
		if hb.Health.DiskFreeBytes != nil || strings.Contains(string(raw), "disk_free_bytes") {
			t.Fatalf("unset path must omit disk_free_bytes: %s", raw)
		}
		if strings.Contains(text, "hypermesh_disk_free_bytes") || strings.Contains(text, "hypermesh_disk_size_bytes") {
			t.Fatalf("metrics must omit both gauges: %s", text)
		}
	})
	t.Run("statfs error", func(t *testing.T) {
		dir := t.TempDir()
		gguf := filepath.Join(dir, "model.gguf")
		if err := os.WriteFile(gguf, []byte("gguf"), 0o644); err != nil {
			t.Fatal(err)
		}
		raw, hb, text := heartbeatHealth(t, func(a *Agent) {
			a.cfg.GGUFPath = gguf
			a.statFree = func(string) (int64, error) {
				return 0, errors.New("statfs failed")
			}
		})
		if hb.DeviceID != testDevice {
			t.Fatalf("old fields changed: %+v", hb)
		}
		if hb.Health == nil || hb.Health.BuildCommit == "" {
			t.Fatalf("heartbeat must still succeed: %s", raw)
		}
		if hb.Health.DiskFreeBytes != nil || strings.Contains(string(raw), "disk_free_bytes") {
			t.Fatalf("statfs error must omit disk_free_bytes: %s", raw)
		}
		if strings.Contains(text, "hypermesh_disk_free_bytes") || strings.Contains(text, "hypermesh_disk_size_bytes") {
			t.Fatalf("metrics must omit both gauges: %s", text)
		}
	})
}

func seriesLabels(text, name string) (string, bool) {
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, name) {
			continue
		}
		rest := strings.TrimPrefix(line, name)
		if rest == "" || (rest[0] != ' ' && rest[0] != '{') {
			continue
		}
		if strings.HasPrefix(rest, "{") {
			end := strings.IndexByte(rest, '}')
			if end < 0 {
				return "", false
			}
			return rest[:end+1], true
		}
		return "", true
	}
	return "", false
}

func heartbeatHealth(t *testing.T, setup func(*Agent)) ([]byte, api.HeartbeatRequest, string) {
	t.Helper()
	var raw []byte
	mux := http.NewServeMux()
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		var err error
		raw, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read heartbeat: %v", err)
		}
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{
			DeviceID:   testDevice,
			PathBState: api.PathBGreen,
			AcceptedAt: time.Now().UTC(),
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("4.0 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, srv.URL, dir, uptime, "", "")
	a.state.DeviceID = testDevice
	a.client.DeviceSecret = testSecret
	a.tunnels = &fakeTunnel{}
	if setup != nil {
		setup(a)
	}
	if err := a.heartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	var hb api.HeartbeatRequest
	if err := json.Unmarshal(raw, &hb); err != nil {
		t.Fatal(err)
	}
	return raw, hb, string(a.metricsText())
}

func sameKeys(m map[string]json.RawMessage, want []string) bool {
	if len(m) != len(want) {
		return false
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			return false
		}
	}
	return true
}
