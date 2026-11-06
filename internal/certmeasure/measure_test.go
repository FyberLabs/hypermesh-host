package certmeasure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/hardware"
)

func fixture(t *testing.T) ([]string, dependencies, *bytes.Buffer, string) {
	t.Helper()
	dir := t.TempDir()
	model := filepath.Join(dir, "model.gguf")
	binary := filepath.Join(dir, "llama-bench")
	content := []byte("GGUFfixture")
	if err := os.WriteFile(model, content, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, content, 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	pin := hex.EncodeToString(sum[:])
	artifacts := filepath.Join(dir, "evidence")
	args := []string{"--model-file", model, "--runtime-file", binary, "--model-sha256", pin, "--runtime-sha256", pin, "--backend", "cpu", "--state-dir", dir, "--artifact-dir", artifacts}
	profile := hardware.Profile{Fingerprint: "stable", Backend: "cpu", ModelBudgetBytes: 1 << 30, Facts: hardware.Facts{AvailableBytes: 4 << 30}}
	deps := dependencies{inventory: func(context.Context) string { return "sha256:inventory" }, detect: func(context.Context) hardware.Profile { return profile }, execute: func(ctx context.Context, path string, args []string) ([]byte, time.Duration, error) {
		raw, _ := json.Marshal([]map[string]any{{"model_filename": model, "n_prompt": 0, "n_gen": 32, "n_depth": 480, "n_gpu_layers": 0, "backends": "CPU", "devices": "none", "avg_ns": 1000000000, "samples_ns": []int{1000000000}}})
		return raw, 2 * time.Second, nil
	}}
	return args, deps, &bytes.Buffer{}, artifacts
}
func readReport(t *testing.T, out *bytes.Buffer) Report {
	t.Helper()
	b, err := os.ReadFile(strings.TrimSpace(out.String()))
	if err != nil {
		t.Fatal(err)
	}
	var r Report
	if err = json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}
func TestMeasurementsNeedNoEnrollmentOrBaseline(t *testing.T) {
	args, deps, out, _ := fixture(t)
	var diagnostic bytes.Buffer
	if code := run(context.Background(), args, out, &diagnostic, deps); code != 0 {
		t.Fatalf("%d %s", code, diagnostic.String())
	}
	r := readReport(t, out)
	if r.MeasurementState != "passed" || len(r.Samples) != 3 || r.CertificationState != "not_evaluated" || r.PhysicalAcceptance {
		t.Fatalf("%+v", r)
	}
}
func TestMissingModelRetainsFailure(t *testing.T) {
	args, deps, out, _ := fixture(t)
	os.Remove(args[1])
	if run(context.Background(), args, out, &bytes.Buffer{}, deps) != 1 {
		t.Fatal("missing model passed")
	}
	r := readReport(t, out)
	if r.FailureCode != "model_unavailable_or_pin_mismatch" || r.MeasurementState != "failed" {
		t.Fatalf("%+v", r)
	}
}
func TestLeaseNeverInterrupted(t *testing.T) {
	args, deps, out, _ := fixture(t)
	state := args[11]
	if err := os.WriteFile(filepath.Join(state, "lease.json"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	deps.execute = func(context.Context, string, []string) ([]byte, time.Duration, error) {
		t.Fatal("ran with a lease")
		return nil, 0, nil
	}
	if run(context.Background(), args, out, &bytes.Buffer{}, deps) != 1 {
		t.Fatal("lease ignored")
	}
	if readReport(t, out).FailureCode != "active_lease" {
		t.Fatal(out.String())
	}
}
func TestPartialSamplesRetained(t *testing.T) {
	args, deps, out, _ := fixture(t)
	original := deps.execute
	calls := 0
	deps.execute = func(c context.Context, p string, a []string) ([]byte, time.Duration, error) {
		calls++
		if calls == 2 {
			return []byte("invalid"), time.Second, nil
		}
		return original(c, p, a)
	}
	if run(context.Background(), args, out, &bytes.Buffer{}, deps) != 1 {
		t.Fatal("invalid sample passed")
	}
	r := readReport(t, out)
	if len(r.Samples) != 1 || r.FailureCode != "invalid_measurement" {
		t.Fatalf("%+v", r)
	}
}
func TestBenchmarkMustMatchActualRequestedBackendAndContext(t *testing.T) {
	o := options{model: "/model", backend: "metal", context: 512, generated: 32}
	valid := map[string]any{"model_filename": "/model", "n_prompt": 0, "n_gen": 32, "n_depth": 480, "n_gpu_layers": 99, "backends": "Metal,BLAS", "devices": "MTL0", "avg_ns": 1e9, "samples_ns": []float64{1e9}}
	for _, key := range []string{"valid", "model_filename", "n_gen", "n_depth", "n_gpu_layers", "backends", "devices", "avg_ns", "samples_ns"} {
		t.Run(key, func(t *testing.T) {
			row := map[string]any{}
			for k, v := range valid {
				row[k] = v
			}
			if key != "valid" {
				delete(row, key)
			}
			raw, _ := json.Marshal([]any{row})
			_, err := parse(raw, o, 2*time.Second)
			if (err == nil) != (key == "valid") {
				t.Fatalf("%s: %v", key, err)
			}
		})
	}
}
func TestInputMutationCannotPass(t *testing.T) {
	args, deps, out, _ := fixture(t)
	original := deps.execute
	deps.execute = func(c context.Context, p string, a []string) ([]byte, time.Duration, error) {
		raw, elapsed, err := original(c, p, a)
		os.WriteFile(args[1], []byte("changed"), 0600)
		return raw, elapsed, err
	}
	if run(context.Background(), args, out, &bytes.Buffer{}, deps) != 1 {
		t.Fatal("mutation passed")
	}
	if readReport(t, out).FailureCode != "input_or_hardware_drift" {
		t.Fatal(out.String())
	}
}

func TestMetalUpstreamAbbreviation(t *testing.T) {
	raw := []byte(`[{"model_filename":"/model","n_prompt":0,"n_gen":32,"n_depth":480,"n_gpu_layers":99,"backends":"MTL,BLAS","devices":"MTL0","avg_ns":1000000000,"samples_ns":[1000000000]}]`)
	if _, err := parse(raw, options{model: "/model", backend: "metal", context: 512, generated: 32}, 2*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestRequestedCPUBudgetIgnoresDetectedAccelerator(t *testing.T) {
	for _, backend := range []string{"cuda", "hip", "vulkan"} {
		p := hardware.Profile{Backend: backend, ModelBudgetBytes: 0, Facts: hardware.Facts{AvailableBytes: 16 << 30, AcceleratorFreeBytes: 1 << 30}}
		if got := memoryBudget(p, "cpu"); got != 12<<30 {
			t.Fatalf("%s: %d", backend, got)
		}
	}
}
func TestBareRuntimeIsResolvedBeforeExecution(t *testing.T) {
	args, deps, out, _ := fixture(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(filepath.Dir(args[3])); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)
	absolute := args[3]
	args[3] = "llama-bench"
	original := deps.execute
	deps.execute = func(c context.Context, p string, a []string) ([]byte, time.Duration, error) {
		if p != absolute || !filepath.IsAbs(p) {
			t.Fatalf("unpinned PATH lookup: %s", p)
		}
		return original(c, p, a)
	}
	if code := run(context.Background(), args, out, &bytes.Buffer{}, deps); code != 0 {
		t.Fatal(code)
	}
}
func TestDeviceSelectionRejectsWrongAccelerator(t *testing.T) {
	o := options{model: "/model", backend: "vulkan", context: 512, generated: 32}
	raw := []byte(`[{"model_filename":"/model","n_prompt":0,"n_gen":32,"n_depth":480,"n_gpu_layers":99,"devices":"CUDA0","backends":"CUDA,Vulkan","avg_ns":1000000000,"samples_ns":[1000000000]}]`)
	if _, err := parse(raw, o, 2*time.Second); err == nil {
		t.Fatal("accepted CUDA under Vulkan label")
	}
	if !validDevice("metal", "MTL0") || validDevice("metal", "CUDA0") || validDevice("cuda", "CUDA0,Vulkan0") {
		t.Fatal("incorrect device binding")
	}
	args := arguments(o)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-dev Vulkan0") {
		t.Fatal(joined)
	}
}
