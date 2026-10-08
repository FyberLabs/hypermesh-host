//go:build linux

package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestActivationSellGuard(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		raw  string
		keep bool
		bad  bool
	}{
		{raw: `{"sell_state":"selling"}`},
		{raw: `{"sell_state":"paused"}`},
		{raw: "{\n  \"sell_state\": \"paused\"\n}\n", keep: true},
		{raw: `{"sell_state":"paused","source":"owner"}`, keep: true},
		{raw: `{"sell_state":"paused","source":"activation"}`, keep: true},
		{raw: `{"sell_state":"paused","until":"2026-10-08T00:00:00Z","source":"owner"}`, keep: true},
		{raw: `{"sell_state":"paused","until":"2026-10-01T00:00:00Z","source":"owner"}`},
		{raw: `{"sell_state":"paused","until":"soon"}`, bad: true},
		{raw: `{"sell_state":"off"}`, bad: true},
		{raw: `{"sell_state":"paused","extra":true}`, bad: true},
	} {
		keep, err := activationKeepsSell([]byte(tc.raw), now)
		if (err != nil) != tc.bad || keep != tc.keep {
			t.Fatalf("%s: keep=%v err=%v", tc.raw, keep, err)
		}
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "sell.json")
	// A fresh activation writes the guard.
	if wrote, err := prepareActivationSell(path, now); err != nil || wrote != true {
		t.Fatal(wrote, err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != string(activationSellGuard) {
		t.Fatalf("guard %s", raw)
	}
	// A selling posture is replaced by the guard while the service is stopped.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"sell_state":"selling"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if wrote, err := prepareActivationSell(path, now); err != nil || wrote != true {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != string(activationSellGuard) {
		t.Fatalf("selling not guarded: %s", raw)
	}
	// An owner pause is kept byte for byte.
	owner := `{"sell_state":"paused","until":"2026-10-08T00:00:00Z","source":"owner"}`
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(owner), 0o600); err != nil {
		t.Fatal(err)
	}
	if wrote, err := prepareActivationSell(path, now); err != nil || wrote != false {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != owner {
		t.Fatalf("owner pause replaced: %s", raw)
	}
}

func fakeOwnerSocket(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hm-sell-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.WriteFile(filepath.Join(dir, "owner.token"), []byte("test-owner-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "owner.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-owner-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler(w, r)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return dir
}

func TestSellCommandPauseResumeStatus(t *testing.T) {
	state := "selling"
	var seen []string
	dir := fakeOwnerSocket(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/sell_pause":
			state = "paused"
			_ = json.NewEncoder(w).Encode(map[string]any{"passed": true, "sell_state": state, "until": body["until"]})
		case "/v0/sell_resume":
			state = "selling"
			_ = json.NewEncoder(w).Encode(map[string]any{"passed": true, "sell_state": state})
		case "/v0/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"sell_state": state, "maintenance": false})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ctx := context.Background()
	var out, diag bytes.Buffer
	if code := runSell(ctx, dir, []string{"pause", "--until=2026-10-08T09:00:00Z"}, &out, &diag); code != 0 {
		t.Fatalf("pause code %d: %s", code, diag.String())
	}
	if !strings.Contains(out.String(), `"sell_state":"paused"`) || !strings.Contains(out.String(), "2026-10-08T09:00:00Z") {
		t.Fatalf("pause output %s", out.String())
	}
	out.Reset()
	if code := runSell(ctx, dir, []string{"resume"}, &out, &diag); code != 0 || !strings.Contains(out.String(), `"sell_state":"selling"`) {
		t.Fatalf("resume code %d: %s %s", code, out.String(), diag.String())
	}
	out.Reset()
	if code := runSell(ctx, dir, []string{"status"}, &out, &diag); code != 0 || !strings.Contains(out.String(), `"sell_state":"selling"`) {
		t.Fatalf("status code %d: %s", code, out.String())
	}
	want := []string{"POST /v0/sell_pause", "POST /v0/sell_resume", "GET /v0/status"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("requests %v", seen)
	}
	for _, args := range [][]string{nil, {"pause", "--until=soon"}, {"resume", "now"}, {"start"}} {
		if code := runSell(ctx, dir, args, &out, &diag); code != 2 {
			t.Fatalf("%v accepted with code %d", args, code)
		}
	}
	if len(seen) != len(want) {
		t.Fatal("invalid usage reached the agent")
	}
}

func TestSellCommandReportsRefusal(t *testing.T) {
	dir := fakeOwnerSocket(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"passed": false, "sell_state": "paused", "reason": "host maintenance hold"})
	})
	var out, diag bytes.Buffer
	if code := runSell(context.Background(), dir, []string{"resume"}, &out, &diag); code != 1 {
		t.Fatalf("refusal code %d", code)
	}
	if !strings.Contains(diag.String(), "maintenance") {
		t.Fatalf("diagnostic %s", diag.String())
	}
	missing := t.TempDir()
	if code := runSell(context.Background(), missing, []string{"status"}, &out, &diag); code != 1 {
		t.Fatalf("missing agent code %d", code)
	}
}

func TestActivationReleaseWaitsForAgentUpdate(t *testing.T) {
	var calls atomic.Int32
	dir := fakeOwnerSocket(t, func(w http.ResponseWriter, r *http.Request) {
		// The first answers come from the older agent; then it has updated.
		if calls.Add(1) <= 2 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sell_state":"selling"}`))
	})
	var diag bytes.Buffer
	if err := releaseActivationSellAt(context.Background(), dir, 0, 10*time.Second, &diag); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || diag.Len() != 0 {
		t.Fatalf("calls=%d diag=%q", calls.Load(), diag.String())
	}
}

func TestActivationReleaseLeavesGuardWhenAgentCannotRelease(t *testing.T) {
	dir := fakeOwnerSocket(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	sell := filepath.Join(dir, "sell.json")
	if err := os.WriteFile(sell, activationSellGuard, 0o600); err != nil {
		t.Fatal(err)
	}
	var diag bytes.Buffer
	if err := releaseActivationSellAt(context.Background(), dir, 0, 1500*time.Millisecond, &diag); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diag.String(), "sudo hypermesh-deploy sell resume") {
		t.Fatalf("no resume instruction: %q", diag.String())
	}
	if raw, _ := os.ReadFile(sell); !bytes.Equal(raw, activationSellGuard) {
		t.Fatalf("installer changed sell.json: %s", raw)
	}
}

func TestActivationReleaseReportsUnreleasedGuard(t *testing.T) {
	dir := fakeOwnerSocket(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sell_state":"paused","pause_source":"activation"}`))
	})
	if err := releaseActivationSellAt(context.Background(), dir, 0, time.Minute, io.Discard); err == nil {
		t.Fatal("unreleased guard reported as released")
	}
}

func TestActivationReleaseFailsWhenAgentStopsAnsweringAfterMissingRoute(t *testing.T) {
	var calls atomic.Int32
	dir := fakeOwnerSocket(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	var diag bytes.Buffer
	if err := releaseActivationSellAt(context.Background(), dir, 0, 1500*time.Millisecond, &diag); err == nil {
		t.Fatalf("agent failure reported as a missing route: %q", diag.String())
	}
}
