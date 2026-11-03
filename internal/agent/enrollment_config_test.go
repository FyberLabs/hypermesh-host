package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/telem"
)

func TestMissingEnrollmentKeysStopBeforeRequestsOrLocalSetup(t *testing.T) {
	for _, tc := range []struct {
		name, tenant, token string
		missing             []string
	}{
		{"both-empty", "", "", []string{"HYPERMESH_TENANT_ID", "HYPERMESH_ENROLL_TOKEN"}},
		{"both-whitespace", " \t", "\n ", []string{"HYPERMESH_TENANT_ID", "HYPERMESH_ENROLL_TOKEN"}},
		{"tenant-only", " ", "private-token-fixture", []string{"HYPERMESH_TENANT_ID"}},
		{"token-only", testTenant, " \t", []string{"HYPERMESH_ENROLL_TOKEN"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests, telemetry atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			dir := filepath.Join(t.TempDir(), "uncreated-state")
			cfg := Config{ControlPlaneURL: server.URL, TenantID: tc.tenant, EnrollToken: tc.token,
				StateDir: dir, HeartbeatEvery: time.Second, JobEvery: time.Second}
			a, err := New(cfg, discardLog())
			if err == nil {
				a.telem = telem.Source{LookPath: func(string) (string, error) {
					telemetry.Add(1)
					return "", os.ErrNotExist
				}}
				err = a.Run(context.Background())
			}
			if err == nil {
				t.Fatal("missing enrollment configuration was accepted")
			}
			for _, key := range tc.missing {
				if !strings.Contains(err.Error(), key) {
					t.Fatalf("missing key %s in %v", key, err)
				}
			}
			if strings.Contains(err.Error(), "private-token-fixture") {
				t.Fatal("configuration error leaked token")
			}
			if requests.Load() != 0 || telemetry.Load() != 0 {
				t.Fatalf("startup attempted requests=%d telemetry=%d", requests.Load(), telemetry.Load())
			}
			if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
				t.Fatalf("startup changed state directory: %v", statErr)
			}
		})
	}
}

func TestStoredIdentityDoesNotRequireEnrollmentToken(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved-secret", true: "recovery-secret"}[recovery], func(t *testing.T) {
			dir := t.TempDir()
			st := State{DeviceID: testDevice, DeviceSecret: testSecret}
			cfg := Config{TenantID: testTenant, StateDir: dir, EnrollToken: " \t", Reenroll: true}
			if recovery {
				st.DeviceSecret = ""
				cfg.DeviceSecret = testSecret
			}
			data, err := json.Marshal(st)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, stateFile)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			a := &Agent{cfg: cfg, client: api.NewClient("http://unused.invalid", testTenant), log: discardLog()}
			if err := a.ensureEnrolled(context.Background()); err != nil {
				t.Fatal(err)
			}
			if a.state.DeviceID != testDevice || a.client.DeviceSecret != testSecret {
				t.Fatal("saved identity was not reused")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(data) {
				t.Fatalf("saved identity was rewritten: %v", err)
			}
			cfg.TenantID = ""
			_, err = New(cfg, discardLog())
			if err == nil || !strings.Contains(err.Error(), "HYPERMESH_TENANT_ID") || strings.Contains(err.Error(), "HYPERMESH_ENROLL_TOKEN") {
				t.Fatalf("saved identity should only need tenant configuration: %v", err)
			}
		})
	}
}
