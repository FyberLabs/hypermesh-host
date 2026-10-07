package agent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
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

func TestMetricsListenFailureFallsBackWithoutSpin(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("10.0 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.cfg.MetricsAddr = ""
	a.metricsEvery = 15 * time.Millisecond
	a.metricsFailEvery = 400 * time.Millisecond
	var mu sync.Mutex
	failListen := true
	a.metricsWG = func() string { return "10.77.0.3" }
	var calls []string
	a.metricsServe = func(ctx context.Context, addr, tunnel string) (string, error) {
		mu.Lock()
		calls = append(calls, addr)
		fail := failListen
		mu.Unlock()
		if fail && addr != metrics.DefaultAddr {
			return "", errors.New("bind: cannot assign requested address")
		}
		return addr, nil
	}
	var buf safeLog
	a.log = slog.New(slog.NewTextHandler(&buf, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.StartMetrics(ctx)

	waitCalls := func(want func([]string) bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			got := append([]string(nil), calls...)
			mu.Unlock()
			if want(got) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("calls %v", calls)
	}
	waitCalls(func(got []string) bool {
		return len(got) >= 2 && got[len(got)-1] == metrics.DefaultAddr
	})
	time.Sleep(80 * time.Millisecond)
	mu.Lock()
	got := append([]string(nil), calls...)
	mu.Unlock()
	var wgN, loopN int
	for _, c := range got {
		switch c {
		case "10.77.0.3:9464":
			wgN++
		case metrics.DefaultAddr:
			loopN++
		default:
			t.Fatalf("unexpected bind %s in %v", c, got)
		}
	}
	if wgN != 1 || loopN != 1 {
		t.Fatalf("wg %d loop %d calls %v", wgN, loopN, got)
	}
	log := buf.String()
	if strings.Count(log, "metrics endpoint not started") != 1 || !strings.Contains(log, "metrics bound loopback") {
		t.Fatalf("log %s", log)
	}

	mu.Lock()
	failListen = false
	mu.Unlock()
	a.pokeMetrics()
	waitCalls(func(got []string) bool {
		return len(got) > 0 && got[len(got)-1] == "10.77.0.3:9464"
	})
}

func TestMetricsLoopbackFailureBacksOff(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("10.0 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.cfg.MetricsAddr = ""
	a.metricsWG = func() string { return "" }
	a.metricsEvery = 10 * time.Millisecond
	a.metricsFailEvery = 80 * time.Millisecond
	attempts := make(chan time.Time, 3)
	a.metricsServe = func(ctx context.Context, _ string, _ string) (string, error) {
		select {
		case attempts <- time.Now():
		case <-ctx.Done():
		}
		return "", errors.New("bind: cannot assign requested address")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.StartMetrics(ctx)

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	var previous time.Time
	for i := 0; i < 3; i++ {
		select {
		case at := <-attempts:
			if i > 0 {
				minimum := a.metricsFailEvery * time.Duration(1<<(i-1))
				if elapsed := at.Sub(previous); elapsed < minimum {
					t.Fatalf("retry %d after %s, want at least %s", i, elapsed, minimum)
				}
			}
			previous = at
		case <-deadline.C:
			t.Fatal("metrics retries did not arrive")
		}
	}
}

func TestBindIPReadByMetricsIsRaceFree(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("10.0 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	a.ifaceAddr = func() string { return "" }
	a.metricsEvery = 2 * time.Millisecond
	a.metricsServe = func(context.Context, string, string) (string, error) {
		return metrics.DefaultAddr, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.StartMetrics(ctx)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if i%2 == 0 {
				a.setBindIP("10.77.0.2/32")
			} else {
				a.setBindIP("")
			}
			a.pokeMetrics()
		}
	}()
	for i := 0; i < 200; i++ {
		_ = a.tunnelMetricsHost()
		_ = a.currentBindIP()
	}
	wg.Wait()
}

// safeLog is a slog writer the test can read while the metrics loop logs.
type safeLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeLog) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeLog) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
