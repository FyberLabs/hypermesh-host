package installer

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"time"
)

//go:embed uma_probe.py
var umaProbeScript string

// UMARuntime is a current runtime capacity snapshot, not an allocation guarantee.
type UMARuntime struct {
	Backend string `json:"backend"`
	Device int `json:"device"`
	FreeBytes int64 `json:"free_bytes"`
	TotalBytes int64 `json:"total_bytes"`
}

func validUMARuntime(r *UMARuntime) bool {
	return r != nil && (r.Backend == "cuda" || r.Backend == "hip") &&
		r.Device >= 0 && r.Device < 64 && r.FreeBytes > 0 && r.TotalBytes >= r.FreeBytes
}

// boundedProbeOutput rejects excess output without retaining driver log floods.
type boundedProbeOutput struct { bytes.Buffer }
func (b *boundedProbeOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4096 { return 0, fmt.Errorf("runtime output too large") }
	return b.Buffer.Write(p)
}

func probeUMARuntime(ctx context.Context) (*UMARuntime, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "python3", "-I", "-c", umaProbeScript)
	var out boundedProbeOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("cannot query UMA runtime: install Python 3 and the supported CUDA/HIP driver, check GPU access and expose exactly one integrated accelerator; no allocation inferred from RAM")
	}
	var r UMARuntime
	if err := json.Unmarshal(out.Bytes(), &r); err != nil || !validUMARuntime(&r) {
		return nil, fmt.Errorf("invalid UMA runtime measurement; check the CUDA/HIP installation")
	}
	return &r, nil
}

// Host reserve is ceil(10% of OS-visible RAM), with a 2 GiB floor.
// Runtime reserve covers bookkeeping; model/context fit still needs a benchmark.
func umaBudget(r Resources) (hostReserve, runtimeReserve, maximum int64) {
	hostReserve = r.SystemBytes/10
	if r.SystemBytes%10 > 0 { hostReserve++ }
	if hostReserve < 2*GiB { hostReserve = 2*GiB }
	runtimeReserve = GiB
	if !validUMARuntime(r.UMARuntime) { return }
	hostBudget := r.AvailableSystemBytes-hostReserve
	runtimeBudget := r.UMARuntime.FreeBytes-runtimeReserve
	maximum = hostBudget
	if runtimeBudget < maximum { maximum = runtimeBudget }
	if maximum < 0 { maximum = 0 }
	return
}
