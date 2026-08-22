package api

import (
	"context"
	"encoding/json"
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
			SuiteID:     "stub-suite-v1",
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
			PeerPrivateKey:  "PEERPRIVATE",
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
	if sess == nil || sess.Status != TunnelAllocated || sess.PeerPrivateKey == "" {
		t.Fatalf("session %+v", sess)
	}
	if err := c.AckTunnel(context.Background(), testDevice, TunnelAckRequest{Applied: true}); err != nil {
		t.Fatal(err)
	}
	if !acked {
		t.Fatal("ack not posted")
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
	if sess.Status != TunnelRevoked || sess.PeerPrivateKey != "" {
		t.Fatalf("revoked must omit private key: %+v", sess)
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
