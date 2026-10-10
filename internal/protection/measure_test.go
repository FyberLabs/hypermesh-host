//go:build linux

package protection

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func adapterFixture(t *testing.T, body string) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "adapter")
	content := "#!/bin/sh\n" + body
	if err := os.WriteFile(path, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := pinnedFile(path, sum(content))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestPinnedFilesRejectMismatchAndSymlinks(t *testing.T) {
	f := adapterFixture(t, "exit 0\n")
	if _, err := pinnedFile(f.Name(), sum("wrong")); err == nil {
		t.Fatal("accepted mismatched pin")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(f.Name(), link); err != nil {
		t.Fatal(err)
	}
	if _, err := pinnedFile(link, sum("wrong")); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestMeasurementsRequireValidAdapterOutputAndRespectTimeout(t *testing.T) {
	model := adapterFixture(t, "test model\n")
	for _, tc := range []struct {
		name, body string
		ok         bool
	}{
		{"valid", "sleep 0.05\nprintf '%s' '{\"generated_tokens\":128,\"inference_seconds\":0.01}'\n", true},
		{"mismatch", "printf '%s' '{\"generated_tokens\":10,\"inference_seconds\":0.01}'\n", false},
		{"zero", "printf '%s' '{\"generated_tokens\":0,\"inference_seconds\":0.01}'\n", false},
		{"unknown", "printf '%s' '{\"generated_tokens\":10,\"inference_seconds\":0.01,\"qualified\":true}'\n", false},
		{"trailing", "printf '%s' '{\"generated_tokens\":10,\"inference_seconds\":0.01} {}'\n", false},
		{"deadline", "sleep 30\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter := adapterFixture(t, tc.body)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			m, peak, _, err := measure(ctx, adapter, model, Binding{ContextTokens: 4096})
			if tc.ok && (err != nil || peak <= 0 || m.GeneratedTokens != 128) {
				t.Fatalf("metrics=%+v peak=%d err=%v", m, peak, err)
			}
			if !tc.ok && err == nil {
				t.Fatal("accepted invalid measurement")
			}
		})
	}
}

func TestNativeLlamaBenchRequiresExactCPUContext(t *testing.T) {
	raw := []byte(`[{"model_filename":"/proc/self/fd/4","n_prompt":0,"n_gen":128,"n_depth":3968,"n_gpu_layers":0,"no_kv_offload":true,"avg_ns":10000000,"samples_ns":[10000000]}]`)
	m, err := parseLlamaBench(raw, 4096, 128)
	if err != nil || m.GeneratedTokens != 128 || m.InferenceSeconds != 0.01 || len(m.RawResult) == 0 {
		t.Fatalf("%+v %v", m, err)
	}
	for _, changed := range [][]byte{
		bytes.Replace(raw, []byte(`"n_gpu_layers":0`), []byte(`"n_gpu_layers":99`), 1),
		bytes.Replace(raw, []byte(`"n_depth":3968`), []byte(`"n_depth":0`), 1),
		bytes.Replace(raw, []byte(`"no_kv_offload":true`), []byte(`"no_kv_offload":false`), 1),
		bytes.Replace(raw, []byte(`"n_gpu_layers":0,`), nil, 1),
	} {
		if _, err := parseLlamaBench(changed, 4096, 128); err == nil {
			t.Fatal("accepted different benchmark domain")
		}
	}
}

func TestRunnerProducesThreeHashBoundMeasurementArtifacts(t *testing.T) {
	model := adapterFixture(t, "public fixture content\n")
	adapter := adapterFixture(t, "sleep 0.05\nprintf '%s' '[{\"model_filename\":\"/proc/self/fd/4\",\"n_prompt\":0,\"n_gen\":128,\"n_depth\":3968,\"n_gpu_layers\":0,\"no_kv_offload\":true,\"avg_ns\":10000000,\"samples_ns\":[10000000]}]'\n")
	modelContent, _ := os.ReadFile(model.Name())
	runtimeContent, _ := os.ReadFile(adapter.Name())
	dir := t.TempDir()
	var output, diag bytes.Buffer
	code := runMeasure([]string{"--model-file", model.Name(), "--runtime-file", adapter.Name(), "--model-sha256", sum(string(modelContent)), "--runtime-sha256", sum(string(runtimeContent)), "--phase", "protected", "--artifact-dir", dir}, &output, &diag, func() Binding { return Binding{BootID: "boot", Hardware: "testboard", Firmware: "v1"} })
	if code != 0 {
		t.Fatalf("code=%d: %s", code, diag.String())
	}
	var samples []Sample
	if err := json.Unmarshal(output.Bytes(), &samples); err != nil {
		t.Fatal(err)
	}
	if len(samples) != 3 {
		t.Fatal("missing repeated loads")
	}
	for _, s := range samples {
		if !s.Protected || s.Binding.Backend != "cpu" || s.PeakBytes <= 0 || s.TokensPerSecond != 12800 {
			t.Fatalf("invalid sample: %+v", s)
		}
	}
	if err := VerifyArtifacts(Report{Samples: samples}, dir); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerAcceptsFirmwareOverrideOnDeviceTreeHosts(t *testing.T) {
	model := adapterFixture(t, "public fixture content\n")
	adapter := adapterFixture(t, "sleep 0.05\nprintf '%s' '[{\"model_filename\":\"/proc/self/fd/4\",\"n_prompt\":0,\"n_gen\":128,\"n_depth\":3968,\"n_gpu_layers\":0,\"no_kv_offload\":true,\"avg_ns\":10000000,\"samples_ns\":[10000000]}]'\n")
	modelContent, _ := os.ReadFile(model.Name())
	runtimeContent, _ := os.ReadFile(adapter.Name())
	args := []string{"--model-file", model.Name(), "--runtime-file", adapter.Name(), "--model-sha256", sum(string(modelContent)), "--runtime-sha256", sum(string(runtimeContent)), "--phase", "baseline", "--artifact-dir", t.TempDir()}
	observe := func() Binding { return Binding{BootID: "boot", Hardware: "testboard"} }
	var output, diag bytes.Buffer
	if code := runMeasure(args, &output, &diag, observe); code == 0 {
		t.Fatal("accepted missing firmware identity")
	}
	output.Reset()
	diag.Reset()
	if code := runMeasure(append(args, "--firmware-id", "r36.4"), &output, &diag, observe); code != 0 {
		t.Fatalf("code=%d: %s", code, diag.String())
	}
	var samples []Sample
	if err := json.Unmarshal(output.Bytes(), &samples); err != nil || len(samples) != 3 || samples[0].Binding.Firmware != "r36.4" {
		t.Fatalf("%v %+v", err, samples)
	}
}
