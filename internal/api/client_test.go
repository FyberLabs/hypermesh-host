package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testTenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	testDevice = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	testJob    = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	testSecret = "hm_dev_fixture_secret"
)

func requireBearer(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get(HeaderTenantID) != testTenant {
		t.Errorf("tenant %q", r.Header.Get(HeaderTenantID))
	}
	if r.Header.Get(HeaderAuthorization) != "Bearer "+testSecret {
		t.Errorf("authorization %q", r.Header.Get(HeaderAuthorization))
	}
}

func TestEnrollHeartbeatJob204Result(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc(PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(HeaderAuthorization) != "" {
			t.Errorf("enroll must not send Bearer")
		}
		if r.Header.Get(HeaderTenantID) != testTenant {
			t.Errorf("enroll tenant")
		}
		var body EnrollRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("enroll decode: %v", err)
		}
		if body.EnrollToken != "one-time" || body.ImageHash != "sha256:abc" {
			t.Errorf("enroll body %+v", body)
		}
		writeJSON(w, http.StatusOK, EnrollResponse{
			DeviceID:         testDevice,
			CredentialPrefix: "deadbeef",
			DeviceSecret:     testSecret,
		})
	})

	mux.HandleFunc(PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		var body HeartbeatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("heartbeat decode: %v", err)
		}
		if body.DeviceID != testDevice || body.UptimeS != 12 {
			t.Errorf("heartbeat body %+v", body)
		}
		writeJSON(w, http.StatusOK, HeartbeatResponse{
			DeviceID:   testDevice,
			PathBState: PathBNeverRun,
			AcceptedAt: time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC),
		})
	})

	mux.HandleFunc(PathJobs, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.URL.Query().Get("device_id") != testDevice {
			t.Errorf("jobs query %q", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/"+testJob+"/result", func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.URL.Query().Get("device_id") != testDevice {
			t.Errorf("result query %q", r.URL.RawQuery)
		}
		var body JobResultRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("result decode: %v", err)
		}
		if !body.Passed || body.ImageHash != "sha256:abc" {
			t.Errorf("result %+v", body)
		}
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, testTenant)
	ctx := context.Background()

	enrolled, err := c.Enroll(ctx, EnrollRequest{EnrollToken: "one-time", ImageHash: "sha256:abc"})
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if enrolled.DeviceID != testDevice || enrolled.DeviceSecret != testSecret {
		t.Fatalf("enroll resp device=%s secret_set=%v", enrolled.DeviceID, enrolled.DeviceSecret != "")
	}
	c.DeviceSecret = enrolled.DeviceSecret

	thermal := 41.5
	hb, err := c.Heartbeat(ctx, HeartbeatRequest{
		DeviceID:  testDevice,
		ImageHash: "sha256:abc",
		UptimeS:   12,
		ThermalC:  &thermal,
	})
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if hb.PathBState != PathBNeverRun {
		t.Fatalf("heartbeat resp %+v", hb)
	}

	job, err := c.PullJob(ctx, testDevice)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if job != nil {
		t.Fatalf("expected 204 → nil job, got %+v", job)
	}

	if err := c.PostResult(ctx, testJob, testDevice, JobResultRequest{Passed: true, ImageHash: "sha256:abc"}); err != nil {
		t.Fatalf("result: %v", err)
	}
}

func TestBearerRequiredAndAuthErrors(t *testing.T) {
	c := NewClient("http://127.0.0.1:8010", testTenant)
	_, err := c.Heartbeat(context.Background(), HeartbeatRequest{DeviceID: testDevice, UptimeS: 0})
	if err != ErrMissingSecret {
		t.Fatalf("missing secret: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc(PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(HeaderAuthorization) != "Bearer "+testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc(PathJobs, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c = NewClient(srv.URL, testTenant)
	c.DeviceSecret = "wrong"
	_, err = c.Heartbeat(context.Background(), HeartbeatRequest{DeviceID: testDevice, UptimeS: 0})
	if err != ErrUnauthorized {
		t.Fatalf("401: %v", err)
	}
	c.DeviceSecret = testSecret
	_, err = c.Heartbeat(context.Background(), HeartbeatRequest{DeviceID: testDevice, UptimeS: 1})
	if err != ErrSecretMismatch {
		t.Fatalf("403: %v", err)
	}
	_, err = c.PullJob(context.Background(), testDevice)
	if err != ErrUnauthorized {
		t.Fatalf("jobs 401: %v", err)
	}
}

func TestEnrollErrors(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrInvalidEnrollToken},
		{http.StatusConflict, ErrEnrollTokenUsed},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		c := NewClient(srv.URL, testTenant)
		_, err := c.Enroll(context.Background(), EnrollRequest{EnrollToken: "x"})
		srv.Close()
		if err != tc.want {
			t.Fatalf("status %d: got %v want %v", tc.status, err, tc.want)
		}
	}
}

func TestUnknownDevice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, testTenant)
	c.DeviceSecret = testSecret
	_, err := c.Heartbeat(context.Background(), HeartbeatRequest{DeviceID: testDevice, UptimeS: 1})
	if err != ErrUnknownDevice {
		t.Fatalf("heartbeat: %v", err)
	}
	_, err = c.PullJob(context.Background(), testDevice)
	if err != ErrUnknownDevice {
		t.Fatalf("pull: %v", err)
	}
}

func TestMissingTenant(t *testing.T) {
	c := NewClient("http://127.0.0.1:8010", "")
	_, err := c.Enroll(context.Background(), EnrollRequest{EnrollToken: "x"})
	if err != ErrMissingTenant {
		t.Fatalf("got %v", err)
	}
}

func TestPullJobKinds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, Job{
			JobID:       testJob,
			Kind:        KindLeaseStart,
			DeviceID:    testDevice,
			LeaseID:     "lease-1",
			CatalogID:   "llama-3.1-8b-q4",
			ImageDigest: "",
			Runtime:     "nvidia",
			SuiteID:     "thin-v1",
		})
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, testTenant)
	c.DeviceSecret = testSecret
	job, err := c.PullJob(context.Background(), testDevice)
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || job.JobID != testJob || job.Kind != KindLeaseStart || job.Runtime != "nvidia" {
		t.Fatalf("job %+v", job)
	}
	if job.Backend != "" {
		t.Fatalf("omitted backend must stay empty: %+v", job)
	}
}

func TestJobBackendJSON(t *testing.T) {
	var j Job
	if err := json.Unmarshal([]byte(`{"job_id":"j","kind":"lease_start","device_id":"d","runtime":"nvidia","backend":"cuda-jetson"}`), &j); err != nil {
		t.Fatal(err)
	}
	if j.Backend != "cuda-jetson" || j.Runtime != "nvidia" {
		t.Fatalf("%+v", j)
	}
	raw, err := json.Marshal(Job{JobID: "j", Kind: KindLeaseStart, Runtime: "nvidia"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "backend") {
		t.Fatalf("omitempty leaked backend: %s", raw)
	}
	if !strings.Contains(string(raw), `"runtime":"nvidia"`) {
		t.Fatalf("runtime must stay nvidia: %s", raw)
	}
}

func TestSellPauseJobAndResultJSON(t *testing.T) {
	var j Job
	if err := json.Unmarshal([]byte(`{"job_id":"j","kind":"sell_pause","device_id":"d","until":"2026-09-08T00:00:00Z"}`), &j); err != nil {
		t.Fatal(err)
	}
	if j.Kind != KindSellPause || j.DeviceID != "d" || j.Until != "2026-09-08T00:00:00Z" {
		t.Fatalf("%+v", j)
	}

	leaseRaw, err := json.Marshal(JobResultRequest{Passed: true, ImageHash: "sha256:abc"})
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"sell_state", "until", "device_id"} {
		if strings.Contains(string(leaseRaw), leak) {
			t.Fatalf("lease/path_b result leaked %s: %s", leak, leaseRaw)
		}
	}

	pauseRaw, err := json.Marshal(JobResultRequest{
		Passed:    true,
		SellState: SellPaused,
		Until:     "2026-09-08T00:00:00Z",
		DeviceID:  testDevice,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pauseRaw), `"sell_state":"paused"`) {
		t.Fatalf("sell_pause result must include sell_state: %s", pauseRaw)
	}
	if strings.Contains(string(pauseRaw), `"n/a"`) {
		t.Fatalf("sell_pause result must not use n/a: %s", pauseRaw)
	}

	invalid := JobResultRequest{Passed: true, SellState: SellNA}
	invRaw, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(invRaw), `"passed":true`) || !strings.Contains(string(invRaw), `"n/a"`) {
		t.Fatalf("fixture %+v raw %s", invalid, invRaw)
	}
}

func TestHeartbeatSellStateJSON(t *testing.T) {
	raw, err := json.Marshal(HeartbeatRequest{DeviceID: testDevice, UptimeS: 1, SellState: SellSelling})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"sell_state":"selling"`) {
		t.Fatalf("heartbeat must send sell_state: %s", raw)
	}
	if strings.Contains(string(raw), "path_b") {
		t.Fatalf("heartbeat request must not copy path_b into sell: %s", raw)
	}
	omitted, err := json.Marshal(HeartbeatRequest{DeviceID: testDevice, UptimeS: 1})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(omitted), "sell_state") {
		t.Fatalf("omitempty leaked sell_state: %s", omitted)
	}
}

func TestTunnelGetAck(t *testing.T) {
	var acked bool
	mux := http.NewServeMux()
	mux.HandleFunc(PathTunnelAck, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.URL.Query().Get("device_id") != testDevice {
			t.Errorf("ack query")
		}
		var body TunnelAckRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Applied {
			t.Errorf("applied")
		}
		acked = true
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.URL.Query().Get("device_id") != testDevice {
			t.Errorf("tunnel query")
		}
		writeJSON(w, http.StatusOK, TunnelSession{
			TunnelID:        "tun-1",
			LeaseID:         "lease-1",
			Status:          TunnelAllocated,
			Endpoint:        "198.51.100.10:51820",
			ServerPublicKey: "serverpub",
			PeerPublicKey:   "peerpub",
			AllowedIPs:      "10.8.0.0/24",
			ClientAddress:   "10.8.0.2/32",
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, testTenant)
	c.DeviceSecret = testSecret
	sess, err := c.GetTunnel(context.Background(), testDevice)
	if err != nil {
		t.Fatal(err)
	}
	if sess == nil || sess.Status != TunnelAllocated || sess.PeerPublicKey == "" {
		t.Fatalf("session %+v", sess)
	}
	if err := c.AckTunnel(context.Background(), testDevice, TunnelAckRequest{Applied: true}); err != nil {
		t.Fatal(err)
	}
	if !acked {
		t.Fatal("ack not posted")
	}
}

func TestProposePeer(t *testing.T) {
	const hostPub = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	const notSent = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="
	var rawBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc(PathTunnelPeer, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		if r.URL.Query().Get("device_id") != testDevice {
			t.Errorf("peer query")
		}
		var err error
		rawBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"allocated","peer_public_key":"` + hostPub + `"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, testTenant)
	c.DeviceSecret = testSecret
	if err := c.ProposePeer(context.Background(), testDevice, TunnelPeerRequest{PeerPublicKey: hostPub}); err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(rawBody, &obj); err != nil {
		t.Fatal(err)
	}
	if len(obj) != 1 {
		t.Fatalf("POST body must be public key only, got %s", rawBody)
	}
	if obj["peer_public_key"] != hostPub {
		t.Fatalf("posted %#v", obj)
	}
	if _, ok := obj["peer_private_key"]; ok {
		t.Fatal("private key field must not be posted")
	}
	if strings.Contains(string(rawBody), notSent) || strings.Contains(string(rawBody), "private") {
		t.Fatalf("body leaked key material: %s", rawBody)
	}
}

func TestGetTunnelWithoutPeerPrivateKey(t *testing.T) {
	const leaked = "PLANEPRIVATEKEYMUSTNOTSTICK"
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		n++
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			_, _ = w.Write([]byte(`{"tunnel_id":"tun-1","lease_id":"lease-1","status":"allocated","endpoint":"wg.test.hyperme.sh:51820","server_public_key":"c2VydmVycHVi","allowed_ips":"10.77.0.0/24","client_address":"10.77.0.2/32"}`))
			return
		}
		_, _ = w.Write([]byte(`{"tunnel_id":"tun-1","lease_id":"lease-1","status":"allocated","endpoint":"wg.test.hyperme.sh:51820","server_public_key":"c2VydmVycHVi","peer_public_key":"aG9zdHB1Yg==","allowed_ips":"10.77.0.0/24","client_address":"10.77.0.2/32","peer_private_key":"` + leaked + `"}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, testTenant)
	c.DeviceSecret = testSecret

	sess, err := c.GetTunnel(context.Background(), testDevice)
	if err != nil {
		t.Fatal(err)
	}
	if sess == nil || sess.Status != TunnelAllocated || sess.PeerPublicKey != "" {
		t.Fatalf("session without peer key: %+v", sess)
	}
	if sess.ClientAddress != "10.77.0.2/32" || sess.Endpoint != "wg.test.hyperme.sh:51820" {
		t.Fatalf("session %+v", sess)
	}

	sess, err = c.GetTunnel(context.Background(), testDevice)
	if err != nil {
		t.Fatal(err)
	}
	if sess.PeerPublicKey != "aG9zdHB1Yg==" {
		t.Fatalf("peer public %+v", sess)
	}
	out, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), leaked) || strings.Contains(string(out), "peer_private_key") {
		t.Fatalf("parsed session kept a private key: %s", out)
	}
}

func TestProposePeerNoTunnelIsDistinct(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.URL.Path != PathTunnelPeer {
			t.Errorf("path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Tunnel not found"}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, testTenant)
	c.DeviceSecret = testSecret
	err := c.ProposePeer(context.Background(), testDevice, TunnelPeerRequest{PeerPublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="})
	if !errors.Is(err, ErrNoTunnel) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "Tunnel not found") {
		t.Fatalf("404 body leaked into error: %v", err)
	}
}

func TestGetTunnelPeerNotRegistered(t *testing.T) {
	const code = PeerPublicKeyNotRegistered
	bodies := []string{
		`{"detail":{"code":"` + code + `"}}`,
		`{"detail":"` + code + `"}`,
		`{"code":"` + code + `"}`,
		`{"error":{"code":"` + code + `"}}`,
	}
	for _, raw := range bodies {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requireBearer(t, r)
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(raw))
		}))
		c := NewClient(srv.URL, testTenant)
		c.DeviceSecret = testSecret
		_, err := c.GetTunnel(context.Background(), testDevice)
		srv.Close()
		if !errors.Is(err, ErrPeerPublicKeyNotRegistered) {
			t.Fatalf("body %s: %v", raw, err)
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"detail":"Tunnel is revoked"}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, testTenant)
	c.DeviceSecret = testSecret
	_, err := c.GetTunnel(context.Background(), testDevice)
	if errors.Is(err, ErrPeerPublicKeyNotRegistered) {
		t.Fatal("revoked 409 must not look like a missing peer key")
	}
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTunnel204AndRevoked(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, TunnelSession{
			TunnelID: "tun-1",
			LeaseID:  "lease-1",
			Status:   TunnelRevoked,
		})
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, testTenant)
	c.DeviceSecret = testSecret
	sess, err := c.GetTunnel(context.Background(), testDevice)
	if err != nil || sess != nil {
		t.Fatalf("204: %v %+v", err, sess)
	}
	sess, err = c.GetTunnel(context.Background(), testDevice)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Status != TunnelRevoked {
		t.Fatalf("revoked: %+v", sess)
	}
}

func TestIssueEnrollToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/hypermesh/devices/"+testDevice+"/enroll-token" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get(HeaderAuthorization) != "" {
			t.Errorf("platform helper must not send device Bearer")
		}
		if r.Header.Get(HeaderTenantID) != testTenant {
			t.Errorf("tenant")
		}
		writeJSON(w, http.StatusOK, EnrollTokenResponse{EnrollToken: "one-time-new"})
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, testTenant)
	out, err := c.IssueEnrollToken(context.Background(), testDevice)
	if err != nil {
		t.Fatal(err)
	}
	if out.EnrollToken != "one-time-new" {
		t.Fatalf("%+v", out)
	}
}

func TestClientDoesNotEchoSecretsOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"detail":"bad token hm_dev_should_not_leak"}`)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, testTenant)
	_, err := c.Enroll(context.Background(), EnrollRequest{EnrollToken: "hm_dev_should_not_leak"})
	if err != ErrInvalidEnrollToken {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), "hm_dev_should_not_leak") {
		t.Fatalf("error leaked secret: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
