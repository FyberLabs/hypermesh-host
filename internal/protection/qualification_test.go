package protection

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sum(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func ready(t *testing.T, modes []Mode, backend string) (Report, Binding, time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	b := Binding{BootID: "protected-boot", Hardware: "lab-board", Firmware: "vendor-version", ModelSHA256: sum("model"), RuntimeSHA256: sum("runtime"), Backend: backend, ContextTokens: 4096}
	r, err := Template(modes, b, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for i := range r.Checks {
		r.Checks[i].Status = "passed"
		r.Checks[i].ArtifactSHA256 = sum(r.Checks[i].ID)
	}
	for i := 0; i < 6; i++ {
		binding := b
		if i < 3 {
			binding.BootID = "baseline-boot"
		}
		r.Samples = append(r.Samples, Sample{ID: fmt.Sprint(i), Binding: binding, Protected: i >= 3, TokensPerSecond: 10, PeakBytes: 1024, ArtifactSHA256: sum(fmt.Sprint(i))})
	}
	return r, b, now
}

func TestEvidenceReadyNeverQualifies(t *testing.T) {
	for _, modes := range [][]Mode{{Physical}, {Confidential}, {Physical, Confidential}} {
		for _, backend := range []string{"cpu", "uma", "discrete_gpu"} {
			r, b, now := ready(t, modes, backend)
			out := Evaluate(r, b, now)
			if !out.ReviewReady || out.Qualified || out.ThroughputRatio != 1 {
				t.Fatalf("%+v", out)
			}
		}
	}
}

func TestFailClosedEvidence(t *testing.T) {
	for _, kind := range []string{"expired", "future", "long-window", "schema", "mode", "duplicate-mode", "binding", "firmware", "boot", "missing-check", "failed-check", "duplicate-check", "bad-artifact", "missing-samples", "replay", "artifact-replay", "protected-boot", "workload", "nan", "infinity", "zero-memory"} {
		t.Run(kind, func(t *testing.T) {
			r, b, now := ready(t, []Mode{Physical, Confidential}, "uma")
			switch kind {
			case "expired":
				r.ExpiresAt = now
			case "future":
				r.ObservedAt = now.Add(time.Minute)
			case "long-window":
				r.ExpiresAt = r.ObservedAt.Add(25 * time.Hour)
			case "schema":
				r.Schema = "v99"
			case "mode":
				r.Modes = []Mode{"unknown"}
			case "duplicate-mode":
				r.Modes = []Mode{Physical, Physical}
			case "binding":
				b.ModelSHA256 = sum("other")
			case "firmware":
				b.Firmware = "new-version"
			case "boot":
				b.BootID = "reboot"
			case "missing-check":
				r.Checks = r.Checks[1:]
			case "failed-check":
				r.Checks[0].Status = "failed"
			case "duplicate-check":
				r.Checks = append(r.Checks, r.Checks[0])
			case "bad-artifact":
				r.Checks[0].ArtifactSHA256 = "bad"
			case "missing-samples":
				r.Samples = r.Samples[:5]
			case "replay":
				r.Samples[1].ID = r.Samples[0].ID
			case "artifact-replay":
				r.Samples[1].ArtifactSHA256 = r.Samples[0].ArtifactSHA256
			case "protected-boot":
				r.Samples[3].Binding.BootID = "old"
			case "workload":
				r.Samples[0].Binding.ContextTokens = 1024
			case "nan":
				r.Samples[0].TokensPerSecond = math.NaN()
			case "infinity":
				r.Samples[0].TokensPerSecond = math.Inf(1)
			case "zero-memory":
				r.Samples[0].PeakBytes = 0
			}
			if out := Evaluate(r, b, now); out.ReviewReady || out.Qualified || len(out.Blockers) == 0 {
				t.Fatalf("unsafe result: %+v", out)
			}
		})
	}
}

func TestArtifacts(t *testing.T) {
	dir := t.TempDir()
	content := "vendor boot evidence"
	ref := sum(content)
	r := Report{Checks: []Check{{ArtifactSHA256: ref}}}
	path := filepath.Join(dir, ref)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArtifacts(r, dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if VerifyArtifacts(r, dir) == nil {
		t.Fatal("accepted tampered artifact")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", path); err != nil {
		t.Fatal(err)
	}
	if VerifyArtifacts(r, dir) == nil {
		t.Fatal("accepted symlink")
	}
}

func TestCLIRejectsBadPinsAndModes(t *testing.T) {
	for _, args := range [][]string{{}, {"--model-sha256", sum("m"), "--runtime-sha256", sum("r"), "--modes", "physical_memory,physical_memory"}, {"--model-sha256", sum("m"), "--runtime-sha256", sum("r"), "--backend", "metal"}} {
		var out, diagnostic strings.Builder
		if Run(args, &out, &diagnostic) != 2 {
			t.Fatalf("accepted invalid args: %v", args)
		}
	}
}
