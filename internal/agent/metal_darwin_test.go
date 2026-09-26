//go:build darwin

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

func TestLeaseStartLlamaCppCPUMetal(t *testing.T) {
	const ref = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var metalCalls [][]string
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	wantHash := readyLeaseStart(t, a)
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("darwin llama-cpp-cpu must not use nvidia docker")
		return nil, nil
	}}
	a.metal = &runtime.Metal{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		metalCalls = append(metalCalls, append([]string{name}, args...))
		return []byte("99\n"), nil
	}}
	got := a.runJob(context.Background(), api.Job{
		JobID:       "j-metal",
		Kind:        api.KindLeaseStart,
		DeviceID:    testDevice,
		LeaseID:     "lease-metal",
		CatalogID:   "llama-3.1-8b-q4",
		ImageDigest: ref,
		FileHash:    wantHash,
		Runtime:     "nvidia",
		Backend:     "llama-cpp-cpu",
	})
	if !got.Passed || got.Reason != "lease_start" || got.ImageHash != wantHash {
		t.Fatalf("metal scaffold result %+v want hash %q", got, wantHash)
	}
	if len(metalCalls) != 1 || metalCalls[0][0] != "llama-server" {
		t.Fatalf("expected llama-server, calls=%v", metalCalls)
	}
	joined := strings.Join(metalCalls[0], " ")
	if strings.Contains(strings.ToLower(joined), "cuda") || strings.Contains(joined, "nvidia") ||
		strings.Contains(joined, "--gpus") {
		t.Fatalf("metal args %v", metalCalls[0])
	}
}

func TestUnknownLlamaCppMetalFails(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-name",
		Kind:     api.KindLeaseStart,
		DeviceID: testDevice,
		LeaseID:  "lease-x",
		Backend:  "llama-cpp-metal",
	})
	if got.Passed || got.Reason != "This image does not run llama-cpp-metal." {
		t.Fatalf("%+v", got)
	}
}
