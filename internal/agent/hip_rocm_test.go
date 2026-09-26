//go:build rocm

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

func TestLeaseStartHIPROCmScaffold(t *testing.T) {
	const ref = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	var hipCalls [][]string
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	wantHash := readyLeaseStart(t, a)
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("hip-rocm must not use nvidia docker")
		return nil, nil
	}}
	a.hip = &runtime.HIP{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		hipCalls = append(hipCalls, append([]string{}, args...))
		if len(args) > 0 && args[0] == "network" {
			return []byte{}, nil
		}
		return []byte("cid-hip\n"), nil
	}}
	got := a.runJob(context.Background(), api.Job{
		JobID:       "j-hip",
		Kind:        api.KindLeaseStart,
		DeviceID:    testDevice,
		LeaseID:     "lease-hip",
		CatalogID:   "llama-3.1-8b-q4",
		ImageDigest: ref,
		FileHash:    wantHash,
		Runtime:     "nvidia",
		Backend:     "hip-rocm",
	})
	if !got.Passed || got.Reason != "lease_start" || got.ImageHash != wantHash {
		t.Fatalf("hip-rocm scaffold result %+v want hash %q", got, wantHash)
	}
	var runArgs []string
	for _, c := range hipCalls {
		if len(c) > 0 && c[0] == "run" {
			runArgs = c
			break
		}
		if len(c) > 0 && c[0] == "pull" {
			t.Fatalf("expected hip docker run without pull, calls=%v", hipCalls)
		}
	}
	if runArgs == nil {
		t.Fatalf("expected hip docker run, calls=%v", hipCalls)
	}
	joined := strings.Join(runArgs, " ")
	if strings.Contains(joined, "--runtime=nvidia") || strings.Contains(joined, "HSA_OVERRIDE") ||
		strings.Contains(joined, "gfx1036") || strings.Contains(joined, "--gpus") {
		t.Fatalf("hip args %v", runArgs)
	}
	if strings.Contains(joined, "--network=host") || !strings.Contains(joined, runtime.WorkloadNetworkName) {
		t.Fatalf("hip workload jail network: %v", runArgs)
	}
}

func TestStubVulkanStillFailsOnROCmImage(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0o644)
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	got := a.runJob(context.Background(), api.Job{
		JobID:    "j-vk",
		Kind:     api.KindLeaseStart,
		DeviceID: testDevice,
		LeaseID:  "lease-vk",
		Backend:  "llama-cpp-vulkan",
	})
	if got.Passed || got.Reason != "This image does not run llama-cpp-vulkan." {
		t.Fatalf("%+v", got)
	}
}
