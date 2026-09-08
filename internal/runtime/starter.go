package runtime

import "context"

// Starter starts and stops a leased workload. NVIDIA is the AGX path.
// HIP and Metal are scaffolds on other host images; they must not fake
// completions or tok/s.
type Starter interface {
	Start(ctx context.Context, spec Spec) (*Handle, error)
	Stop(ctx context.Context, h *Handle) error
}
