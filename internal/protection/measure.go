//go:build linux

package protection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"
)

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 65536 {
		return 0, errors.New("adapter output exceeds 64 KiB")
	}
	return b.Buffer.Write(p)
}

func pinnedFile(path, pin string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("pin requires regular non-symlink file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		f.Close()
		return nil, errors.New("file changed while opening")
	}
	if err = checkPin(f, pin); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func checkPin(f *os.File, pin string) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != pin {
		return errors.New("content pin mismatch")
	}
	_, err := f.Seek(0, io.SeekStart)
	return err
}

func measure(ctx context.Context, executable, model *os.File, b Binding) (Measurement, int64, time.Duration, error) {
	return measureProtocol(ctx, executable, model, b, "adapter", 128)
}

func measureProtocol(ctx context.Context, executable, model *os.File, b Binding, protocol string, generated int) (Measurement, int64, time.Duration, error) {
	request, _ := json.Marshal(struct {
		Schema          string  `json:"schema"`
		ModelPath       string  `json:"model_path"`
		GeneratedTokens int     `json:"generated_tokens"`
		Binding         Binding `json:"binding"`
	}{"hypermesh-measure-request-v1", "/proc/self/fd/4", generated, b})
	cmd := exec.CommandContext(ctx, "/proc/self/fd/3")
	if protocol == "llama-bench" {
		cmd.Args = append(cmd.Args, "-m", "/proc/self/fd/4", "-p", "0", "-n", strconv.Itoa(generated), "-d", strconv.Itoa(b.ContextTokens-generated), "-ngl", "0", "-nkvo", "1", "-nopo", "1", "-r", "1", "-o", "json")
	}
	cmd.ExtraFiles = []*os.File{executable, model}
	cmd.Stdin = bytes.NewReader(request)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "HOME=/nonexistent"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	output := &boundedOutput{}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	start := time.Now()
	err := cmd.Run()
	if cmd.Process != nil {
		// A trusted adapter must not leave background descendants after a load.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	elapsed := time.Since(start)
	if err != nil {
		return Measurement{}, 0, elapsed, err
	}
	var m Measurement
	if protocol == "llama-bench" {
		m, err = parseLlamaBench(output.Bytes(), b.ContextTokens, generated)
	} else {
		decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
		decoder.DisallowUnknownFields()
		var response struct {
			GeneratedTokens  int64   `json:"generated_tokens"`
			InferenceSeconds float64 `json:"inference_seconds"`
		}
		err = decoder.Decode(&response)
		m.GeneratedTokens = response.GeneratedTokens
		m.InferenceSeconds = response.InferenceSeconds
		var extra any
		if err == nil && decoder.Decode(&extra) != io.EOF {
			err = errors.New("trailing adapter output")
		}
	}
	if err == nil && m.GeneratedTokens != int64(generated) {
		err = errors.New("generated token count differs from request")
	}
	if err != nil || m.GeneratedTokens <= 0 || m.InferenceSeconds <= 0 || math.IsNaN(m.InferenceSeconds) || math.IsInf(m.InferenceSeconds, 0) || m.InferenceSeconds > elapsed.Seconds() {
		return m, 0, elapsed, errors.New("invalid workload measurement")
	}
	usage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage)
	if !ok || usage.Maxrss <= 0 {
		return m, 0, elapsed, errors.New("missing measured peak RSS")
	}
	return m, usage.Maxrss * 1024, elapsed, nil
}

func RunMeasure(args []string, out, diagnostic io.Writer) int {
	return runMeasure(args, out, diagnostic, Observe)
}

func runMeasure(args []string, out, diagnostic io.Writer, observe func() Binding) int {
	f := flag.NewFlagSet("protection measure", flag.ContinueOnError)
	f.SetOutput(diagnostic)
	modelPath := f.String("model-file", "", "public test model; regular file")
	runtimePath := f.String("runtime-file", "", "pinned static llama-bench or trusted adapter executable")
	protocol := f.String("protocol", "llama-bench", "llama-bench or adapter")
	generated := f.Int("generated-tokens", 128, "decode tokens within requested context")
	modelPin := f.String("model-sha256", "", "model content pin")
	runtimePin := f.String("runtime-sha256", "", "adapter executable content pin")
	phase := f.String("phase", "", "baseline or protected; a label, not enablement proof")
	count := f.Int("samples", 3, "independent loads, between 3 and 30")
	contextTokens := f.Int("context-tokens", 4096, "requested model context size")
	timeout := f.Duration("timeout", 10*time.Minute, "maximum time for each sample, at most 1 hour")
	dir := f.String("artifact-dir", "", "private evidence directory")
	firmware := f.String("firmware-id", "", "operator-observed firmware identity when the platform does not expose DMI; not verification")
	if err := f.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if runtime.GOOS != "linux" || (*protocol != "llama-bench" && *protocol != "adapter") || *generated <= 0 || *contextTokens < *generated || f.NArg() != 0 || !digest(*modelPin) || !digest(*runtimePin) || (*phase != "baseline" && *phase != "protected") || *count < 3 || *count > 30 || *contextTokens <= 0 || *timeout <= 0 || *timeout > time.Hour || *dir == "" {
		fmt.Fprintln(diagnostic, "require Linux, valid pins, phase, 3–30 samples, positive context/timeout and artifact directory")
		return 2
	}
	model, err := pinnedFile(*modelPath, *modelPin)
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	defer model.Close()
	adapter, err := pinnedFile(*runtimePath, *runtimePin)
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	defer adapter.Close()
	if err = os.MkdirAll(*dir, 0700); err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	observeBound := func() Binding {
		current := observe()
		if current.Firmware == "" {
			current.Firmware = *firmware
		}
		return current
	}
	b := observeBound()
	b.ModelSHA256 = *modelPin
	b.RuntimeSHA256 = *runtimePin
	b.Backend = "cpu"
	b.ContextTokens = *contextTokens
	if b.BootID == "" || b.Hardware == "" || b.Firmware == "" {
		fmt.Fprintln(diagnostic, "require observable current boot/hardware/firmware identity")
		return 1
	}
	// Workloads run in their own process group, so Ctrl-C and SIGTERM must
	// cancel them through the context or they would outlive this command.
	interrupted, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	samples := []Sample{}
	for i := 0; i < *count; i++ {
		if err = checkPin(adapter, *runtimePin); err == nil {
			err = checkPin(model, *modelPin)
		}
		if err != nil {
			fmt.Fprintln(diagnostic, err)
			return 1
		}
		captured := time.Now().UTC()
		ctx, cancel := context.WithTimeout(interrupted, *timeout)
		metrics, peak, elapsed, runErr := measureProtocol(ctx, adapter, model, b, *protocol, *generated)
		cancel()
		if runErr != nil {
			fmt.Fprintln(diagnostic, runErr)
			return 1
		}
		if err = checkPin(adapter, *runtimePin); err == nil {
			err = checkPin(model, *modelPin)
		}
		if err != nil {
			fmt.Fprintln(diagnostic, err)
			return 1
		}
		current := observeBound()
		if current.BootID != b.BootID || current.Hardware != b.Hardware || current.Firmware != b.Firmware {
			fmt.Fprintln(diagnostic, "platform drift during measurement")
			return 1
		}
		id := fmt.Sprintf("%s-%d-%d", *phase, captured.UnixNano(), i)
		payload, _ := json.Marshal(SamplePayload{b, metrics, peak, "linux_process_rusage_maxrss", elapsed.Seconds(), *phase == "protected"})
		content, _ := json.Marshal(Artifact{Schema: ArtifactSchema, Kind: "sample", ID: id, CapturedAt: captured, Payload: payload})
		hash := sha256.Sum256(content)
		ref := hex.EncodeToString(hash[:])
		file, err := os.OpenFile(filepath.Join(*dir, ref), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			fmt.Fprintln(diagnostic, err)
			return 1
		}
		_, err = file.Write(content)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			fmt.Fprintln(diagnostic, "artifact write failed")
			return 1
		}
		samples = append(samples, Sample{ID: id, Binding: b, Protected: *phase == "protected", CapturedAt: captured, TokensPerSecond: float64(metrics.GeneratedTokens) / metrics.InferenceSeconds, PeakBytes: peak, ArtifactSHA256: ref})
	}
	if err = json.NewEncoder(out).Encode(samples); err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	return 0
}

// llama-bench documents JSON avg_ns and generation at prefilled context depth.
// Preserve its complete raw JSON in the content-addressed artifact.
func parseLlamaBench(raw []byte, contextTokens, generated int) (Measurement, error) {
	var objects []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &objects); err != nil || len(objects) != 1 {
		return Measurement{}, errors.New("require one benchmark object")
	}
	for _, key := range []string{"model_filename", "n_prompt", "n_gen", "n_depth", "n_gpu_layers", "no_kv_offload", "no_op_offload", "avg_ns", "samples_ns"} {
		if value, ok := objects[0][key]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return Measurement{}, errors.New("missing benchmark identity or metrics")
		}
	}
	var rows []struct {
		ModelFilename string    `json:"model_filename"`
		Prompt        int       `json:"n_prompt"`
		Generated     int       `json:"n_gen"`
		Depth         int       `json:"n_depth"`
		GPULayers     int       `json:"n_gpu_layers"`
		NoKVOffload   bool      `json:"no_kv_offload"`
		NoOpOffload   flexBool  `json:"no_op_offload"`
		AverageNS     float64   `json:"avg_ns"`
		SamplesNS     []float64 `json:"samples_ns"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return Measurement{}, err
	}
	if len(rows) != 1 {
		return Measurement{}, errors.New("require one CPU decode benchmark")
	}
	r := rows[0]
	if r.ModelFilename != "/proc/self/fd/4" || r.Prompt != 0 || r.Generated != generated || r.Depth != contextTokens-generated || r.GPULayers != 0 || !r.NoKVOffload || !bool(r.NoOpOffload) || r.AverageNS <= 0 || len(r.SamplesNS) != 1 || r.SamplesNS[0] != r.AverageNS {
		return Measurement{}, errors.New("benchmark differs from pinned CPU/context request")
	}
	return Measurement{GeneratedTokens: int64(generated), InferenceSeconds: r.AverageNS / 1e9, RawResult: append(json.RawMessage(nil), raw...)}, nil
}

// flexBool accepts the boolean or 0/1 integer forms llama-bench has used.
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	switch string(bytes.TrimSpace(data)) {
	case "true", "1":
		*b = true
	case "false", "0":
		*b = false
	default:
		return errors.New("invalid boolean")
	}
	return nil
}
