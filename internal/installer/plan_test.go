package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"sync/atomic"

	"github.com/FyberLabs/hypermesh-host/internal/hardware"
)

func TestPlanAggregatesBlockers(t *testing.T) {
	for _, fail := range []bool{false, true} {
		var out, diagnostic bytes.Buffer
		resource := func(context.Context, string, string, int64) (Resources, error) {
			if fail { return Resources{}, fmt.Errorf("disk unavailable") }
			return Resources{SystemBytes: 64*GiB, AvailableSystemBytes: 48*GiB,
				UMA: true, UMAAllocationBytes: 40*GiB, DiskFreeBytes: 88*GiB}, nil
		}
		detect := func(context.Context) hardware.Profile {
			if fail { return hardware.Recommend(hardware.Facts{Arch: "unknown"}) }
			return hardware.Recommend(hardware.Facts{Arch: "arm64", Tegra: true, CUDA: true})
		}
		endpoint := func(context.Context, string) error {
			if fail { return fmt.Errorf("endpoint unavailable") }
			return nil
		}
		code := runPlan(context.Background(), []string{"--memory-mode", "uma", "--uma-gib", "40",
			"--model-dir", ".", "--aiuditor-check-url", "https://example.invalid/ready"},
			&out, &diagnostic, resource, detect, endpoint)
		var p Plan
		if err := json.Unmarshal(out.Bytes(), &p); err != nil { t.Fatal(err, diagnostic.String()) }
		if (code == 0) == fail || p.PreliminaryChecksPassed == fail || p.InstallAuthorized || len(p.Pending) == 0 {
			t.Fatalf("code=%d report=%+v", code, p)
		}
		if fail && len(p.Blockers) < 4 { t.Fatalf("lost blockers: %+v", p) }
	}
}

func TestPlanRejectsCredentialURLsBeforeProbing(t *testing.T) {
	for _, address := range []string{"", "http://example.com", "https://u:p@example.com", "https://example.com/?token=secret", "https://example.com/#secret"} {
		var out bytes.Buffer
		code := runPlan(context.Background(), []string{"--aiuditor-check-url", address},
			&out, &out, nil, nil, nil)
		if code != 2 { t.Fatalf("accepted %q", address) }
	}
}

func TestEndpointProbe(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet { t.Error("expected read-only GET") }
		w.Header().Set("Location", "/other")
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte("private diagnostic body"))
	}))
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if err := probeEndpoint(context.Background(), server.URL, client); err != nil { t.Fatal(err) }
	for _, value := range []int{302, 401, 503} {
		status.Store(int32(value))
		if err := probeEndpoint(context.Background(), server.URL, client); err == nil {
			t.Fatalf("accepted HTTP %d", value)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := probeEndpoint(ctx, server.URL, client); err == nil { t.Fatal("ignored cancellation") }
	if err := checkEndpoint(context.Background(), server.URL); err == nil { t.Fatal("accepted untrusted TLS") }
}
