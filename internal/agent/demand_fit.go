package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/hardware"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

func demandModelBudget(profile hardware.Profile, backend string) int64 {
	switch runtime.NormalizeBackend(backend) {
	case runtime.BackendCUDAJetson:
		if profile.Backend == "cuda" {
			return profile.ModelBudgetBytes
		}
	case runtime.BackendHIPROCm:
		if profile.Backend == "hip" {
			return profile.ModelBudgetBytes
		}
	case runtime.BackendLlamaCppCPU:
		// CPU is explicitly requested. Budget system RAM even if this host also
		// has a GPU; never borrow a GPU budget or silently replace a GPU request.
		facts := profile.Facts
		if facts.MemoryBytes <= 0 || facts.AvailableBytes <= 0 || facts.AvailableBytes > facts.MemoryBytes {
			return 0
		}
		facts.CUDA = false
		facts.HIP = false
		facts.Metal = false
		facts.Vulkan = false
		return hardware.Recommend(facts).ModelBudgetBytes
	}
	return 0
}

func (a *Agent) checkDemandFit(ctx context.Context, size int64, backend string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.modelFit != nil {
		return a.modelFit(ctx, size)
	}
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	detect := a.modelProfile
	if detect == nil {
		detect = hardware.Detect
	}
	profile := detect(bounded)
	if bounded.Err() != nil || size <= 0 || demandModelBudget(profile, backend) < size {
		return fmt.Errorf("requested backend or available memory cannot fit the selected model")
	}
	return nil
}
