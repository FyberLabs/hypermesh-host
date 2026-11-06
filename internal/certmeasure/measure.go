// Package certmeasure collects model measurements independently of enrollment,
// class baselines and certificate issuance. It never changes host eligibility.
package certmeasure

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/hardware"
	"github.com/FyberLabs/hypermesh-host/internal/telem"
)

type Sample struct {
	CapturedAt      time.Time       `json:"captured_at"`
	ElapsedMS       int64           `json:"elapsed_ms"`
	TokensPerSecond float64         `json:"tokens_per_second"`
	Raw             json.RawMessage `json:"raw"`
}
type Report struct {
	RuntimeImage       string    `json:"runtime_image,omitempty"`
	EnvironmentHash    string    `json:"environment_hash"`
	Schema             string    `json:"schema"`
	RunID              string    `json:"run_id"`
	StartedAt          time.Time `json:"started_at"`
	CompletedAt        time.Time `json:"completed_at,omitempty"`
	OS                 string    `json:"os"`
	Arch               string    `json:"arch"`
	Hardware           string    `json:"hardware_fingerprint"`
	Backend            string    `json:"backend"`
	ModelSHA256        string    `json:"model_sha256"`
	RuntimeSHA256      string    `json:"runtime_sha256"`
	ContextTokens      int       `json:"context_tokens"`
	GeneratedTokens    int       `json:"generated_tokens"`
	Samples            []Sample  `json:"samples"`
	MeasurementState   string    `json:"measurement_state"`
	FailureCode        string    `json:"failure_code,omitempty"`
	CertificationState string    `json:"certification_state"`
	PhysicalAcceptance bool      `json:"physical_acceptance"`
}
type options struct {
	model, binary, modelPin, runtimePin, backend, dir, state, image string
	samples, context, generated                                     int
	timeout                                                         time.Duration
}
type dependencies struct {
	inventory func(context.Context) string
	detect    func(context.Context) hardware.Profile
	execute   func(context.Context, string, []string) ([]byte, time.Duration, error)
}

// Run retains a report even when inputs are missing or a sample fails. No
// network credential or trusted reference is needed to collect local evidence.
func Run(ctx context.Context, args []string, out, diagnostic io.Writer) int {
	return run(ctx, args, out, diagnostic, dependencies{inventory: func(ctx context.Context) string {
		snapshot, err := telem.Default().Collect(ctx)
		if err != nil {
			return ""
		}
		return snapshot.EnvironmentHash
	}, detect: hardware.Detect, execute: execute})
}
func run(ctx context.Context, args []string, out, diagnostic io.Writer, d dependencies) int {
	var o options
	f := flag.NewFlagSet("measure-certification", flag.ContinueOnError)
	f.SetOutput(diagnostic)
	f.StringVar(&o.model, "model-file", "", "local GGUF; never downloaded implicitly")
	f.StringVar(&o.image, "runtime-image", "", "optional pinned NVIDIA OCI image; Linux only, uses the existing isolated workload runner")
	f.StringVar(&o.binary, "runtime-file", "", "local llama-bench executable")
	f.StringVar(&o.modelPin, "model-sha256", "", "expected model SHA256 (hex)")
	f.StringVar(&o.runtimePin, "runtime-sha256", "", "expected executable SHA256 (hex)")
	f.StringVar(&o.backend, "backend", "auto", "auto, cpu, metal, cuda, hip or vulkan")
	f.StringVar(&o.dir, "artifact-dir", "", "private output directory; each invocation creates a fresh run")
	f.StringVar(&o.state, "state-dir", "/var/lib/hypermesh", "host state directory; an active lease prevents interference")
	f.IntVar(&o.samples, "samples", 3, "independent model loads (1 to 30)")
	f.IntVar(&o.context, "context-tokens", 512, "prefilled context plus generated tokens")
	f.IntVar(&o.generated, "generated-tokens", 32, "timed decode tokens")
	f.DurationVar(&o.timeout, "timeout", 10*time.Minute, "per-sample deadline, at most one hour")
	if err := f.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 || o.dir == "" || o.samples < 1 || o.samples > 30 || o.generated < 1 || o.context < o.generated || o.context > 131072 || o.timeout <= 0 || o.timeout > time.Hour {
		fmt.Fprintln(diagnostic, "require artifact directory, 1–30 samples, valid context and timeout")
		return 2
	}
	if err := os.MkdirAll(o.dir, 0700); err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	id := hex.EncodeToString(nonce[:])
	dir := filepath.Join(o.dir, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	profile := d.detect(ctx)
	environment := d.inventory(ctx)
	if o.backend == "auto" {
		o.backend = profile.Backend
	}
	report := Report{RuntimeImage: o.image, EnvironmentHash: environment, Schema: "hypermesh-certification-measurements-v1", RunID: id, StartedAt: time.Now().UTC(), OS: runtime.GOOS, Arch: runtime.GOARCH, Hardware: profile.Fingerprint, Backend: o.backend, ModelSHA256: o.modelPin, RuntimeSHA256: o.runtimePin, ContextTokens: o.context, GeneratedTokens: o.generated, Samples: []Sample{}, MeasurementState: "running", CertificationState: "not_evaluated"}
	save := func() error {
		b, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		temp := filepath.Join(dir, "report.tmp")
		if err = os.WriteFile(temp, b, 0600); err != nil {
			return err
		}
		return os.Rename(temp, filepath.Join(dir, "report.json"))
	}
	finish := func(code string) int {
		report.CompletedAt = time.Now().UTC()
		report.FailureCode = code
		report.MeasurementState = "passed"
		result := 0
		if code != "" {
			report.MeasurementState = "failed"
			result = 1
		}
		if err := save(); err != nil {
			fmt.Fprintln(diagnostic, err)
			return 1
		}
		fmt.Fprintln(out, filepath.Join(dir, "report.json"))
		return result
	}
	if o.image != "" {
		report.GeneratedTokens = 0
	}
	if err := save(); err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	if !supportedBackend(o.backend) || (o.backend == "metal" && (runtime.GOOS != "darwin" || runtime.GOARCH != "arm64")) {
		return finish("unsupported_backend")
	}
	model, err := pin(o.model, o.modelPin, false)
	if err != nil {
		return finish("model_unavailable_or_pin_mismatch")
	}
	defer model.Close()
	var binary *os.File
	if o.image == "" {
		binary, err = pin(o.binary, o.runtimePin, true)
		if err != nil {
			return finish("runtime_unavailable_or_pin_mismatch")
		}
		defer binary.Close()
	} else if err = validateContainer(o); err != nil {
		return finish("unsupported_container_request")
	}
	modelInfo, _ := model.Stat()
	if profile.ModelBudgetBytes <= 0 || modelInfo.Size() > profile.ModelBudgetBytes {
		return finish("insufficient_observed_memory")
	}
	for i := 0; i < o.samples; i++ {
		if err := ctx.Err(); err != nil {
			return finish("interrupted")
		}
		if _, err := os.Stat(filepath.Join(o.state, "lease.json")); err == nil {
			return finish("active_lease")
		} else if !errors.Is(err, os.ErrNotExist) {
			return finish("lease_state_unavailable")
		}
		if !unchanged(model, o.model, o.modelPin) || (binary != nil && !unchanged(binary, o.binary, o.runtimePin)) {
			return finish("input_drift")
		}
		captured := time.Now().UTC()
		sampleCtx, cancel := context.WithTimeout(ctx, o.timeout)
		var raw []byte
		var elapsed time.Duration
		var rate float64
		if o.image != "" {
			raw, elapsed, rate, err = containerSample(sampleCtx, o, id, i)
		} else {
			raw, elapsed, err = d.execute(sampleCtx, o.binary, arguments(o))
		}
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return finish("interrupted")
			}
			return finish("workload_failed_or_timeout")
		}
		if writeErr := os.WriteFile(filepath.Join(dir, fmt.Sprintf("sample-%02d.stdout", i+1)), raw, 0600); writeErr != nil {
			fmt.Fprintln(diagnostic, writeErr)
			return 1
		}
		if o.image == "" {
			rate, err = parse(raw, o, elapsed)
		}
		if err != nil {
			return finish("invalid_measurement")
		}
		if !unchanged(model, o.model, o.modelPin) || (binary != nil && !unchanged(binary, o.binary, o.runtimePin)) || d.detect(ctx).Fingerprint != profile.Fingerprint || environment == "" || d.inventory(ctx) != environment {
			return finish("input_or_hardware_drift")
		}
		report.Samples = append(report.Samples, Sample{captured, elapsed.Milliseconds(), rate, append(json.RawMessage(nil), raw...)})
		if err = save(); err != nil {
			fmt.Fprintln(diagnostic, err)
			return 1
		}
	}
	return finish("")
}
func supportedBackend(s string) bool {
	return s == "cpu" || s == "metal" || s == "cuda" || s == "hip" || s == "vulkan"
}
func arguments(o options) []string {
	layers := "99"
	if o.backend == "cpu" {
		layers = "0"
	}
	args := []string{"-m", o.model, "-p", "0", "-n", strconv.Itoa(o.generated), "-d", strconv.Itoa(o.context - o.generated), "-ngl", layers, "-r", "1", "-o", "json"}
	if o.backend == "cpu" {
		args = append(args, "-nkvo", "1", "-nopo", "1")
	}
	return args
}
func pin(path, want string, executable bool) (*os.File, error) {
	decoded, err := hex.DecodeString(want)
	if err != nil || len(decoded) != 32 || want != strings.ToLower(want) {
		return nil, errors.New("invalid SHA256")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || executable && info.Mode().Perm()&0111 == 0 {
		return nil, errors.New("require regular pinned file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if !unchanged(f, path, want) {
		f.Close()
		return nil, errors.New("file changed or pin mismatch")
	}
	return f, nil
}
func unchanged(f *os.File, path, want string) bool {
	current, err := os.Lstat(path)
	if err != nil || !current.Mode().IsRegular() {
		return false
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(current, opened) {
		return false
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return false
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == want
}
func parse(raw []byte, o options, elapsed time.Duration) (float64, error) {
	var rows []struct {
		Model     string    `json:"model_filename"`
		Prompt    *int      `json:"n_prompt"`
		Generated *int      `json:"n_gen"`
		Depth     *int      `json:"n_depth"`
		Layers    *int      `json:"n_gpu_layers"`
		Backends  string    `json:"backends"`
		Average   float64   `json:"avg_ns"`
		Samples   []float64 `json:"samples_ns"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) != 1 {
		return 0, errors.New("require one benchmark")
	}
	r := rows[0]
	layers := 99
	if o.backend == "cpu" {
		layers = 0
	}
	if r.Model != o.model || r.Prompt == nil || *r.Prompt != 0 || r.Generated == nil || *r.Generated != o.generated || r.Depth == nil || *r.Depth != o.context-o.generated || r.Layers == nil || *r.Layers != layers || r.Average <= 0 || math.IsNaN(r.Average) || math.IsInf(r.Average, 0) || len(r.Samples) != 1 || r.Samples[0] != r.Average || r.Average > float64(elapsed.Nanoseconds()) {
		return 0, errors.New("benchmark differs from requested load")
	}
	backend := o.backend
	if backend == "metal" {
		backend = "mtl"
	}
	if backend == "hip" {
		backend = "rocm"
	}
	if o.backend != "cpu" && !(strings.Contains(strings.ToLower(r.Backends), backend) || (o.backend == "metal" && strings.Contains(strings.ToLower(r.Backends), "metal"))) {
		return 0, errors.New("accelerator backend not reported")
	}
	return float64(o.generated) * 1e9 / r.Average, nil
}
