package runtime

import (
	"context"
	"strings"
	"testing"
)

func TestCPUContainerBindsPinnedModelWithoutAccelerators(t *testing.T) {
	spec := Spec{LeaseID: "cpu-lease", CatalogID: "exact-model", GGUFPath: "/weights/exact.gguf", ImageDigest: testRef}
	args := CPUStartArgs(spec, testRef)
	assertWorkloadJail(t, args)
	text := strings.Join(args, " ")
	for _, forbidden := range []string{"--runtime=nvidia", "--device=", "--gpus", "HSA_OVERRIDE"} {
		if strings.Contains(text, forbidden) {
			t.Fatal("CPU exposed accelerator", text)
		}
	}
	for _, required := range []string{"/weights/exact.gguf:/models/model.gguf:ro", "HYPERMESH_SERVE_ALIAS=exact-model", "--n-gpu-layers 0"} {
		if !strings.Contains(text, required) {
			t.Fatal("CPU model binding missing", required)
		}
	}
	cpu := NewCPU()
	cpu.Run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "run" {
			return []byte("cpu-container"), nil
		}
		return nil, nil
	}
	h, err := cpu.Start(context.Background(), spec)
	if err != nil || h.Backend != BackendLlamaCppCPU || h.ContainerID != "cpu-container" {
		t.Fatalf("%+v %v", h, err)
	}
	if err := cpu.Stop(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if CPUImageEnabled() {
		path, reason := ResolveBackend(BackendLlamaCppCPU)
		if path != CPUContainer || reason != "" {
			t.Fatal(path, reason)
		}
	}
}
