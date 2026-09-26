package agent

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/metrics"
)

func TestMetricsFallbackRebindAndNames(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("10.0 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.cfg.MetricsAddr = ""
	a.cfg.GGUFPath = filepath.Join(dir, "model.gguf")
	a.statFree = func(string) (int64, error) { return 99, nil }
	a.metricsEvery = 15 * time.Millisecond

	var mu sync.Mutex
	var host string
	a.metricsWG = func() string {
		mu.Lock()
		defer mu.Unlock()
		return host
	}
	var bound []string
	a.metricsServe = func(ctx context.Context, addr, tunnel string) (string, error) {
		if err := metrics.AuthorizeBind(addr, tunnel); err != nil {
			return "", err
		}
		ip, _, err := net.SplitHostPort(addr)
		if err != nil {
			return "", err
		}
		parsed := net.ParseIP(ip)
		if parsed == nil || parsed.IsUnspecified() || (!parsed.IsLoopback() && !parsed.IsPrivate()) {
			t.Errorf("bound %s", addr)
			return "", err
		}
		mu.Lock()
		bound = append(bound, addr)
		mu.Unlock()
		return addr, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.StartMetrics(ctx)

	waitFor := func(want string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			last := ""
			if n := len(bound); n > 0 {
				last = bound[n-1]
			}
			mu.Unlock()
			if last == want {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		mu.Lock()
		got := append([]string(nil), bound...)
		mu.Unlock()
		t.Fatalf("bound %v want %s", got, want)
	}

	waitFor("127.0.0.1:9464")
	mu.Lock()
	host = "10.77.0.3"
	mu.Unlock()
	a.pokeMetrics()
	waitFor("10.77.0.3:9464")
	mu.Lock()
	host = "10.77.0.4"
	mu.Unlock()
	a.pokeMetrics()
	waitFor("10.77.0.4:9464")
	mu.Lock()
	host = "8.8.8.8"
	mu.Unlock()
	a.pokeMetrics()
	waitFor("127.0.0.1:9464")

	mu.Lock()
	defer mu.Unlock()
	for _, addr := range bound {
		ip, _, err := net.SplitHostPort(addr)
		if err != nil {
			t.Fatal(err)
		}
		parsed := net.ParseIP(ip)
		if parsed == nil || parsed.IsUnspecified() || (!parsed.IsLoopback() && !parsed.IsPrivate()) {
			t.Fatalf("public or unspecified bind %s", addr)
		}
	}

	a.recordPull(2048, 250*time.Millisecond)
	text := string(a.metricsText())
	for _, name := range []string{
		"hypermesh_heartbeat_success_total",
		"hypermesh_heartbeat_failure_total{class=\"http_5xx\"}",
		"hypermesh_disk_free_bytes 99",
		"hypermesh_pull_bytes_total 2048",
		"hypermesh_pull_duration_seconds_total 0.25",
	} {
		if !strings.Contains(text, name) {
			t.Fatalf("missing %s\n%s", name, text)
		}
	}
	if strings.Contains(text, "hm_dev_") || strings.Contains(text, "private_key") {
		t.Fatalf("metrics leaked\n%s", text)
	}
}
