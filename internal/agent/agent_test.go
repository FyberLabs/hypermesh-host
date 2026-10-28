package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
	"github.com/FyberLabs/hypermesh-host/internal/telem"
	"github.com/FyberLabs/hypermesh-host/internal/tunnel"
)

const (
	testTenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	testDevice = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	testSecret = "hm_dev_fixture_secret"
	testJob    = "cccccccc-cccc-cccc-cccc-cccccccccccc"
)

type fakeTunnel struct {
	mu        sync.Mutex
	applied   []tunnel.Session
	teardowns int
}

func (f *fakeTunnel) Apply(_ context.Context, sess tunnel.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, sess)
	return nil
}

func (f *fakeTunnel) Teardown(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.teardowns++
	return nil
}

func TestLoopEnrollBearerPersistAndLabSoak(t *testing.T) {
	var mu sync.Mutex
	var enrolls, heartbeats, pulls, results int
	var enrollAuth, hbAuth string
	var enrollBody api.EnrollRequest

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		enrolls++
		enrollAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&enrollBody)
		mu.Unlock()
		writeJSON(w, http.StatusOK, api.EnrollResponse{
			DeviceID:         testDevice,
			CredentialPrefix: "cafe0000",
			DeviceSecret:     testSecret,
		})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		heartbeats++
		hbAuth = r.Header.Get("Authorization")
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{
			DeviceID:   testDevice,
			PathBState: api.PathBNeverRun,
			AcceptedAt: time.Now().UTC(),
		})
	})
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		pulls++
		n := pulls
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if n == 1 {
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
		mu.Lock()
		results++
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body api.JobResultRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Passed {
			t.Errorf("stub should pass, got %+v", body)
		}
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("3.0 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := newTestAgent(t, srv.URL, dir, uptime, "one-time-token", "")
	ft := &fakeTunnel{}
	a.tunnels = ft

	waitLoop(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return enrolls == 1 && heartbeats >= 1 && pulls >= 2 && results >= 1
	})

	mu.Lock()
	defer mu.Unlock()
	if enrollAuth != "" {
		t.Fatalf("enroll sent Authorization %q", enrollAuth)
	}
	if hbAuth != "Bearer "+testSecret {
		t.Fatalf("heartbeat auth %q", hbAuth)
	}
	if enrollBody.EnrollToken != "one-time-token" {
		t.Fatal("enroll body token missing")
	}

	path := filepath.Join(dir, "device.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "one-time-token") {
		t.Fatal("enroll token written to state")
	}
	if !strings.Contains(string(raw), testSecret) {
		t.Fatal("device_secret not persisted")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("state mode %o", fi.Mode().Perm())
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.DeviceID != testDevice || st.DeviceSecret != testSecret {
		t.Fatalf("state %+v", st)
	}
}

func TestSkipsEnrollWhenDeviceAndSecretOnDisk(t *testing.T) {
	var heartbeats int
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("enroll must not run when device_id+secret are on disk")
		w.WriteHeader(http.StatusConflict)
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			t.Errorf("stored secret not sent")
		}
		mu.Lock()
		heartbeats++
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
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "device.json"), []byte(`{"device_id":"`+testDevice+`","device_secret":"`+testSecret+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, srv.URL, dir, uptime, "must-not-be-sent", "")
	a.tunnels = &fakeTunnel{}
	waitLoop(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return heartbeats >= 1
	})
}

func TestTunnelApplyAckAndRevoke(t *testing.T) {
	var mu sync.Mutex
	acks := 0
	phase := 0

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.EnrollResponse{DeviceID: testDevice, DeviceSecret: testSecret})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{DeviceID: testDevice, PathBState: api.PathBGreen, AcceptedAt: time.Now().UTC()})
	})
	mux.HandleFunc(api.PathTunnelAck, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body api.TunnelAckRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Applied {
			t.Errorf("ack applied=false")
		}
		mu.Lock()
		acks++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	peerPub := acceptTunnelPeer(mux)
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		p := phase
		mu.Unlock()
		if p == 0 {
			writeJSON(w, http.StatusOK, api.TunnelSession{
				TunnelID:        "tun-1",
				LeaseID:         "lease-1",
				Status:          api.TunnelAllocated,
				Endpoint:        "198.51.100.10:51820",
				ServerPublicKey: "serverpub",
				PeerPublicKey:   peerPub(),
				AllowedIPs:      "10.8.0.0/24",
				ClientAddress:   "10.8.0.2/32",
			})
			return
		}
		writeJSON(w, http.StatusOK, api.TunnelSession{
			TunnelID: "tun-1",
			LeaseID:  "lease-1",
			Status:   api.TunnelRevoked,
		})
	})
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, srv.URL, dir, uptime, "tok", "")
	ft := &fakeTunnel{}
	a.tunnels = ft

	waitLoop(t, a, func() bool {
		ft.mu.Lock()
		n := len(ft.applied)
		ft.mu.Unlock()
		mu.Lock()
		nAck := acks
		mu.Unlock()
		return n >= 1 && nAck >= 1
	})

	ft.mu.Lock()
	if len(ft.applied) < 1 || ft.applied[0].PeerPrivateKey == "" {
		ft.mu.Unlock()
		t.Fatalf("apply %+v", ft.applied)
	}
	ft.mu.Unlock()

	mu.Lock()
	phase = 1
	mu.Unlock()

	waitLoop(t, a, func() bool {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		return ft.teardowns >= 1
	})
}

func TestSessionNeedsPeer(t *testing.T) {
	if !sessionNeedsPeer("", "pub") {
		t.Fatal("empty plane key needs a POST")
	}
	if !sessionNeedsPeer("other", "pub") {
		t.Fatal("mismatch needs a POST")
	}
	if sessionNeedsPeer("pub", "pub") || sessionNeedsPeer("  pub  ", "pub") {
		t.Fatal("matching plane key must not POST again")
	}
}

func TestAllocatedTunnelPostsPublicKeyOnly(t *testing.T) {
	var mu sync.Mutex
	var peerBodies [][]byte
	var hbBodies [][]byte
	var registered string
	acks, gets := 0, 0

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.EnrollResponse{DeviceID: testDevice, DeviceSecret: testSecret})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		hbBodies = append(hbBodies, raw)
		mu.Unlock()
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{DeviceID: testDevice, PathBState: api.PathBGreen, AcceptedAt: time.Now().UTC()})
	})
	mux.HandleFunc(api.PathTunnelAck, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		acks++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(api.PathTunnelPeer, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body api.TunnelPeerRequest
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("peer json: %v", err)
		}
		mu.Lock()
		peerBodies = append(peerBodies, raw)
		registered = body.PeerPublicKey
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gets++
		pub := registered
		mu.Unlock()
		writeJSON(w, http.StatusOK, api.TunnelSession{
			TunnelID:        "tun-1",
			LeaseID:         "lease-1",
			Status:          api.TunnelAllocated,
			Endpoint:        "wg.test.hyperme.sh:51820",
			ServerPublicKey: "serverpub",
			PeerPublicKey:   pub,
			AllowedIPs:      "10.77.0.0/24",
			ClientAddress:   "10.77.0.2/32",
		})
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
	ft := &fakeTunnel{}
	a.tunnels = ft

	waitLoop(t, a, func() bool {
		ft.mu.Lock()
		n := len(ft.applied)
		ft.mu.Unlock()
		mu.Lock()
		nAck := acks
		nPeer := len(peerBodies)
		nGet := gets
		mu.Unlock()
		// GET 3 starts only after the previous sync finished, so an extra POST
		// would already be counted.
		return n >= 1 && nAck >= 1 && nPeer >= 1 && nGet >= 3
	})

	keyRaw, err := os.ReadFile(filepath.Join(a.keyDir, "hypermesh.key"))
	if err != nil {
		t.Fatal(err)
	}
	priv := strings.TrimSpace(string(keyRaw))
	pub, err := tunnel.PublicFromPrivate(priv)
	if err != nil {
		t.Fatal(err)
	}

	ft.mu.Lock()
	if len(ft.applied) < 1 || ft.applied[0].PeerPrivateKey != priv {
		ft.mu.Unlock()
		t.Fatal("apply must use the local private key")
	}
	ft.mu.Unlock()

	mu.Lock()
	defer mu.Unlock()
	if len(peerBodies) != 1 {
		t.Fatalf("peer POSTs = %d, want 1 (idempotent once the plane has the key)", len(peerBodies))
	}
	var obj map[string]any
	if err := json.Unmarshal(peerBodies[0], &obj); err != nil {
		t.Fatal(err)
	}
	if len(obj) != 1 || obj["peer_public_key"] != pub {
		t.Fatalf("peer body %s", peerBodies[0])
	}
	if strings.Contains(string(peerBodies[0]), priv) {
		t.Fatal("peer POST contained the private key")
	}
	for _, raw := range hbBodies {
		if strings.Contains(string(raw), priv) || strings.Contains(string(raw), "peer_private_key") {
			t.Fatalf("heartbeat contained key material: %s", raw)
		}
	}
}

func TestPeerNotRegisteredPostsThenRegets(t *testing.T) {
	var mu sync.Mutex
	gets, posts := 0, 0
	var bodies [][]byte
	var registered string

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathTunnelPeer, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body api.TunnelPeerRequest
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		posts++
		bodies = append(bodies, raw)
		registered = body.PeerPublicKey
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"peer_public_key": body.PeerPublicKey})
	})
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gets++
		n := gets
		pub := registered
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":"peer_public_key_not_registered","detail":"No host WireGuard public key is registered for this allocated tunnel. POST the public key to /api/v1/hypermesh/agent/tunnel/peer first."}`))
			return
		}
		sess := allocatedTunnel("lease-1")
		sess.PeerPublicKey = pub
		writeJSON(w, http.StatusOK, sess)
	})
	mux.HandleFunc(api.PathTunnelAck, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	a := newTestAgent(t, srv.URL, dir, filepath.Join(dir, "uptime"), "tok", "")
	_ = os.WriteFile(filepath.Join(dir, "uptime"), []byte("1 1\n"), 0o644)
	a.state = State{DeviceID: testDevice, DeviceSecret: testSecret}
	a.client.DeviceSecret = testSecret
	pub, _, err := preloadPostedKey(a)
	if err != nil {
		t.Fatal(err)
	}
	ft := &fakeTunnel{}
	a.tunnels = ft

	if err := a.syncTunnel(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gets != 2 {
		t.Fatalf("gets %d, want GET, POST, re-GET", gets)
	}
	if posts != 1 {
		t.Fatalf("posts %d, want one recovery POST", posts)
	}
	if strings.Contains(string(bodies[0]), "peer_private_key") {
		t.Fatal("recovery POST included a private key field")
	}
	var obj map[string]any
	if err := json.Unmarshal(bodies[0], &obj); err != nil {
		t.Fatal(err)
	}
	if obj["peer_public_key"] != pub {
		t.Fatalf("posted %#v", obj)
	}
	ft.mu.Lock()
	if len(ft.applied) != 1 {
		ft.mu.Unlock()
		t.Fatalf("apply %d", len(ft.applied))
	}
	ft.mu.Unlock()
}

func TestPeerMismatchAndKeyChangeRepost(t *testing.T) {
	var mu sync.Mutex
	posts := 0
	planePub := "OTHERPUBLICKEYNOTOURS++++++++++++++++++++="

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathTunnelPeer, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body api.TunnelPeerRequest
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		posts++
		planePub = body.PeerPublicKey
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		pub := planePub
		mu.Unlock()
		sess := allocatedTunnel("lease-1")
		sess.PeerPublicKey = pub
		writeJSON(w, http.StatusOK, sess)
	})
	mux.HandleFunc(api.PathTunnelAck, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	a := newTestAgent(t, srv.URL, dir, filepath.Join(dir, "uptime"), "tok", "")
	a.state = State{DeviceID: testDevice, DeviceSecret: testSecret}
	a.client.DeviceSecret = testSecret
	localPub, _, err := preloadPostedKey(a)
	if err != nil {
		t.Fatal(err)
	}
	a.tunnels = &fakeTunnel{}

	if err := a.syncTunnel(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if posts != 1 {
		mu.Unlock()
		t.Fatalf("mismatch posts %d", posts)
	}
	mu.Unlock()
	if a.postedPub != localPub {
		t.Fatal("posted pub must be the local public key")
	}

	a.postedPub = "stale-public-key"
	if err := a.syncTunnel(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if posts != 2 {
		mu.Unlock()
		t.Fatalf("key-change posts %d, want 2", posts)
	}
	mu.Unlock()
}

func TestPeer404IsBenignAndNotRepeatedAsError(t *testing.T) {
	var buf strings.Builder
	posts := 0
	mux := http.NewServeMux()
	mux.HandleFunc(api.PathTunnelPeer, func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Tunnel not found"}`))
	})
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	a := newTestAgent(t, srv.URL, dir, filepath.Join(dir, "uptime"), "tok", "")
	a.log = slog.New(slog.NewTextHandler(&buf, nil))
	a.state = State{DeviceID: testDevice, DeviceSecret: testSecret}
	a.client.DeviceSecret = testSecret
	a.tunnels = &fakeTunnel{}

	if err := a.syncTunnel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.syncTunnel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if posts != 1 {
		t.Fatalf("posts %d, want one POST; 404 saved the device key", posts)
	}
	logs := buf.String()
	if strings.Count(logs, "no tunnel allocated yet") != 1 {
		t.Fatalf("benign 404 should log once, logs:\n%s", logs)
	}
	if strings.Contains(logs, "tunnel sync failed") {
		t.Fatalf("404 must not be a sync failure: %s", logs)
	}
}

func TestNullPeerPostsThenRegetsBeforeApply(t *testing.T) {
	var mu sync.Mutex
	gets, posts, acks := 0, 0, 0
	var posted string

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathTunnelPeer, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("peer query %q", r.URL.RawQuery)
		}
		raw, _ := io.ReadAll(r.Body)
		var body api.TunnelPeerRequest
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		posts++
		posted = body.PeerPublicKey
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gets++
		n := gets
		pub := posted
		mu.Unlock()
		sess := allocatedTunnel("lease-1")
		if n == 1 {
			sess.PeerPublicKey = ""
		} else {
			sess.PeerPublicKey = pub
		}
		writeJSON(w, http.StatusOK, sess)
	})
	mux.HandleFunc(api.PathTunnelAck, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		acks++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	a := newTestAgent(t, srv.URL, dir, filepath.Join(dir, "uptime"), "tok", "")
	a.state = State{DeviceID: testDevice, DeviceSecret: testSecret}
	a.client.DeviceSecret = testSecret
	ft := &fakeTunnel{}
	a.tunnels = ft

	if err := a.syncTunnel(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if posts != 2 || gets != 2 || acks != 1 {
		mu.Unlock()
		t.Fatalf("posts %d gets %d acks %d", posts, gets, acks)
	}
	mu.Unlock()
	ft.mu.Lock()
	if len(ft.applied) != 1 {
		ft.mu.Unlock()
		t.Fatalf("apply count %d", len(ft.applied))
	}
	ft.mu.Unlock()
}

func TestRotationWaitsForGetThenAcksAgain(t *testing.T) {
	var mu sync.Mutex
	posts, gets, acks := 0, 0, 0
	shown := ""
	staged := ""
	lag := false

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathTunnelPeer, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body api.TunnelPeerRequest
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		posts++
		if lag {
			staged = body.PeerPublicKey
		} else {
			shown = body.PeerPublicKey
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gets++
		pub := shown
		mu.Unlock()
		sess := allocatedTunnel("lease-1")
		sess.PeerPublicKey = pub
		writeJSON(w, http.StatusOK, sess)
	})
	mux.HandleFunc(api.PathTunnelAck, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		acks++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	a := newTestAgent(t, srv.URL, dir, filepath.Join(dir, "uptime"), "tok", "")
	a.state = State{DeviceID: testDevice, DeviceSecret: testSecret}
	a.client.DeviceSecret = testSecret
	ft := &fakeTunnel{}
	a.tunnels = ft

	if err := a.syncTunnel(context.Background()); err != nil {
		t.Fatal(err)
	}
	privA, err := os.ReadFile(filepath.Join(a.keyDir, "hypermesh.key"))
	if err != nil {
		t.Fatal(err)
	}
	ft.mu.Lock()
	if len(ft.applied) != 1 || ft.applied[0].PeerPrivateKey != strings.TrimSpace(string(privA)) {
		ft.mu.Unlock()
		t.Fatalf("first apply %+v", ft.applied)
	}
	ft.mu.Unlock()

	privB, pubB, err := tunnel.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.keyDir, "hypermesh.key"), []byte(privB+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	lag = true
	posts, gets, acks = 0, 0, 0
	mu.Unlock()

	if err := a.syncTunnel(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if posts < 1 || gets < 2 || acks != 0 {
		mu.Unlock()
		t.Fatalf("lagging rotation posts %d gets %d acks %d", posts, gets, acks)
	}
	if shown == pubB {
		mu.Unlock()
		t.Fatal("shown key changed before a GET revealed it")
	}
	mu.Unlock()
	ft.mu.Lock()
	if len(ft.applied) != 1 {
		ft.mu.Unlock()
		t.Fatal("old config must stay applied while GET still shows the old public key")
	}
	ft.mu.Unlock()

	mu.Lock()
	shown = staged
	lag = false
	posts, gets, acks = 0, 0, 0
	mu.Unlock()
	if err := a.syncTunnel(context.Background()); err != nil {
		t.Fatal(err)
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if len(ft.applied) != 2 || ft.applied[1].PeerPrivateKey != privB {
		t.Fatalf("switch apply %+v", ft.applied)
	}
	mu.Lock()
	if acks != 1 {
		mu.Unlock()
		t.Fatalf("rotation must ack the new config, acks %d", acks)
	}
	mu.Unlock()
}

func TestGetRevokedStatusDoesNotRepost(t *testing.T) {
	var buf strings.Builder
	var mu sync.Mutex
	posts := 0
	planePub := ""

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathTunnelPeer, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		posts++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		pub := planePub
		mu.Unlock()
		writeJSON(w, http.StatusOK, api.TunnelSession{
			TunnelID:      "tun-revoked",
			LeaseID:       "lease-revoked",
			Status:        api.TunnelRevoked,
			PeerPublicKey: pub,
		})
	})
	mux.HandleFunc(api.PathTunnelAck, func(w http.ResponseWriter, r *http.Request) {
		t.Error("revoked GET must not ack")
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	a := newTestAgent(t, srv.URL, dir, filepath.Join(dir, "uptime"), "tok", "")
	a.log = slog.New(slog.NewTextHandler(&buf, nil))
	a.state = State{DeviceID: testDevice, DeviceSecret: testSecret}
	a.client.DeviceSecret = testSecret
	if _, _, err := preloadPostedKey(a); err != nil {
		t.Fatal(err)
	}
	a.applied = "was-up"
	ft := &fakeTunnel{}
	a.tunnels = ft

	for i := 0; i < 2; i++ {
		if err := a.syncTunnel(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	if posts != 0 {
		mu.Unlock()
		t.Fatalf("null peer_public_key posts %d, want 0", posts)
	}
	mu.Unlock()
	ft.mu.Lock()
	if len(ft.applied) != 0 || ft.teardowns != 2 {
		ft.mu.Unlock()
		t.Fatalf("null key apply %d teardown %d", len(ft.applied), ft.teardowns)
	}
	ft.mu.Unlock()
	if a.applied != "" {
		t.Fatal("revoked GET must clear the applied fingerprint")
	}
	if strings.Count(buf.String(), "host peer revoked, torn down") != 1 {
		t.Fatalf("revoked log should appear once, logs:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "posting the public key") || strings.Contains(buf.String(), "tunnel sync failed") {
		t.Fatalf("revoked GET must not re-register or fail the sync, logs:\n%s", buf.String())
	}

	mu.Lock()
	planePub = "OTHERPUBLICKEYNOTOURS++++++++++++++++++++="
	mu.Unlock()
	for i := 0; i < 2; i++ {
		if err := a.syncTunnel(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	if posts != 0 {
		mu.Unlock()
		t.Fatalf("mismatched peer_public_key posts %d, want 0", posts)
	}
	mu.Unlock()
	ft.mu.Lock()
	if len(ft.applied) != 0 || ft.teardowns != 4 {
		ft.mu.Unlock()
		t.Fatalf("mismatch apply %d teardown %d", len(ft.applied), ft.teardowns)
	}
	ft.mu.Unlock()
	if strings.Count(buf.String(), "host peer revoked, torn down") != 1 {
		t.Fatalf("revoked streak must not log again, logs:\n%s", buf.String())
	}
}

func TestRevokedPeerDoesNotBringInterfaceUp(t *testing.T) {
	var buf strings.Builder
	posts := 0
	mux := http.NewServeMux()
	mux.HandleFunc(api.PathTunnelPeer, func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"detail":"Tunnel is revoked"}`))
	})
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.TunnelSession{
			TunnelID: "tun-1",
			LeaseID:  "lease-1",
			Status:   api.TunnelRevoked,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	a := newTestAgent(t, srv.URL, dir, filepath.Join(dir, "uptime"), "tok", "")
	a.log = slog.New(slog.NewTextHandler(&buf, nil))
	a.state = State{DeviceID: testDevice, DeviceSecret: testSecret}
	a.client.DeviceSecret = testSecret
	ft := &fakeTunnel{}
	a.tunnels = ft

	if err := a.syncTunnel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.syncTunnel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if posts != 1 {
		t.Fatalf("posts %d, want a single revoked POST", posts)
	}
	ft.mu.Lock()
	nApply, nDown := len(ft.applied), ft.teardowns
	ft.mu.Unlock()
	if nApply != 0 || nDown < 1 {
		t.Fatalf("apply %d teardown %d", nApply, nDown)
	}
	if !strings.Contains(buf.String(), "not bringing WireGuard up") {
		t.Fatalf("log: %s", buf.String())
	}
}

func preloadPostedKey(a *Agent) (pub, priv string, err error) {
	priv, pub, err = tunnel.LoadOrCreatePrivateKey(a.keyDir)
	if err != nil {
		return "", "", err
	}
	a.peerPosted = true
	a.postedPub = pub
	return pub, priv, nil
}

func TestTwoAllocatedTicketsOneHostPeer(t *testing.T) {
	var mu sync.Mutex
	acks, gets := 0, 0

	samePeer := func(tun, lease string) api.TunnelSession {
		return api.TunnelSession{
			TunnelID:        tun,
			LeaseID:         lease,
			Status:          api.TunnelAllocated,
			Endpoint:        "198.51.100.10:51820",
			ServerPublicKey: "serverpub",
			AllowedIPs:      "10.8.0.0/24",
			ClientAddress:   "10.8.0.2/32",
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.EnrollResponse{DeviceID: testDevice, DeviceSecret: testSecret})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{DeviceID: testDevice, PathBState: api.PathBGreen, AcceptedAt: time.Now().UTC()})
	})
	mux.HandleFunc(api.PathTunnelAck, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body api.TunnelAckRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Applied {
			t.Errorf("ack applied=false")
		}
		mu.Lock()
		acks++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	peerPub := acceptTunnelPeer(mux)
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		gets++
		n := gets
		mu.Unlock()
		sess := samePeer("tun-2", "lease-2")
		if n == 1 {
			sess = samePeer("tun-1", "lease-1")
		}
		sess.PeerPublicKey = peerPub()
		writeJSON(w, http.StatusOK, sess)
	})
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, srv.URL, dir, uptime, "tok", "")

	var ops []string
	a.tunnels = &tunnel.Runner{
		ConfigDir: t.TempDir(),
		Quick: func(_ context.Context, args ...string) error {
			mu.Lock()
			ops = append(ops, args[0])
			mu.Unlock()
			return nil
		},
	}

	waitLoop(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return gets >= 2 && acks >= 1
	})

	mu.Lock()
	defer mu.Unlock()
	ups, downs := 0, 0
	for _, op := range ops {
		switch op {
		case "up":
			ups++
		case "down":
			downs++
		}
	}
	if ups != 1 {
		t.Fatalf("same host peer must wg-quick up once, ops=%v", ops)
	}
	if downs != 0 {
		t.Fatalf("lease/tunnel ticket change must not teardown, ops=%v", ops)
	}
	if acks < 1 {
		t.Fatal("expected at least one ack")
	}
}

func TestEmptyAndUnknownKindFail(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	empty := a.runJob(context.Background(), api.Job{JobID: "j-empty", DeviceID: testDevice})
	if empty.Passed || empty.Reason != "empty job kind" {
		t.Fatalf("empty kind %+v", empty)
	}
	unknown := a.runJob(context.Background(), api.Job{JobID: "j-bad", Kind: "not-a-kind", DeviceID: testDevice})
	if unknown.Passed || unknown.Reason != "unknown job kind" {
		t.Fatalf("unknown kind %+v", unknown)
	}
}

func TestLeaseStartEmptyDigestFails(t *testing.T) {
	var mu sync.Mutex
	var kinds []api.JobKind
	var startPassed *bool
	var dockerCalls int

	jobs := []api.Job{
		{JobID: "j-start", Kind: api.KindLeaseStart, DeviceID: testDevice, LeaseID: "lease-1", CatalogID: "llama-3.1-8b-q4", Runtime: "nvidia"},
		{JobID: "j-stop", Kind: api.KindLeaseStop, DeviceID: testDevice, LeaseID: "lease-1"},
	}

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.EnrollResponse{DeviceID: testDevice, DeviceSecret: testSecret})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{DeviceID: testDevice, PathBState: api.PathBGreen, AcceptedAt: time.Now().UTC()})
	})
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if len(jobs) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		j := jobs[0]
		jobs = jobs[1:]
		writeJSON(w, http.StatusOK, j)
	})
	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/j-start/result", func(w http.ResponseWriter, r *http.Request) {
		var body api.JobResultRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		startPassed = &body.Passed
		kinds = append(kinds, api.KindLeaseStart)
		mu.Unlock()
		if body.Passed {
			t.Errorf("empty-digest lease_start must not pass")
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/j-stop/result", func(w http.ResponseWriter, r *http.Request) {
		var body api.JobResultRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Passed {
			t.Errorf("lease_stop should pass")
		}
		mu.Lock()
		kinds = append(kinds, api.KindLeaseStop)
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
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		mu.Lock()
		dockerCalls++
		mu.Unlock()
		return []byte("cid\n"), nil
	}}

	waitLoop(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(kinds) >= 2
	})

	mu.Lock()
	defer mu.Unlock()
	if dockerCalls != 0 {
		t.Fatalf("empty digest must not invoke docker, calls=%d", dockerCalls)
	}
	if startPassed == nil || *startPassed {
		t.Fatalf("empty digest must POST passed=false")
	}
	if len(kinds) < 2 || kinds[0] != api.KindLeaseStart || kinds[1] != api.KindLeaseStop {
		t.Fatalf("kinds %v", kinds)
	}
}

func TestLeaseStartWithDigestUsesNVIDIA(t *testing.T) {
	const ref = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var mu sync.Mutex
	var calls [][]string
	var passed bool

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.EnrollResponse{DeviceID: testDevice, DeviceSecret: testSecret})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{DeviceID: testDevice, PathBState: api.PathBGreen, AcceptedAt: time.Now().UTC()})
	})
	peerPub := acceptTunnelPeer(mux)
	acceptTunnelAck(mux)
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		sess := allocatedTunnel("lease-2")
		sess.PeerPublicKey = peerPub()
		writeJSON(w, http.StatusOK, sess)
	})
	mux.HandleFunc(api.PathJobs, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		done := passed
		mu.Unlock()
		if done {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, api.Job{
			JobID:       "j-dig",
			Kind:        api.KindLeaseStart,
			DeviceID:    testDevice,
			LeaseID:     "lease-2",
			CatalogID:   "llama-3.1-8b-q4",
			ImageDigest: ref,
			FileHash:    fixtureGGUFHash(),
			Runtime:     "nvidia",
		})
	})
	var postedHash string
	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/j-dig/result", func(w http.ResponseWriter, r *http.Request) {
		var body api.JobResultRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		passed = body.Passed
		postedHash = body.ImageHash
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, srv.URL, dir, uptime, "tok", "")
	wantHash := readyLeaseStart(t, a)
	a.tunnels = &fakeTunnel{}
	a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, a ...string) ([]byte, error) {
		mu.Lock()
		calls = append(calls, append([]string{}, a...))
		mu.Unlock()
		return []byte("cid9\n"), nil
	}}

	waitLoop(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return passed && len(calls) >= 2
	})
	if calls[0][0] != "pull" || calls[0][1] != ref {
		t.Fatalf("pull %v", calls[0])
	}
	runArgs := findDockerRun(calls)
	if runArgs == nil {
		t.Fatalf("missing docker run: %+v", calls)
	}
	joined := strings.Join(runArgs, " ")
	if !strings.Contains(joined, "--runtime=nvidia") || strings.Contains(joined, "--gpus") {
		t.Fatalf("docker args %v", runArgs)
	}
	if !strings.Contains(joined, "-p") || !strings.Contains(joined, "10.77.0.2:8080:8080") {
		t.Fatalf("port publish must be WG IP only: %v", runArgs)
	}
	if strings.Contains(joined, "--network=host") || strings.Contains(joined, "--network host") {
		t.Fatalf("workload must not use host network: %v", runArgs)
	}
	if !strings.Contains(joined, "--network") || !strings.Contains(joined, runtime.WorkloadNetworkName) {
		t.Fatalf("want internal workload network: %v", runArgs)
	}
	if strings.Contains(joined, "docker.sock") {
		t.Fatalf("must not mount docker.sock: %v", runArgs)
	}
	if !strings.Contains(joined, "--cap-drop") || !strings.Contains(joined, "ALL") {
		t.Fatalf("want --cap-drop ALL: %v", runArgs)
	}
	if !strings.Contains(joined, "--read-only") {
		t.Fatalf("want --read-only: %v", runArgs)
	}
	if !strings.Contains(joined, "-v") || !strings.Contains(joined, a.cfg.GGUFHostPath()+":"+runtime.ContainerGGUFPath+":ro") {
		t.Fatalf("GGUF bind-mount %v", runArgs)
	}
	if !strings.Contains(joined, "hypermesh-lease-2") || !strings.Contains(joined, ref) {
		t.Fatalf("name/ref %v", runArgs)
	}
	assertLeaseLlamaServer(t, runArgs, "llama-3.1-8b-q4", "1", "16384", "1800")
	if postedHash != wantHash {
		t.Fatalf("lease_start result image_hash %q want GGUF %q", postedHash, wantHash)
	}
}

func assertLeaseLlamaServer(t *testing.T, args []string, catalog, jinja, ctxSize, timeout string) {
	t.Helper()
	if got := dockerEnvValue(args, "LLAMA_ARG_JINJA"); got != jinja {
		t.Fatalf("LLAMA_ARG_JINJA=%q want %q in %v", got, jinja, args)
	}
	if got := dockerEnvValue(args, "LLAMA_ARG_CTX_SIZE"); got != ctxSize {
		t.Fatalf("LLAMA_ARG_CTX_SIZE=%q want %q in %v", got, ctxSize, args)
	}
	if got := dockerEnvValue(args, "LLAMA_ARG_TIMEOUT"); got != timeout {
		t.Fatalf("LLAMA_ARG_TIMEOUT=%q want %q in %v", got, timeout, args)
	}
	if got := dockerEnvValue(args, runtime.ServeAliasEnv); got != catalog {
		t.Fatalf("alias %q want catalog %q in %v", got, catalog, args)
	}
	joined := strings.Join(args, "\n")
	if !strings.Contains(joined, "--entrypoint") || !strings.Contains(joined, "/bin/sh") {
		t.Fatalf("entrypoint override: %v", args)
	}
	if !strings.Contains(joined, "--alias") || strings.Contains(joined, "hypermesh-test") {
		t.Fatalf("alias must be the catalog id, not hypermesh-test: %v", args)
	}
}

func dockerEnvValue(args []string, key string) string {
	prefix := key + "="
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-e" && strings.HasPrefix(args[i+1], prefix) {
			return strings.TrimPrefix(args[i+1], prefix)
		}
	}
	return ""
}

func findDockerRun(calls [][]string) []string {
	for _, c := range calls {
		if len(c) > 0 && c[0] == "run" {
			return c
		}
	}
	return nil
}

func TestLeaseStartBackends(t *testing.T) {
	const ref = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cases := []struct {
		name       string
		backend    string
		wantPass   bool
		wantDocker bool
		wantReason string
	}{
		{name: "omitted", backend: "", wantPass: true, wantDocker: true, wantReason: "lease_start"},
		{name: "cuda-jetson", backend: "cuda-jetson", wantPass: true, wantDocker: true, wantReason: "lease_start"},
		{name: "cuda-sm87", backend: "cuda-sm87", wantPass: true, wantDocker: true, wantReason: "lease_start"},
		{name: "llama-cpp-cpu", backend: "llama-cpp-cpu", wantPass: false, wantDocker: false, wantReason: "This image does not run llama-cpp-cpu."},
		{name: "llama-cpp-vulkan", backend: "llama-cpp-vulkan", wantPass: false, wantDocker: false, wantReason: "This image does not run llama-cpp-vulkan."},
		{name: "hip-rocm", backend: "hip-rocm", wantPass: false, wantDocker: false, wantReason: "This image does not run hip-rocm."},
		{name: "disk-io", backend: "disk-io", wantPass: false, wantDocker: false, wantReason: "This image does not run disk-io."},
		{name: "unknown", backend: "not-a-backend", wantPass: false, wantDocker: false, wantReason: "This image does not run not-a-backend."},
	}
	for _, tc := range cases {
		if tc.backend == "llama-cpp-cpu" && (runtime.MetalImageEnabled() || runtime.CPUImageEnabled()) {
			t.Run(tc.name, func(t *testing.T) {
				t.Skip("darwin image: llama-cpp-cpu is the Metal lab path")
			})
			continue
		}
		if tc.backend == "hip-rocm" && runtime.HIPImageEnabled() {
			t.Run(tc.name, func(t *testing.T) {
				t.Skip("rocm image: hip-rocm is the HIP path")
			})
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			uptime := filepath.Join(dir, "uptime")
			_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
			var dockerCalls [][]string
			a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
			if tc.wantPass {
				_ = readyLeaseStart(t, a)
			}
			wantJinja, wantCtx, wantTimeout := "1", "16384", "1800"
			if tc.name == "cuda-jetson" {
				a.cfg.LlamaJinja = "1"
				a.cfg.LlamaCtxSize = "32768"
				a.cfg.LlamaTimeout = "2400"
				wantCtx, wantTimeout = "32768", "2400"
			}
			a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				dockerCalls = append(dockerCalls, append([]string{}, args...))
				return []byte("cid\n"), nil
			}}
			job := api.Job{
				JobID:       "j-backend",
				Kind:        api.KindLeaseStart,
				DeviceID:    testDevice,
				LeaseID:     "lease-b",
				CatalogID:   "llama-3.1-8b-q4",
				ImageDigest: ref,
				Runtime:     "nvidia",
				Backend:     tc.backend,
			}
			if tc.wantPass {
				job.FileHash = fixtureGGUFHash()
			}
			got := a.runJob(context.Background(), job)
			if got.Passed != tc.wantPass || got.Reason != tc.wantReason {
				t.Fatalf("result %+v want pass=%v reason=%q", got, tc.wantPass, tc.wantReason)
			}
			if tc.wantDocker {
				if len(dockerCalls) < 2 || dockerCalls[0][0] != "pull" || dockerCalls[0][1] != ref {
					t.Fatalf("expected nvidia pull, calls=%v", dockerCalls)
				}
				runArgs := findDockerRun(dockerCalls)
				if runArgs == nil {
					t.Fatalf("missing docker run: %v", dockerCalls)
				}
				joined := strings.Join(runArgs, " ")
				if !strings.Contains(joined, "--runtime=nvidia") || strings.Contains(joined, "--gpus") {
					t.Fatalf("docker args %v", runArgs)
				}
				if strings.Contains(joined, "--network=host") || !strings.Contains(joined, runtime.WorkloadNetworkName) {
					t.Fatalf("workload jail network: %v", runArgs)
				}
				assertLeaseLlamaServer(t, runArgs, "llama-3.1-8b-q4", wantJinja, wantCtx, wantTimeout)
			} else if len(dockerCalls) != 0 {
				t.Fatalf("stub must not call docker, calls=%v", dockerCalls)
			}
		})
	}
}

func TestLeaseStopIdentity(t *testing.T) {
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
		jobLease   string
		wantPass   bool
		wantReason string
		wantDocker bool
		wantKeep   bool
	}{
		{
			name:       "match stops",
			handle:     &handle,
			jobLease:   localID,
			wantPass:   true,
			wantReason: "lease_stop",
			wantDocker: true,
		},
		{
			name:       "mismatch refuses",
			handle:     &handle,
			jobLease:   "lease-other",
			wantPass:   false,
			wantReason: "lease_id mismatch",
			wantKeep:   true,
		},
		{
			name:       "empty lease_id with handle refuses",
			handle:     &handle,
			jobLease:   "",
			wantPass:   false,
			wantReason: "lease_id mismatch",
			wantKeep:   true,
		},
		{
			name:       "whitespace lease_id with handle refuses",
			handle:     &handle,
			jobLease:   "   ",
			wantPass:   false,
			wantReason: "lease_id mismatch",
			wantKeep:   true,
		},
		{
			name:       "no handle soft no-op",
			jobLease:   "lease-anything",
			wantPass:   true,
			wantReason: "lease_stop",
		},
		{
			name:       "no handle empty lease_id still no-op",
			jobLease:   "",
			wantPass:   true,
			wantReason: "lease_stop",
		},
		{
			name:       "disk handle mismatch refuses",
			handle:     &handle,
			disk:       true,
			jobLease:   "lease-other",
			wantPass:   false,
			wantReason: "lease_id mismatch",
			wantKeep:   true,
		},
		{
			name:       "disk handle match stops",
			handle:     &handle,
			disk:       true,
			jobLease:   localID,
			wantPass:   true,
			wantReason: "lease_stop",
			wantDocker: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			uptime := filepath.Join(dir, "uptime")
			if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			var dockerCalls [][]string
			a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
			ft := &fakeTunnel{}
			a.tunnels = ft
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

			got := a.runJob(context.Background(), api.Job{
				JobID:    "j-stop",
				Kind:     api.KindLeaseStop,
				DeviceID: testDevice,
				LeaseID:  tc.jobLease,
			})
			if got.Passed != tc.wantPass || got.Reason != tc.wantReason {
				t.Fatalf("result %+v want pass=%v reason=%q", got, tc.wantPass, tc.wantReason)
			}
			if tc.wantDocker {
				assertDockerStopThenRM(t, dockerCalls, handle.Name)
			} else if len(dockerCalls) != 0 {
				t.Fatalf("docker must not run, calls=%v", dockerCalls)
			}
			ft.mu.Lock()
			teardowns := ft.teardowns
			ft.mu.Unlock()
			if teardowns != 0 {
				t.Fatalf("lease_stop must not bounce WireGuard, teardowns=%d", teardowns)
			}

			kept := a.lease != nil || a.loadLease() != nil
			if kept != tc.wantKeep {
				t.Fatalf("handle kept=%v want %v mem=%+v disk=%+v", kept, tc.wantKeep, a.lease, a.loadLease())
			}
		})
	}
}

func TestLeaseStopMismatchPostsPassedFalseOnly(t *testing.T) {
	var mu sync.Mutex
	var posted map[string]any
	var dockerCalls int

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.EnrollResponse{DeviceID: testDevice, DeviceSecret: testSecret})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{DeviceID: testDevice, PathBState: api.PathBGreen, AcceptedAt: time.Now().UTC()})
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
			JobID:    "j-mismatch",
			Kind:     api.KindLeaseStop,
			DeviceID: testDevice,
			LeaseID:  "lease-other",
		})
	})
	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/j-mismatch/result", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("result body: %v", err)
		}
		mu.Lock()
		posted = body
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
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
	if err := a.saveLease(&runtime.Handle{
		Name:        "hypermesh-lease-running",
		LeaseID:     "lease-running",
		ContainerID: "cid-local",
	}); err != nil {
		t.Fatal(err)
	}
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		mu.Lock()
		dockerCalls++
		mu.Unlock()
		return []byte("ok\n"), nil
	}}

	waitLoop(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return posted != nil
	})

	mu.Lock()
	defer mu.Unlock()
	if dockerCalls != 0 {
		t.Fatalf("mismatch must not docker stop, calls=%d", dockerCalls)
	}
	passed, _ := posted["passed"].(bool)
	if passed {
		t.Fatalf("mismatch must POST passed=false, body=%v", posted)
	}
	for _, extra := range []string{"reason", "reason_code", "lease_id", "sell_state", "path_b"} {
		if _, ok := posted[extra]; ok {
			t.Fatalf("result body must stay {passed, image_hash?}, extra %s in %v", extra, posted)
		}
	}
}

func assertDockerStopThenRM(t *testing.T, calls [][]string, name string) {
	t.Helper()
	if len(calls) != 2 || calls[0][0] != "stop" || calls[1][0] != "rm" {
		t.Fatalf("expected docker stop then rm, calls=%v", calls)
	}
	if name != "" && calls[1][len(calls[1])-1] != name {
		t.Fatalf("rm target %v want %s", calls[1], name)
	}
}

func dockerNameArg(args []string) string {
	for i, a := range args {
		if a == "--name" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestLeaseStopThenStartFreesExitedName(t *testing.T) {
	// Same leftover as a stopped lease: docker stop left hypermesh-<lease_id> Exited,
	// next lease_start with that lease_id hit docker exit 125.
	const leaseID = "22b3f0d7-f4dc-4423-8250-58449f15c61d"
	name := runtime.ContainerName(leaseID)
	const digest = "example.invalid/hypermesh/workload@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	present := map[string]string{}
	var dockerCalls [][]string
	var teardowns int

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ft := &fakeTunnel{}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	ggufHash := readyLeaseStart(t, a)
	a.tunnels = ft
	a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		dockerCalls = append(dockerCalls, append([]string{}, args...))
		if len(args) == 0 {
			return nil, errors.New("empty docker")
		}
		switch args[0] {
		case "pull", "image":
			return []byte{}, nil
		case "run":
			got := dockerNameArg(args)
			if got == "" {
				got = name
			}
			if _, ok := present[got]; ok {
				return []byte(`Conflict: The container name "/` + got + `" is already in use`), errors.New("exit status 125")
			}
			present[got] = "running"
			return []byte("cid-reuse\n"), nil
		case "stop":
			target := args[len(args)-1]
			if target == "cid-reuse" || target == name {
				if _, ok := present[name]; ok {
					present[name] = "exited"
				}
			}
			return []byte{}, nil
		case "rm":
			target := args[len(args)-1]
			if target == "cid-reuse" || target == name {
				delete(present, name)
			} else {
				delete(present, target)
			}
			return []byte{}, nil
		default:
			return []byte{}, nil
		}
	}}

	startJob := api.Job{
		JobID: "j-start-1", Kind: api.KindLeaseStart, DeviceID: testDevice,
		LeaseID: leaseID, CatalogID: "llama-3.1-8b-q4",
		ImageDigest: digest, FileHash: ggufHash, Runtime: "nvidia",
	}
	first := a.runJob(context.Background(), startJob)
	if !first.Passed {
		t.Fatalf("first lease_start %+v", first)
	}
	stop := a.runJob(context.Background(), api.Job{
		JobID: "j-stop-1", Kind: api.KindLeaseStop, DeviceID: testDevice, LeaseID: leaseID,
	})
	if !stop.Passed {
		t.Fatalf("lease_stop %+v", stop)
	}
	if stop.ImageHash != digest && stop.ImageHash != "sha256:fixture" && stop.ImageHash != ggufHash {
		t.Fatalf("lease_stop must not invent a hash: %q", stop.ImageHash)
	}
	if _, ok := present[name]; ok {
		t.Fatalf("matching stop must rm leftover Exited name, leftover=%v", present)
	}
	startJob.JobID = "j-start-2"
	second := a.runJob(context.Background(), startJob)
	if !second.Passed {
		t.Fatalf("same lease_id must not collide on leftover Exited name: %+v calls=%v", second, dockerCalls)
	}
	ft.mu.Lock()
	teardowns = ft.teardowns
	ft.mu.Unlock()
	if teardowns != 0 {
		t.Fatalf("lease_stop must not bounce WireGuard, teardowns=%d", teardowns)
	}
}

func TestLeaseStartCUDAJetsonEmptyDigestStillFails(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var dockerCalls int
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		dockerCalls++
		return []byte("cid\n"), nil
	}}
	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-empty",
		Kind:     api.KindLeaseStart,
		DeviceID: testDevice,
		LeaseID:  "lease-1",
		Runtime:  "nvidia",
		Backend:  "cuda-jetson",
	})
	if got.Passed || got.Reason != "no image_digest; no soak image" {
		t.Fatalf("empty digest must still fail: %+v", got)
	}
	if dockerCalls != 0 {
		t.Fatalf("empty digest must not invoke docker, calls=%d", dockerCalls)
	}
}

func TestLeaseStartRequiresCatalogAlias(t *testing.T) {
	const ref = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var dockerCalls int
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	_ = readyLeaseStart(t, a)
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		dockerCalls++
		return []byte("cid\n"), nil
	}}
	got := a.runJob(context.Background(), api.Job{
		JobID:       "j-noalias",
		Kind:        api.KindLeaseStart,
		DeviceID:    testDevice,
		LeaseID:     "lease-noalias",
		ImageDigest: ref,
		FileHash:    fixtureGGUFHash(),
		Runtime:     "nvidia",
		Backend:     "cuda-jetson",
	})
	if got.Passed || got.Reason != "catalog_id required for llama-server alias" {
		t.Fatalf("missing catalog id must fail closed: %+v", got)
	}
	if dockerCalls != 0 {
		t.Fatalf("missing catalog id must not invoke docker, calls=%d", dockerCalls)
	}
}

func TestLeaseStartBareDigestReasonUnchanged(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var dockerCalls int
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		dockerCalls++
		return []byte("cid\n"), nil
	}}
	got := a.runJob(context.Background(), api.Job{
		JobID:       "j-bare",
		Kind:        api.KindLeaseStart,
		DeviceID:    testDevice,
		LeaseID:     "lease-bare",
		CatalogID:   "llama-3.1-8b-q4",
		ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Runtime:     "nvidia",
	})
	if got.Passed || got.Reason != "image_digest is not repo@digest" {
		t.Fatalf("bare digest reason unchanged: %+v", got)
	}
	if got.ImageHash != "" {
		t.Fatalf("must not invent image_hash: %+v", got)
	}
	if dockerCalls != 0 {
		t.Fatalf("bare digest must not invoke docker, calls=%d", dockerCalls)
	}
}

func TestLeaseStartDockerErrorAppearsInReason(t *testing.T) {
	const ref = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const hook = "nvidia-cdi-hook cudacompat: slice bounds out of range [:73] with capacity 71"
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var logBuf strings.Builder
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	_ = readyLeaseStart(t, a)
	a.log = slog.New(slog.NewTextHandler(&logBuf, nil))
	a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "run" {
			return []byte(hook + "\n"), errors.New("exit status 1")
		}
		if len(args) > 0 && args[0] == "inspect" {
			return []byte("created\n"), nil
		}
		return []byte{}, nil
	}}
	got := a.runJob(context.Background(), api.Job{
		JobID:       "j-cdi",
		Kind:        api.KindLeaseStart,
		DeviceID:    testDevice,
		LeaseID:     "lease-cdi",
		CatalogID:   "llama-3.1-8b-q4",
		ImageDigest: ref,
		FileHash:    fixtureGGUFHash(),
		Runtime:     "nvidia",
	})
	if got.Passed {
		t.Fatalf("start must fail: %+v", got)
	}
	if got.ImageHash != "" {
		t.Fatalf("must not invent image_hash: %+v", got)
	}
	if !strings.HasPrefix(got.Reason, "runtime start failed:") {
		t.Fatalf("reason must wrap Start() err, got %q", got.Reason)
	}
	if !strings.Contains(got.Reason, "hypermesh runtime: docker start") || !strings.Contains(got.Reason, "nvidia-cdi-hook") {
		t.Fatalf("reason missing wrapped docker error: %q", got.Reason)
	}
	if got.Reason == "runtime start failed" {
		t.Fatal("opaque runtime start failed must not be posted")
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "lease_start failed") || !strings.Contains(logs, "nvidia-cdi-hook") {
		t.Fatalf("journal must include wrapped Start() error: %s", logs)
	}
	body := jobResultBody(api.KindLeaseStart, got)
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"reason"`) || strings.Contains(string(raw), "reason_code") {
		t.Fatalf("HTTP result must stay {passed, image_hash?}: %s", raw)
	}
}

func TestLeaseStartLocalImageAfterPullUnauthorized(t *testing.T) {
	const ref = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	wantHash := readyLeaseStart(t, a)
	a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch {
		case len(args) > 0 && args[0] == "pull":
			return []byte("Error response from daemon: unauthorized"), errors.New("exit status 1")
		case len(args) >= 2 && args[0] == "image" && args[1] == "inspect":
			return []byte("sha256:localpin\n"), nil
		case len(args) > 0 && args[0] == "network":
			return []byte{}, nil
		case len(args) > 0 && args[0] == "run":
			return []byte("cid-local\n"), nil
		default:
			t.Fatalf("unexpected docker %v", args)
			return nil, nil
		}
	}}
	got := a.runJob(context.Background(), api.Job{
		JobID:       "j-local",
		Kind:        api.KindLeaseStart,
		DeviceID:    testDevice,
		LeaseID:     "lease-local",
		CatalogID:   "llama-3.1-8b-q4",
		ImageDigest: ref,
		FileHash:    wantHash,
		Runtime:     "nvidia",
	})
	if !got.Passed || got.Reason != "lease_start" {
		t.Fatalf("local pin must not fail Start: %+v", got)
	}
	if got.ImageHash != wantHash {
		t.Fatalf("must POST GGUF hash, not OCI pin: %+v", got)
	}
}

func TestHeartbeatLogsNVIDIABackend(t *testing.T) {
	var mu sync.Mutex
	var heartbeats int
	mux := http.NewServeMux()
	mux.HandleFunc(api.PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.EnrollResponse{DeviceID: testDevice, DeviceSecret: testSecret})
	})
	mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		heartbeats++
		mu.Unlock()
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
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var logBuf syncBuf
	a := newTestAgent(t, srv.URL, dir, uptime, "tok", "")
	a.log = slog.New(slog.NewTextHandler(&logBuf, nil))
	a.tunnels = &fakeTunnel{}

	waitLoop(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		// Receiving the request does not mean the agent has handled its response.
		return heartbeats >= 1 && strings.Contains(logBuf.String(), "backend="+runtime.HostLogLabel())
	})
	out := logBuf.String()
	if !strings.Contains(out, "backend="+runtime.HostLogLabel()) {
		t.Fatalf("heartbeat must log %s, got %q", runtime.HostLogLabel(), out)
	}
	for _, leak := range []string{"tok/s", "TOPS", "tokens/sec"} {
		if strings.Contains(out, leak) {
			t.Fatalf("unexpected %s: %q", leak, out)
		}
	}
}

func TestHeartbeatLoadedModelHashFromLease(t *testing.T) {
	const digest = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	cases := []struct {
		name   string
		memory bool
		disk   bool
	}{
		{name: "memory handle", memory: true},
		{name: "disk handle", disk: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got api.HeartbeatRequest
			mux := http.NewServeMux()
			mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Errorf("heartbeat decode: %v", err)
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
			if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			a := newTestAgent(t, srv.URL, dir, uptime, "", "")
			a.state.DeviceID = testDevice
			a.client.DeviceSecret = testSecret
			h := &runtime.Handle{
				Name:        "hypermesh-lease-1",
				LeaseID:     "lease-1",
				ImageDigest: digest,
				ContainerID: "cid-1",
			}
			if tc.memory {
				a.lease = h
			}
			if tc.disk {
				if err := a.saveLease(h); err != nil {
					t.Fatal(err)
				}
				a.lease = nil
			}

			if err := a.heartbeat(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got.LoadedModelHash != digest {
				t.Fatalf("loaded_model_hash %q want handle ImageDigest %q", got.LoadedModelHash, digest)
			}
		})
	}
}

func TestHeartbeatOmitsLoadedModelHashWithoutLease(t *testing.T) {
	cases := []struct {
		name   string
		handle *runtime.Handle
	}{
		{name: "no lease"},
		{name: "empty digest", handle: &runtime.Handle{LeaseID: "lease-1", ImageDigest: "   "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw []byte
			mux := http.NewServeMux()
			mux.HandleFunc(api.PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
				var err error
				raw, err = io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read body: %v", err)
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
			if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			a := newTestAgent(t, srv.URL, dir, uptime, "", "")
			a.state.DeviceID = testDevice
			a.client.DeviceSecret = testSecret
			a.lease = tc.handle

			if err := a.heartbeat(context.Background()); err != nil {
				t.Fatal(err)
			}
			var got api.HeartbeatRequest
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.LoadedModelHash != "" {
				t.Fatalf("loaded_model_hash must be empty, got %q", got.LoadedModelHash)
			}
			if strings.Contains(string(raw), "loaded_model_hash") {
				t.Fatalf("omitempty must omit loaded_model_hash: %s", raw)
			}
		})
	}
}

func TestLeaseStartResultImageHashIsGGUF(t *testing.T) {
	const ref = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	wantHash := readyLeaseStart(t, a)
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("cid\n"), nil
	}}
	got := a.runJob(context.Background(), api.Job{
		JobID:       "j-hash",
		Kind:        api.KindLeaseStart,
		DeviceID:    testDevice,
		LeaseID:     "lease-hash",
		CatalogID:   "llama-3.1-8b-q4",
		ImageDigest: ref,
		FileHash:    wantHash,
		Runtime:     "nvidia",
	})
	if !got.Passed || got.ImageHash != wantHash {
		t.Fatalf("lease_start must POST GGUF sha256 as image_hash: %+v", got)
	}
	if got.ImageHash == ref {
		t.Fatal("must not echo the OCI pin as image_hash")
	}
	body := jobResultBody(api.KindLeaseStart, got)
	if body.ImageHash != wantHash || !body.Passed {
		t.Fatalf("result body %+v", body)
	}
}

func TestLabSoakPassedRequiresNonEmptyImageHash(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.pathb.Endpoint = "http://127.0.0.1:1/v1/chat/completions"
	a.pathb.HTTP = &http.Client{Timeout: 150 * time.Millisecond}

	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-lab-hash",
		Kind:     api.KindPathB,
		DeviceID: testDevice,
		SuiteID:  "thin-v1",
	})
	if !got.Passed || strings.TrimSpace(got.ImageHash) == "" {
		t.Fatalf("lab soak passed=true requires non-empty image_hash: %+v", got)
	}
}

func TestLabSoakFailsWhenEmptyTelemAndNoLease(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.cfg.ImageHash = ""
	a.pathb.Endpoint = "http://127.0.0.1:1/v1/chat/completions"
	a.pathb.HTTP = &http.Client{Timeout: 150 * time.Millisecond}

	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-lab-empty",
		Kind:     api.KindPathB,
		DeviceID: testDevice,
		SuiteID:  "thin-v1",
	})
	if got.Passed || got.Reason != "no image_hash" {
		t.Fatalf("empty telem + no lease must fail lab soak: %+v", got)
	}
	if got.ImageHash != "" {
		t.Fatalf("must not invent image_hash: %+v", got)
	}
}

func TestLabSoakUsesLeaseDigestWhenTelemHashEmpty(t *testing.T) {
	const digest = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.cfg.ImageHash = ""
	a.lease = &runtime.Handle{LeaseID: "lease-1", ImageDigest: digest}
	a.pathb.Endpoint = "http://127.0.0.1:1/v1/chat/completions"
	a.pathb.HTTP = &http.Client{Timeout: 150 * time.Millisecond}

	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-lab-lease",
		Kind:     api.KindPathB,
		DeviceID: testDevice,
		SuiteID:  "thin-v1",
	})
	if !got.Passed || got.ImageHash != digest {
		t.Fatalf("lab soak must use lease ImageDigest when telem hash is empty: %+v", got)
	}
}

func TestStubBackendFailsLabSoakAndInstanceCheck(t *testing.T) {
	const completion = "UNIQUE_COMPLETION_STUB"
	var probes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": completion}},
			},
		})
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.pathb.Endpoint = srv.URL
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("stub must not call docker")
		return nil, nil
	}}

	backends := []string{"llama-cpp-cpu", "llama-cpp-vulkan", "hip-rocm", "disk-io"}
	kinds := []api.JobKind{api.KindPathB, api.KindInstanceCheck}
	for _, kind := range kinds {
		for _, backend := range backends {
			if backend == "llama-cpp-cpu" && (runtime.MetalImageEnabled() || runtime.CPUImageEnabled()) {
				continue
			}
			if backend == "hip-rocm" && runtime.HIPImageEnabled() {
				continue
			}
			got := a.runJob(context.Background(), api.Job{
				JobID:    "j-stub",
				Kind:     kind,
				DeviceID: testDevice,
				SuiteID:  "thin-v1",
				Runtime:  "nvidia",
				Backend:  backend,
			})
			want := "This image does not run " + backend + "."
			if got.Passed || got.Reason != want {
				t.Fatalf("%s %s: %+v want %q", kind, backend, got, want)
			}
		}
	}
	if probes != 0 {
		t.Fatalf("stub instance_check must not probe, probes=%d", probes)
	}
}

func TestLabSoakOmittedBackendStillRunsThinV1(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.pathb.Endpoint = "http://127.0.0.1:1/v1/chat/completions"
	a.pathb.HTTP = &http.Client{Timeout: 150 * time.Millisecond}

	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-lab",
		Kind:     api.KindPathB,
		DeviceID: testDevice,
		SuiteID:  "thin-v1",
	})
	if !got.Passed || strings.TrimSpace(got.ImageHash) == "" {
		t.Fatalf("omitted backend is cuda-jetson; lab soak telem must still pass with image_hash: %+v", got)
	}
}

func TestInstanceCheckFailsWhenEndpointDown(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var logBuf strings.Builder
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.log = slog.New(slog.NewTextHandler(&logBuf, nil))
	setSafeWorkloadInspection(a)
	a.pathb.Endpoint = "http://127.0.0.1:1/v1/chat/completions"
	a.pathb.HTTP = &http.Client{Timeout: 150 * time.Millisecond}

	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-check",
		Kind:     api.KindInstanceCheck,
		DeviceID: testDevice,
		SuiteID:  "thin-v1",
	})
	if got.Passed {
		t.Fatalf("instance_check must fail when completions are down: %+v", got)
	}
	assertNoPromptInLogs(t, logBuf.String(), got.Reason)
}

func TestInstanceCheckPassesOnFakeCompletions(t *testing.T) {
	const completion = "UNIQUE_COMPLETION_XYZ"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": completion}},
			},
		})
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var logBuf strings.Builder
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.log = slog.New(slog.NewTextHandler(&logBuf, nil))
	setSafeWorkloadInspection(a)
	a.pathb.Endpoint = srv.URL

	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-check",
		Kind:     api.KindInstanceCheck,
		DeviceID: testDevice,
		SuiteID:  "thin-v1",
	})
	if !got.Passed || strings.TrimSpace(got.ImageHash) == "" {
		t.Fatalf("instance_check should pass with a real choice and image_hash: %+v", got)
	}
	out := logBuf.String() + " " + got.Reason
	assertNoPromptInLogs(t, out, got.Reason)
	if strings.Contains(out, completion) {
		t.Fatal("completion text leaked into logs")
	}
}

func TestLabSoakStillPassesWhenCompletionsDown(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var logBuf strings.Builder
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.log = slog.New(slog.NewTextHandler(&logBuf, nil))
	a.pathb.Endpoint = "http://127.0.0.1:1/v1/chat/completions"
	a.pathb.HTTP = &http.Client{Timeout: 150 * time.Millisecond}

	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-lab",
		Kind:     api.KindPathB,
		DeviceID: testDevice,
		SuiteID:  "thin-v1",
	})
	if !got.Passed || strings.TrimSpace(got.ImageHash) == "" {
		t.Fatalf("lab soak must still pass telem with image_hash when inference is down: %+v", got)
	}
	assertNoPromptInLogs(t, logBuf.String(), got.Reason)
}

func TestLabSoakFailsWhenBootIDChanged(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var logBuf strings.Builder
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.log = slog.New(slog.NewTextHandler(&logBuf, nil))
	a.pathb.Endpoint = "http://127.0.0.1:1/v1/chat/completions"
	a.pathb.HTTP = &http.Client{Timeout: 150 * time.Millisecond}

	if err := os.WriteFile(filepath.Join(dir, "boot.json"), []byte(`{"boot_id":"previous-boot","uptime_s":99}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-lab",
		Kind:     api.KindPathB,
		DeviceID: testDevice,
		SuiteID:  "thin-v1",
	})
	if got.Passed || got.Reason != "reboot" {
		t.Fatalf("lab soak must fail on reboot: %+v", got)
	}
	assertNoPromptInLogs(t, logBuf.String(), got.Reason)
}

func TestDefaultControlPlaneURL(t *testing.T) {
	t.Setenv("HYPERMESH_CONTROL_PLANE_URL", "")
	t.Setenv("HYPERMESH_LISTEN_PORT", "")
	t.Setenv("HYPERMESH_COMPLETIONS_URL", "")
	t.Setenv("HYPERMESH_GGUF_PATH", "")
	t.Setenv("HYPERMESH_DISK_FLOOR_BYTES", "")
	t.Setenv("HYPERMESH_DISK_MARGIN_BYTES", "")
	t.Setenv("HYPERMESH_METRICS_ADDR", "")
	t.Setenv("HYPERMESH_METRICS_ALLOW_NON_LOOPBACK", "")
	cfg := FromEnv()
	if cfg.ControlPlaneURL != "https://api.test.hyperme.sh" {
		t.Fatalf("default control plane %q", cfg.ControlPlaneURL)
	}
	if cfg.PublishPort() != "8080" {
		t.Fatalf("default listen port %q", cfg.PublishPort())
	}
	if cfg.CompletionsEndpoint() != "http://127.0.0.1:8080/v1/chat/completions" {
		t.Fatalf("default completions %q", cfg.CompletionsEndpoint())
	}
	if cfg.GGUFHostPath() != DefaultModelPath {
		t.Fatalf("unset GGUF path = %q want %q", cfg.GGUFHostPath(), DefaultModelPath)
	}
	floor, err := cfg.DiskFloorBytes()
	if err != nil || floor != 2<<30 {
		t.Fatalf("disk floor %d err=%v", floor, err)
	}
	margin, err := cfg.DiskMarginBytes()
	if err != nil || margin != 64<<20 {
		t.Fatalf("disk margin %d err=%v", margin, err)
	}
	if cfg.MetricsAddr != "" || cfg.MetricsAllowNonLoopback {
		t.Fatalf("metrics default %q allow %v", cfg.MetricsAddr, cfg.MetricsAllowNonLoopback)
	}
	if cfg.LlamaJinja != "1" || cfg.LlamaCtxSize != "16384" || cfg.LlamaTimeout != "1800" {
		t.Fatalf("llama defaults %+v", cfg)
	}
}

func TestLlamaServerEnvOverrides(t *testing.T) {
	t.Setenv("HYPERMESH_LLAMA_JINJA", "1")
	t.Setenv("HYPERMESH_LLAMA_CTX_SIZE", "32768")
	t.Setenv("HYPERMESH_LLAMA_TIMEOUT", "900")
	cfg := FromEnv()
	if cfg.LlamaJinja != "1" || cfg.LlamaCtxSize != "32768" || cfg.LlamaTimeout != "900" {
		t.Fatalf("llama env %+v", cfg)
	}
}

func assertNoPromptInLogs(t *testing.T, parts ...string) {
	t.Helper()
	joined := strings.Join(parts, "\n")
	for _, leak := range []string{
		"Reply with the single word ready.",
		"Reply with",
		"renter",
	} {
		if strings.Contains(joined, leak) {
			t.Fatalf("prompt leaked into logs/reason: %q in %q", leak, joined)
		}
	}
}

func TestValidateTenant(t *testing.T) {
	cfg := FromEnv()
	cfg.TenantID = "not-a-uuid"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected tenant uuid error")
	}
}

func TestDeviceIDWithoutSecretErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "device.json"), []byte(`{"device_id":"`+testDevice+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ControlPlaneURL: "http://127.0.0.1:8010",
		TenantID:        testTenant,
		StateDir:        dir,
		HeartbeatEvery:  time.Hour,
		JobEvery:        time.Hour,
	}
	a, err := New(cfg, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	err = a.ensureEnrolled(context.Background())
	if err == nil || !strings.Contains(err.Error(), "enroll-token") {
		t.Fatalf("got %v", err)
	}
}

func fixtureGGUFHash() string {
	sum := sha256.Sum256([]byte("fixture-gguf-weights"))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func writeTestGGUF(t *testing.T, dir string) (path, hash string) {
	t.Helper()
	path = filepath.Join(dir, "model.gguf")
	body := []byte("fixture-gguf-weights")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, fixtureGGUFHash()
}

func readyLeaseStart(t *testing.T, a *Agent) string {
	t.Helper()
	path, hash := writeTestGGUF(t, a.cfg.StateDir)
	a.cfg.GGUFPath = path
	a.bindIP = "10.77.0.2/32"
	return hash
}

func allocatedTunnel(leaseID string) api.TunnelSession {
	return api.TunnelSession{
		TunnelID:        "tun-live",
		LeaseID:         leaseID,
		Status:          api.TunnelAllocated,
		Endpoint:        "wg.test.hyperme.sh:51820",
		ServerPublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		AllowedIPs:      "10.77.0.0/24",
		ClientAddress:   "10.77.0.2/32",
	}
}

func acceptTunnelAck(mux *http.ServeMux) {
	mux.HandleFunc(api.PathTunnelAck, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func newTestAgent(t *testing.T, url, dir, uptime, enroll, image string) *Agent {
	t.Helper()
	cfg := Config{
		ControlPlaneURL: url,
		TenantID:        testTenant,
		EnrollToken:     enroll,
		StateDir:        dir,
		HeartbeatEvery:  40 * time.Millisecond,
		JobEvery:        40 * time.Millisecond,
		ImageHash:       image,
	}
	if image == "" {
		cfg.ImageHash = "sha256:fixture"
	}
	a, err := New(cfg, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	boot := filepath.Join(dir, "proc-boot-id")
	if err := os.WriteFile(boot, []byte("test-boot-id\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.telem = telem.Source{
		UptimePath: uptime,
		BootIDPath: boot,
		LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
	}
	a.pathb.Collector = a.telem
	a.keyDir = t.TempDir()
	a.countLeases = func(context.Context) (int, bool) {
		h := a.currentLease()
		if h != nil && strings.TrimSpace(h.LeaseID) != "" {
			return 1, true
		}
		return 0, true
	}
	// Existing lease tests do not listen on the tunnel address. Ingress
	// tests clear this and use the real /health wait.
	a.leaseReady = func(context.Context, *runtime.Handle) error { return nil }
	return a
}

func acceptTunnelPeer(mux *http.ServeMux) func() string {
	var mu sync.Mutex
	var pub string
	mux.HandleFunc(api.PathTunnelPeer, func(w http.ResponseWriter, r *http.Request) {
		var body api.TunnelPeerRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		pub = body.PeerPublicKey
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return pub
	}
}

func waitLoop(t *testing.T, a *Agent, ok func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			cancel()
			<-done
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("loop did not reach expected state")
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// syncBuf is a strings.Builder safe for the heartbeat and job goroutines.
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestFreshEnrollmentChangesKeyAndRestartReusesIdentity(t *testing.T) {
    var requests []api.EnrollRequest
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        var body api.EnrollRequest
        if err := json.NewDecoder(r.Body).Decode(&body); err != nil { t.Fatal(err) }
        requests = append(requests, body)
        writeJSON(w, http.StatusOK, api.EnrollResponse{
            DeviceID: body.EnrollToken, DeviceSecret: testSecret,
        })
    }))
    defer srv.Close()
    dir := t.TempDir()
    uptime := filepath.Join(dir, "uptime")
    if err := os.WriteFile(uptime, []byte("1 1\n"), 0600); err != nil { t.Fatal(err) }
    first := newTestAgent(t, srv.URL, dir, uptime, "first-enrollment", "")
    first.cfg.HostName = "configured-box"
    first.cfg.Reenroll = true
    if err := first.ensureEnrolled(context.Background()); err != nil { t.Fatal(err) }
    if err := first.ensureEnrolled(context.Background()); err != nil { t.Fatal(err) }
    checkpoint:=filepath.Join(dir,"machine-identity.json")
    if err:=os.WriteFile(checkpoint,[]byte(`{"device":"first-enrollment","request":"old-request"}`),0600);err!=nil{t.Fatal(err)}
    second := newTestAgent(t, srv.URL, dir, uptime, "second-enrollment", "")
    second.cfg.HostName = "configured-box"
    second.cfg.Reenroll = true
    second.tunnels = &fakeTunnel{}
    if err := second.ensureEnrolled(context.Background()); err != nil { t.Fatal(err) }
    if _,err:=os.Stat(checkpoint);!os.IsNotExist(err){t.Fatal("retained old signing checkpoint",err)}
    if second.client.DeviceID!="second-enrollment"{t.Fatal("client retained old device")}
    token,err:=second.client.Credential(context.Background());if token!=""||err==nil{t.Fatal("replacement silently used bootstrap without binding",err)}
    if len(requests) != 2 { t.Fatalf("enroll requests: %d", len(requests)) }
    if requests[0].HostName != "configured-box" || requests[1].HostName != "configured-box" {
        t.Fatal("reported name changed")
    }
    if requests[0].WGPublicKey == "" || requests[0].WGPublicKey == requests[1].WGPublicKey {
        t.Fatal("fresh enrollment reused public key")
    }
    restart := newTestAgent(t, srv.URL, dir, uptime, "second-enrollment", "")
    restart.cfg.Reenroll = true
    if err := restart.ensureEnrolled(context.Background()); err != nil { t.Fatal(err) }
    if len(requests) != 2 || restart.keyDir != second.keyDir { t.Fatal("restart changed identity") }
}

func TestReenrollmentRefusesCorruptLeaseState(t *testing.T) {
    dir:=t.TempDir()
    a:=newTestAgent(t,"http://127.0.0.1:1",dir,"","fresh-token","")
    a.cfg.Reenroll=true
    a.tunnels=&fakeTunnel{}
    if err:=a.saveState(State{DeviceID:"old-device",DeviceSecret:testSecret});err!=nil {t.Fatal(err)}
    if err:=os.WriteFile(filepath.Join(dir,leaseFile),[]byte("{broken"),0600);err!=nil {t.Fatal(err)}
    err:=a.ensureEnrolled(context.Background())
    if err==nil || !strings.Contains(err.Error(),"invalid old lease state") {t.Fatalf("got %v",err)}
    st,err:=a.loadState()
    if err!=nil || st.DeviceID!="old-device" {t.Fatal("old identity overwritten")}
}

func setSafeWorkloadInspection(a *Agent) {
	a.setLease(&runtime.Handle{ContainerID: "privacy-fixture"})
	a.dockerRun = func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"LogConfig":{"Type":"none"},"Ulimits":[{"Name":"core","Soft":0,"Hard":0}]}`), nil
	}
}

func TestInstanceCheckRejectsUnsafeWorkloadBeforeProbe(t *testing.T) {
	for _, body := range []string{
		`{"LogConfig":{"Type":"json-file"},"Ulimits":[{"Name":"core","Soft":0,"Hard":0}]}`,
		`{"LogConfig":{"Type":"none"}}`,
	} {
		dir := t.TempDir()
		a := newTestAgent(t, "http://127.0.0.1:9", dir, filepath.Join(dir, "uptime"), "", "")
		setSafeWorkloadInspection(a)
		a.dockerRun = func(_ context.Context, _ ...string) ([]byte, error) { return []byte(body), nil }
		probes := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { probes++ }))
		a.pathb.Endpoint = srv.URL
		got := a.runJob(context.Background(), api.Job{Kind: api.KindInstanceCheck})
		srv.Close()
		if got.Passed || probes != 0 || got.Reason != "workload privacy configuration unsafe" {
			t.Fatalf("unsafe workload probed: %+v probes=%d", got, probes)
		}
	}
}

func TestMetalInstanceCheckDoesNotInspectPIDWithDocker(t *testing.T) {
 if !runtime.MetalImageEnabled() {t.Skip("native Metal only")}
	dir := t.TempDir()
	a := newTestAgent(t, "http://127.0.0.1:9", dir, filepath.Join(dir, "uptime"), "", "")
	a.setLease(&runtime.Handle{ContainerID: "42", Backend: runtime.BackendLlamaCppCPU})
	a.dockerRun = func(_ context.Context, _ ...string) ([]byte, error) {
		t.Fatal("native Metal PID passed to Docker")
		return nil, nil
	}
	a.pathb.Endpoint = "http://127.0.0.1:1/v1/chat/completions"
	a.pathb.HTTP = &http.Client{Timeout: 150 * time.Millisecond}
	got := a.runJob(context.Background(), api.Job{Kind: api.KindInstanceCheck})
	if strings.Contains(got.Reason, "privacy") {
		t.Fatalf("native check blocked by Docker inspection: %+v", got)
	}
}

func TestCPUContainerInstanceCheckEnforcesDockerPrivacy(t *testing.T) {
 if runtime.MetalImageEnabled() {t.Skip("native Metal uses a separate process path")}
 dir:=t.TempDir()
 a:=newTestAgent(t,"http://127.0.0.1:9",dir,filepath.Join(dir,"uptime"),"","")
 a.setLease(&runtime.Handle{ContainerID:"cpu-container",Backend:runtime.BackendLlamaCppCPU})
 inspected:=false
 a.dockerRun=func(_ context.Context,args ...string)([]byte,error){inspected=true;return []byte(`{"LogConfig":{"Type":"json-file"},"Ulimits":[]}`),nil}
 got:=a.runJob(context.Background(),api.Job{Kind:api.KindInstanceCheck})
 if !inspected||got.Passed||got.Reason!="workload privacy configuration unsafe" {t.Fatalf("CPU container bypassed privacy: %+v inspected=%v",got,inspected)}
}
