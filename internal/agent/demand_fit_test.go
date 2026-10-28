package agent

import (
	"context"
	"testing"

	"github.com/FyberLabs/hypermesh-host/internal/hardware"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

func TestDemandFitUsesRequestedBackend(t *testing.T) {
	cpu := hardware.Recommend(hardware.Facts{MemoryBytes: 64 << 30, AvailableBytes: 48 << 30})
	hip := hardware.Recommend(hardware.Facts{MemoryBytes: 64 << 30, AvailableBytes: 48 << 30, HIP: true, AcceleratorFreeBytes: 24 << 30})
	cuda := hardware.Recommend(hardware.Facts{MemoryBytes: 64 << 30, AvailableBytes: 48 << 30, CUDA: true, AcceleratorFreeBytes: 24 << 30})
	for _, tc := range []struct {
		profile hardware.Profile
		backend string
		want    int64
	}{
		{cpu, runtime.BackendLlamaCppCPU, 36 << 30}, {hip, runtime.BackendLlamaCppCPU, 36 << 30},
		{hip, runtime.BackendHIPROCm, 18 << 30}, {cuda, "", 18 << 30},
		{cpu, runtime.BackendHIPROCm, 0}, {hip, runtime.BackendCUDAJetson, 0}, {cpu, "unknown", 0},
	} {
		if got := demandModelBudget(tc.profile, tc.backend); got != tc.want {
			t.Fatal(tc.backend, got, tc.want)
		}
		a := &Agent{modelProfile: func(context.Context) hardware.Profile { return tc.profile }}
		if err := a.checkDemandFit(context.Background(), 1<<30, tc.backend); (err == nil) != (tc.want > 0) {
			t.Fatal(tc.backend, err)
		}
		if a.checkDemandFit(context.Background(), tc.want+1, tc.backend) == nil {
			t.Fatal("oversize admitted")
		}
	}
}
