package installer

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type ResourceReport struct {
	SchemaVersion int `json:"schema_version"`
	MemoryMode string `json:"memory_mode"`
	ModelDirectory string `json:"model_directory"`
	Resources Resources `json:"resources"`
	Admission Admission `json:"admission"`
	Scope string `json:"scope"`
}

type resourceProbe func(context.Context, string, string, int64) (Resources, error)

// RunResourceCheck is read-only: no enrollment, package installation, or writes.
func RunResourceCheck(ctx context.Context, args []string, out, diagnostic io.Writer) int {
	return runResourceCheck(ctx, args, out, diagnostic, probeResources)
}

func runResourceCheck(ctx context.Context, args []string, out, diagnostic io.Writer, probe resourceProbe) int {
	flags := flag.NewFlagSet("check-resources", flag.ContinueOnError)
	flags.SetOutput(diagnostic)
	mode := flags.String("memory-mode", "", "required: uma or discrete; describes the selected accelerator")
	directory := flags.String("model-dir", "", "required: existing model-cache directory on the intended filesystem")
	allocation := flags.Int64("uma-gib", 0, "optional explicit UMA diagnostic budget in GiB; zero checks installation minimum only")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp { return 0 }
		return 2
	}
	if flags.NArg() != 0 || (*mode != "uma" && *mode != "discrete") || *directory == "" ||
		(*mode == "uma" && (*allocation < 0 || *allocation > (1<<63-1)/GiB)) ||
		(*mode == "discrete" && *allocation != 0) {
		fmt.Fprintln(diagnostic, "require --model-dir and --memory-mode uma or discrete; optional --uma-gib must be positive and UMA-only")
		return 2
	}
	path, err := filepath.Abs(*directory)
	if err != nil {
		fmt.Fprintln(diagnostic, "cannot resolve model directory")
		return 2
	}
	resources, probeErr := probe(ctx, path, *mode, *allocation*GiB)
	admission := CheckResources(resources)
	if probeErr != nil {
		admission.Eligible = false
		admission.Reasons = append(admission.Reasons, probeErr.Error())
	}
	report := ResourceReport{1, *mode, path, resources, admission,
		"resource capacity only; GPU allocation, architecture support, AIuditor connectivity, certification and selling readiness remain unverified"}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintln(diagnostic, "cannot write resource report")
		return 1
	}
	if !admission.Eligible { return 1 }
	return 0
}

func parseMemory(text string) (int64, int64, error) {
	values := map[string]int64{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || (fields[0] != "MemTotal:" && fields[0] != "MemAvailable:") { continue }
		if len(fields) != 3 || fields[2] != "kB" { return 0, 0, fmt.Errorf("invalid RAM measurement") }
		n, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || n <= 0 || n > (1<<63-1)/1024 { return 0, 0, fmt.Errorf("invalid RAM measurement") }
		if _, exists := values[fields[0]]; exists { return 0, 0, fmt.Errorf("duplicate RAM measurement") }
		values[fields[0]] = n*1024
	}
	total, available := values["MemTotal:"], values["MemAvailable:"]
	if total == 0 || available == 0 || available > total {
		return 0, 0, fmt.Errorf("cannot measure MemTotal and MemAvailable; inspect /proc/meminfo")
	}
	return total, available, nil
}

func sumMemory(text string, unit int64) (int64, error) {
	var total int64
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		n, err := strconv.ParseInt(strings.TrimSpace(line), 10, 64)
		if err != nil || n <= 0 || n > ((1<<63-1)-total)/unit {
			return 0, fmt.Errorf("invalid or unknown GPU memory measurement")
		}
		total += n*unit
	}
	return total, nil
}

func discreteMemory(ctx context.Context) (int64, error) {
	// Query installed capacity, not free VRAM. This gate does not establish
	// that a runtime can distribute a model across all these devices.
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	data, err := exec.CommandContext(probeCtx, "nvidia-smi", "--query-gpu=memory.total", "--format=csv,noheader,nounits").Output()
	if err == nil { return sumMemory(string(data), 1<<20) }
	paths, err := filepath.Glob("/sys/class/drm/card[0-9]*/device/mem_info_vram_total")
	if err != nil { return 0, err }
	var amounts []string
	for _, path := range paths {
		vendor, err := os.ReadFile(filepath.Join(filepath.Dir(path), "vendor"))
		if err != nil || strings.TrimSpace(string(vendor)) != "0x1002" { continue }
		value, err := os.ReadFile(path)
		if err != nil { return 0, fmt.Errorf("cannot read AMD VRAM; check the GPU driver") }
		amounts = append(amounts, strings.TrimSpace(string(value)))
	}
	if len(amounts) == 0 {
		return 0, fmt.Errorf("cannot measure discrete VRAM; check NVIDIA/AMD drivers, or select uma for a unified-memory accelerator")
	}
	return sumMemory(strings.Join(amounts, "\n"), 1)
}
