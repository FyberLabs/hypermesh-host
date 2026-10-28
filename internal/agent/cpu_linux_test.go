//go:build linux && cpu

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

func TestLeaseStartCPUContainerBindsExactModel(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	_ = os.WriteFile(uptime, []byte("1 1\n"), 0644)
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	hash := readyLeaseStart(t, a)
	a.cpu = runtime.NewCPU()
	var start []string
	a.cpu.Run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "run" {
			start = append([]string{}, args...)
			return []byte("cpu-container"), nil
		}
		return nil, nil
	}
	got := a.runJob(context.Background(), api.Job{JobID: "cpu-job", Kind: api.KindLeaseStart, DeviceID: testDevice, LeaseID: "cpu-lease", CatalogID: "llama-3.1-8b-q4", ImageDigest: "example.invalid/cpu/workload@sha256:" + strings.Repeat("a", 64), FileHash: hash, Backend: runtime.BackendLlamaCppCPU})
	if !got.Passed || got.ImageHash != hash {
		t.Fatalf("CPU result %+v", got)
	}
	text := strings.Join(start, " ")
	for _, need := range []string{"--n-gpu-layers 0", "--log-driver none", "--ulimit core=0", "/models/model.gguf:ro", "HYPERMESH_SERVE_ALIAS=llama-3.1-8b-q4"} {
		if !strings.Contains(text, need) {
			t.Fatalf("missing %s in %s", need, text)
		}
	}
	if strings.Contains(text, "--runtime=nvidia") || strings.Contains(text, "--device=") {
		t.Fatal("CPU received accelerator", text)
	}
}
