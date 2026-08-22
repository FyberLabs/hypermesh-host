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
)

func TestEnrollHeartbeatJob204Result(t *testing.T) {
	var gotTenant []string
	mux := http.NewServeMux()

	mux.HandleFunc(PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		gotTenant = append(gotTenant, r.Header.Get(HeaderTenantID))
		if r.Method != http.MethodPost {
			t.Errorf("enroll method %s", r.Method)
		}
		var body EnrollRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("enroll decode: %v", err)
		}
		if body.EnrollToken != "one-time" {
			t.Errorf("enroll token = %q", body.EnrollToken)
		}
		if body.ImageHash != "sha256:abc" {
			t.Errorf("enroll image_hash = %q", body.ImageHash)
		}
		writeJSON(w, http.StatusOK, EnrollResponse{
			DeviceID:         testDevice,
			CredentialPrefix: "deadbeef",
		})
	})

	mux.HandleFunc(PathHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		gotTenant = append(gotTenant, r.Header.Get(HeaderTenantID))
		var body HeartbeatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("heartbeat decode: %v", err)
		}
		if body.DeviceID != testDevice || body.UptimeS != 12 {
			t.Errorf("heartbeat body %+v", body)
		}
		if body.ThermalC == nil || *body.ThermalC != 41.5 {
			t.Errorf("thermal %+v", body.ThermalC)
		}
		writeJSON(w, http.StatusOK, HeartbeatResponse{
			DeviceID:   testDevice,
			PathBState: PathBNeverRun,
			AcceptedAt: time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC),
		})
	})

	mux.HandleFunc(PathJobs, func(w http.ResponseWriter, r *http.Request) {
		gotTenant = append(gotTenant, r.Header.Get(HeaderTenantID))
		if r.URL.Query().Get("device_id") != testDevice {
			t.Errorf("jobs query %q", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/api/v1/hypermesh/agent/jobs/"+testJob+"/result", func(w http.ResponseWriter, r *http.Request) {
		gotTenant = append(gotTenant, r.Header.Get(HeaderTenantID))
		var body JobResultRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("result decode: %v", err)
		}
		if !body.Passed || body.ImageHash != "sha256:abc" {
			t.Errorf("result %+v", body)
		}
		now := time.Date(2026, 8, 22, 0, 1, 0, 0, time.UTC)
		writeJSON(w, http.StatusOK, CertJob{
			ID:          testJob,
			TenantID:    testTenant,
			DeviceID:    testDevice,
			SuiteID:     "stub-suite-v1",
			Kind:        KindPathB,
			Status:      StatusPassed,
			CreatedAt:   time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC),
			CompletedAt: &now,
			ImageHash:   body.ImageHash,
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, testTenant)
	ctx := context.Background()

	enrolled, err := c.Enroll(ctx, EnrollRequest{EnrollToken: "one-time", ImageHash: "sha256:abc"})
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if enrolled.DeviceID != testDevice || enrolled.CredentialPrefix != "deadbeef" {
		t.Fatalf("enroll resp %+v", enrolled)
	}

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
	if hb.PathBState != PathBNeverRun || hb.DeviceID != testDevice {
		t.Fatalf("heartbeat resp %+v", hb)
	}

	job, err := c.PullJob(ctx, testDevice)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if job != nil {
		t.Fatalf("expected 204 → nil job, got %+v", job)
	}

	done, err := c.PostResult(ctx, testJob, JobResultRequest{Passed: true, ImageHash: "sha256:abc"})
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if done.Status != StatusPassed || done.SuiteID != "stub-suite-v1" {
		t.Fatalf("result resp %+v", done)
	}

	if len(gotTenant) != 4 {
		t.Fatalf("tenant header count %d", len(gotTenant))
	}
	for i, v := range gotTenant {
		if v != testTenant {
			t.Fatalf("tenant[%d]=%q", i, v)
		}
	}
}

func TestEnrollErrors(t *testing.T) {
	t.Parallel()
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

func TestDeviceAuthHook(t *testing.T) {
	var headerName, headerVal, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headerName = "X-Device-Secret"
		headerVal = r.Header.Get("X-Device-Secret")
		auth = r.Header.Get("Authorization")
		writeJSON(w, http.StatusOK, HeartbeatResponse{
			DeviceID:   testDevice,
			PathBState: PathBGreen,
			AcceptedAt: time.Now().UTC(),
		})
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, testTenant)
	c.DeviceAuth = DeviceAuth{Header: "X-Device-Secret", Value: "not-a-protocol"}
	_, err := c.Heartbeat(context.Background(), HeartbeatRequest{DeviceID: testDevice, UptimeS: 0})
	if err != nil {
		t.Fatal(err)
	}
	if headerVal != "not-a-protocol" {
		t.Fatalf("hook header %q", headerVal)
	}
	if auth != "" {
		t.Fatalf("must not invent Authorization, got %q", auth)
	}
	_ = headerName
}

func TestPullJob200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, CertJob{
			ID:        testJob,
			TenantID:  testTenant,
			DeviceID:  testDevice,
			SuiteID:   "stub-suite-v1",
			Kind:      KindPathB,
			Status:    StatusPending,
			CreatedAt: time.Now().UTC(),
		})
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, testTenant)
	job, err := c.PullJob(context.Background(), testDevice)
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || job.ID != testJob || job.Kind != KindPathB {
		t.Fatalf("job %+v", job)
	}
}

func TestClientDoesNotEchoEnrollTokenOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"detail":"bad token hm_should_not_leak"}`)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, testTenant)
	_, err := c.Enroll(context.Background(), EnrollRequest{EnrollToken: "hm_should_not_leak"})
	if err != ErrInvalidEnrollToken {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), "hm_should_not_leak") {
		t.Fatalf("error leaked token: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
