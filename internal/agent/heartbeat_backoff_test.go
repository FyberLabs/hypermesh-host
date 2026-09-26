package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/tunnel"
)

func TestHeartbeatBackoffGrowthAndJitter(t *testing.T) {
	var prevLo, prevHi time.Duration
	for fails := 1; fails <= 12; fails++ {
		lo := heartbeatDelay(fails, false, 0, 0)
		mid := heartbeatDelay(fails, false, 0, 0.5)
		hi := heartbeatDelay(fails, false, 0, 1)
		if lo < heartbeatBackoffFloor || hi > heartbeatBackoffCap {
			t.Fatalf("fails %d lo %s hi %s outside [%s, %s]", fails, lo, hi, heartbeatBackoffFloor, heartbeatBackoffCap)
		}
		if lo > mid || mid > hi {
			t.Fatalf("fails %d not ordered lo %s mid %s hi %s", fails, lo, mid, hi)
		}
		if fails > 1 && (lo < prevLo || hi < prevHi) {
			t.Fatalf("fails %d shrank lo %s→%s hi %s→%s", fails, prevLo, lo, prevHi, hi)
		}
		prevLo, prevHi = lo, hi
		// Every unit in [0, 1] stays inside the ±20% band, then the clamp.
		shift := fails - 1
		if shift > 16 {
			shift = 16
		}
		base := heartbeatBackoffFloor << shift
		for i := 0; i <= 20; i++ {
			unit := float64(i) / 20
			got := heartbeatDelay(fails, false, 0, unit)
			raw := time.Duration(float64(base) * (1 + ((unit*2 - 1) * heartbeatJitterFrac)))
			want := raw
			if want < heartbeatBackoffFloor {
				want = heartbeatBackoffFloor
			}
			if want > heartbeatBackoffCap {
				want = heartbeatBackoffCap
			}
			if got != want {
				t.Fatalf("fails %d unit %v got %s want %s", fails, unit, got, want)
			}
			if got < heartbeatBackoffFloor || got > heartbeatBackoffCap {
				t.Fatalf("fails %d unit %v %s", fails, unit, got)
			}
		}
	}
	if heartbeatDelay(1, false, 0, 0.5) != time.Second {
		t.Fatalf("first base %s", heartbeatDelay(1, false, 0, 0.5))
	}
	if heartbeatDelay(2, false, 0, 0.5) != 2*time.Second {
		t.Fatalf("second base %s", heartbeatDelay(2, false, 0, 0.5))
	}
	if heartbeatDelay(6, false, 0, 0.5) != 32*time.Second {
		t.Fatalf("sixth base %s", heartbeatDelay(6, false, 0, 0.5))
	}
	if heartbeatDelay(7, false, 0, 0.5) != heartbeatBackoffCap {
		t.Fatalf("seventh base %s", heartbeatDelay(7, false, 0, 0.5))
	}
}

func TestHeartbeatRetryAfterHonoredAndClamped(t *testing.T) {
	if got := heartbeatDelay(4, true, 35*time.Second, 0); got != 35*time.Second {
		t.Fatalf("35s hint %s", got)
	}
	if got := heartbeatDelay(4, true, 35*time.Second, 1); got != 35*time.Second {
		t.Fatalf("hint must not jitter: %s", got)
	}
	if got := heartbeatDelay(1, true, 100*time.Millisecond, 0.5); got != heartbeatBackoffFloor {
		t.Fatalf("short hint %s", got)
	}
	if got := heartbeatDelay(1, true, 0, 0); got != heartbeatBackoffFloor {
		t.Fatalf("zero hint %s", got)
	}
	if got := heartbeatDelay(2, true, 10*time.Minute, 1); got != heartbeatBackoffCap {
		t.Fatalf("long hint %s", got)
	}
	// A 500 that happens to carry a hint is not a 503. The loop asks
	// heartbeatBackoff, which ignores it.
	a := &Agent{hbRand: func() float64 { return 0.5 }}
	got := a.heartbeatBackoff(3, &api.StatusError{
		StatusCode:    http.StatusInternalServerError,
		HasRetryAfter: true,
		RetryAfter:    35 * time.Second,
	})
	if got != 4*time.Second {
		t.Fatalf("500 used Retry-After: %s", got)
	}
	got = a.heartbeatBackoff(3, &api.StatusError{
		StatusCode:    http.StatusServiceUnavailable,
		HasRetryAfter: true,
		RetryAfter:    35 * time.Second,
	})
	if got != 35*time.Second {
		t.Fatalf("503 hint %s", got)
	}
}

func TestHeartbeatFailureDoesNotTightLoop(t *testing.T) {
	var mu sync.Mutex
	var waits []time.Duration
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("10.0 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, srv.URL, dir, uptime, "", "")
	a.state.DeviceID = testDevice
	a.client.DeviceSecret = testSecret
	a.hbRand = func() float64 { return 0 }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const n = 5
	a.hbWait = func(ctx context.Context, d time.Duration, interrupt bool) bool {
		if !interrupt {
			t.Errorf("success wait during a 503 streak: %s", d)
		}
		if d < heartbeatBackoffFloor {
			t.Errorf("wait %s is tighter than %s", d, heartbeatBackoffFloor)
		}
		mu.Lock()
		waits = append(waits, d)
		stop := len(waits) >= n
		mu.Unlock()
		if stop {
			cancel()
			return false
		}
		return ctx.Err() == nil
	}
	a.heartbeatLoop(ctx)
	mu.Lock()
	defer mu.Unlock()
	if len(waits) != n {
		t.Fatalf("waits %d", len(waits))
	}
	for i, d := range waits {
		if d != heartbeatBackoffFloor {
			t.Fatalf("wait %d = %s, Retry-After 0 must clamp to %s", i, d, heartbeatBackoffFloor)
		}
	}
}

func TestHeartbeatImmediateBeatOnRecovery(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{
			DeviceID:   testDevice,
			PathBState: api.PathBGreen,
			AcceptedAt: time.Now().UTC(),
		})
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("10.0 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, srv.URL, dir, uptime, "", "")
	a.state.DeviceID = testDevice
	a.client.DeviceSecret = testSecret
	a.cfg.HeartbeatEvery = 30 * time.Second
	a.hbRand = func() float64 { return 0.5 }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var waits []time.Duration
	a.hbWait = func(ctx context.Context, d time.Duration, interrupt bool) bool {
		waits = append(waits, d)
		if !interrupt {
			cancel()
			return false
		}
		return true
	}
	a.heartbeatLoop(ctx)
	if calls != 3 {
		t.Fatalf("calls %d", calls)
	}
	if len(waits) != 3 {
		t.Fatalf("waits %v", waits)
	}
	if waits[0] != time.Second || waits[1] != 2*time.Second {
		t.Fatalf("backoff %v", waits[:2])
	}
	if waits[2] != a.cfg.HeartbeatEvery {
		t.Fatalf("cadence after recovery %s", waits[2])
	}
}

func TestHeartbeatImmediateBeatOnReconnect(t *testing.T) {
	var mu sync.Mutex
	var calls int
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Retry-After", "600")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{
			DeviceID:   testDevice,
			PathBState: api.PathBGreen,
			AcceptedAt: time.Now().UTC(),
		})
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("10.0 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, srv.URL, dir, uptime, "", "")
	a.state.DeviceID = testDevice
	a.client.DeviceSecret = testSecret
	a.cfg.HeartbeatEvery = 30 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	var waits []time.Duration
	a.hbWait = func(waitCtx context.Context, d time.Duration, interrupt bool) bool {
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
		if !interrupt {
			cancel()
			return false
		}
		close(entered)
		select {
		case <-waitCtx.Done():
			return false
		case <-a.hbWake:
			return true
		case <-release:
			t.Error("backoff was not interrupted")
			return false
		}
	}
	done := make(chan struct{})
	go func() {
		a.heartbeatLoop(ctx)
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat did not reach the failure wait")
	}
	a.observeTunnel(tunnel.StateDown)
	a.observeBind("")
	a.observeTunnel(tunnel.StateUp)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("reconnect did not beat immediately")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls < 2 {
		t.Fatalf("calls %d", calls)
	}
	if len(waits) < 2 {
		t.Fatalf("waits %v", waits)
	}
	if waits[0] != heartbeatBackoffCap {
		t.Fatalf("Retry-After 600s clamped to %s, want %s", waits[0], heartbeatBackoffCap)
	}
	if waits[1] != a.cfg.HeartbeatEvery {
		t.Fatalf("cadence after reconnect %s", waits[1])
	}
}
