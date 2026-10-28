//go:build linux && cpu

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

func TestContainerCandidatesCertifyOnlyTheRequestedPins(t *testing.T) {
	backends := []string{runtime.BackendLlamaCppCPU}
	if runtime.HIPImageEnabled() {
		backends = append(backends, runtime.BackendHIPROCm)
	}
	for _, backend := range backends {
		for _, failure := range []string{"", "model hash", "image pin", "inference"} {
			t.Run(backend+"/"+failure, func(t *testing.T) {
				status := 200
				if failure == "inference" {
					status = 503
				}
				a, job, calls := certFixture(t, status)
				job.Backend = backend
				a.cpu = runtime.NewCPU()
				a.cpu.Run = a.runtime.Run
				a.hip = &runtime.HIP{Run: a.runtime.Run}
				if failure == "model hash" {
					job.FileHash = strings.Repeat("b", 64)
				}
				if failure == "image pin" {
					job.ImageDigest = "example.invalid/runtime:mutable"
				}
				got := a.runJob(context.Background(), job)
				if failure != "" {
					if got.Passed || got.Measured || got.WorkloadVerified {
						t.Fatal("invalid candidate certified", got)
					}
					return
				}
				if !got.Passed || !got.Measured || !got.WorkloadVerified || got.DecodeTokS <= 0 {
					t.Fatal("pinned candidate failed", got)
				}
				if a.localLease() != nil {
					t.Fatal("certification activated a lease")
				}
				text := ""
				for _, call := range *calls {
					text += strings.Join(call, " ") + "\n"
				}
				for _, part := range []string{"pull " + certTestPin, "HYPERMESH_SERVE_ALIAS=" + job.CatalogID, "/models/model.gguf:ro", "--log-driver none", "--ulimit core=0", "stop cert-container", "rm hypermesh-cert-bench-"} {
					if !strings.Contains(text, part) {
						t.Fatalf("missing %s: %s", part, text)
					}
				}
			})
		}
	}
}
