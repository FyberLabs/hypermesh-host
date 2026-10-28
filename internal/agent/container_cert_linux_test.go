//go:build linux && cpu

package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/FyberLabs/hypermesh-host/internal/hardware"
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

func TestContainerCertificationDownloadsSourceRefWithBackendFit(t *testing.T) {
	backends := []string{runtime.BackendLlamaCppCPU}
	if runtime.HIPImageEnabled() {
		backends = append(backends, runtime.BackendHIPROCm)
	}
	for _, backend := range backends {
		t.Run(backend, func(t *testing.T) {
			a, job, _ := certFixture(t, 200)
			job.Backend = backend
			body, err := os.ReadFile(a.cfg.GGUFPath)
			if err != nil {
				t.Fatal(err)
			}
			job.SizeBytes = int64(len(body))
			job.SourceRef = "bartowski/model@" + strings.Repeat("b", 40) + "/model.gguf"
			fitReady := false
			a.modelProfile = func(context.Context) hardware.Profile {
				if !fitReady {
					return hardware.Profile{}
				}
				return hardware.Recommend(hardware.Facts{MemoryBytes: 64 << 30, AvailableBytes: 48 << 30, HIP: backend == runtime.BackendHIPROCm, AcceleratorFreeBytes: 32 << 30})
			}
			downloads := 0
			a.fetchWeights = func(_ context.Context, dest, _, pin string) error {
				if pin != job.FileHash {
					t.Fatal("download lost model pin")
				}
				downloads++
				return os.WriteFile(dest, body, 0600)
			}
			a.cpu = runtime.NewCPU()
			a.cpu.Run = a.runtime.Run
			a.hip = &runtime.HIP{Run: a.runtime.Run}
			blocked := a.runJob(context.Background(), job)
			if blocked.Passed || downloads != 0 {
				t.Fatal("missing runtime evidence allowed download", blocked, downloads)
			}
			fitReady = true
			for attempt := 0; attempt < 2; attempt++ {
				got := a.runJob(context.Background(), job)
				if !got.Passed || !got.Measured || downloads != 1 {
					t.Fatalf("source_ref/cached certification rejected: %+v downloads=%d attempt=%d", got, downloads, attempt)
				}
			}
		})
	}
}
