package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestUMABudgets(t *testing.T) {
	base := Resources{SystemBytes: 64*GiB, AvailableSystemBytes: 48*GiB,
		UMA: true, UMAAllocationBytes: 40*GiB, DiskFreeBytes: 100*GiB,
		UMARuntime: &UMARuntime{Backend: "cuda", FreeBytes: 48*GiB, TotalBytes: 64*GiB}}
	for _, tc := range []struct {
		name string
		change func(*Resources)
		pass bool
	}{
		{"valid CUDA", func(r *Resources) {}, true},
		{"valid HIP", func(r *Resources) { r.UMARuntime.Backend = "hip" }, true},
		{"unknown runtime", func(r *Resources) { r.UMARuntime = nil }, false},
		{"invalid runtime", func(r *Resources) { r.UMARuntime.FreeBytes = 65*GiB }, false},
		{"unsupported runtime", func(r *Resources) { r.UMARuntime.Backend = "cpu" }, false},
		{"OS headroom", func(r *Resources) { r.AvailableSystemBytes = 42*GiB }, false},
		{"runtime headroom", func(r *Resources) { r.UMARuntime.FreeBytes = 40*GiB }, false},
		{"runtime boundary", func(r *Resources) { r.UMARuntime.FreeBytes = 41*GiB }, true},
		{"runtime boundary minus byte", func(r *Resources) { r.UMARuntime.FreeBytes = 41*GiB-1 }, false},
		{"invalid ordinal", func(r *Resources) { r.UMARuntime.Device = -1 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			runtime := *base.UMARuntime
			r.UMARuntime = &runtime
			tc.change(&r)
			a := CheckResources(r)
			if a.Eligible != tc.pass { t.Fatalf("%+v", a) }
			if a.HostReserveBytes != (64*GiB+9)/10 || a.RuntimeReserveBytes != GiB {
				t.Fatalf("missing reserve evidence: %+v", a)
			}
		})
	}
	r := base
	_, _, maximum := umaBudget(r)
	r.UMAAllocationBytes = maximum
	if !CheckResources(r).Eligible { t.Fatal("exact host boundary rejected") }
	r.UMAAllocationBytes++
	if CheckResources(r).Eligible { t.Fatal("host headroom boundary not enforced") }
	r.SystemBytes = 16*GiB
	reserve, _, _ := umaBudget(r)
	if reserve != 2*GiB { t.Fatal("2 GiB floor lost") }
}

func TestUMACommandFailsWithoutRuntimeEvidence(t *testing.T) {
	var out, diagnostic bytes.Buffer
	probe := func(context.Context, string, string, int64) (Resources, error) {
		return Resources{SystemBytes: 64*GiB, AvailableSystemBytes: 48*GiB,
			UMA: true, UMAAllocationBytes: 40*GiB, DiskFreeBytes: 100*GiB}, nil
	}
	code := runResourceCheck(context.Background(), []string{"--memory-mode", "uma",
		"--uma-gib", "40", "--model-dir", "."}, &out, &diagnostic, probe)
	var report ResourceReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil { t.Fatal(err) }
	if code != 1 || report.Admission.Eligible || !strings.Contains(out.String(), "repair the CUDA/HIP") {
		t.Fatalf("code %d: %s", code, &out)
	}
}

func TestRuntimeProbeCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := probeUMARuntime(ctx); err == nil { t.Fatal("cancelled probe succeeded") }
}

func TestRuntimeOutputBound(t *testing.T) {
	var out boundedProbeOutput
	if _, err := out.Write(make([]byte, 4096)); err != nil { t.Fatal(err) }
	if _, err := out.Write([]byte("x")); err == nil { t.Fatal("unbounded driver output") }
	if out.Len() != 4096 { t.Fatal("retained excess output") }
}
