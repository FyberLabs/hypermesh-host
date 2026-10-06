package agent

import (
	"context"
	"fmt"
	"github.com/FyberLabs/hypermesh-host/internal/hardware"
	"time"
)

func (a *Agent) checkDemandFit(ctx context.Context, size int64) error {
	if a.modelFit != nil {
		return a.modelFit(ctx, size)
	}
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	profile := hardware.Detect(bounded)
	// Current lease/qualification executors use NVIDIA. Other profiles retain
	// their own future runners; a downloadable GGUF is not certification.
	if profile.Backend != "cuda" || size <= 0 || profile.ModelBudgetBytes < size {
		return fmt.Errorf("current accelerator/runtime or free memory cannot fit the selected model")
	}
	return nil
}
