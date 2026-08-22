// Package pathb runs the only suite this slice knows: stub-suite-v1.
//
// The stub collects image hash / uptime / thermal when available and
// reports passed plus image_hash. It does not invent a real cert suite,
// an inference bench, or tokens/sec.
package pathb

import (
	"context"
	"fmt"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/telem"
)

// SuiteStubV1 is the platform default suite_id. There is no suite payload.
const SuiteStubV1 = "stub-suite-v1"

// Result is what the agent POSTs back. No tok/s field.
type Result struct {
	Passed    bool
	ImageHash string
	Reason    string
}

// Collector is the telemetry the stub needs.
type Collector interface {
	Collect(ctx context.Context) (telem.Snapshot, error)
}

// Runner executes stub-suite-v1 for path_b or instance_check jobs.
type Runner struct {
	Collector Collector
}

// Run collects host facts. Unknown suite ids fail. Missing telemetry fails.
// A successful stub does not claim a loaded-model soak.
func (r Runner) Run(ctx context.Context, job api.CertJob) Result {
	if job.SuiteID != "" && job.SuiteID != SuiteStubV1 {
		return Result{Passed: false, Reason: fmt.Sprintf("unknown suite %s", job.SuiteID)}
	}
	if r.Collector == nil {
		return Result{Passed: false, Reason: "no telemetry collector"}
	}
	snap, err := r.Collector.Collect(ctx)
	if err != nil {
		return Result{Passed: false, Reason: "telemetry unavailable"}
	}
	return Result{Passed: true, ImageHash: snap.ImageHash, Reason: "stub-suite-v1"}
}
