package protection

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func local(path string) string {
	b, _ := os.ReadFile(path)
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " "))
}

func Observe() Binding {
	hardware := strings.TrimSpace(local("/sys/class/dmi/id/sys_vendor") + " " + local("/sys/class/dmi/id/product_name"))
	if hardware == "" {
		hardware = local("/proc/device-tree/model")
	}
	return Binding{BootID: local("/proc/sys/kernel/random/boot_id"), Hardware: hardware, Firmware: local("/sys/class/dmi/id/bios_version")}
}

// Run dispatches read-only probes, explicit lab measurements or evidence validation.
// The default command never changes firmware or starts a workload. It emits a lab evidence
// template or evaluates a completed dossier against current local observations.
func Run(args []string, out, diagnostic io.Writer) int {
	if len(args) > 0 && args[0] == "probe" {
		return RunProbe(args[1:], out, diagnostic)
	}
	if len(args) > 0 && args[0] == "measure" {
		return RunMeasure(args[1:], out, diagnostic)
	}
	f := flag.NewFlagSet("protection", flag.ContinueOnError)
	f.SetOutput(diagnostic)
	mode := f.String("modes", "physical_memory", "comma-separated physical_memory,confidential_execution")
	backend := f.String("backend", "cpu", "cpu, uma or discrete_gpu")
	model := f.String("model-sha256", "", "pinned model content digest")
	runtime := f.String("runtime-sha256", "", "pinned runtime content digest")
	context := f.Int("context-tokens", 4096, "qualified model context size")
	report := f.String("report", "", "completed evidence JSON; omit to emit a template")
	artifacts := f.String("artifact-dir", "", "directory of evidence files named by SHA-256")
	firmware := f.String("firmware-id", "", "operator-observed firmware identity when the platform does not expose DMI; not verification")
	if err := f.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 || !digest(*model) || !digest(*runtime) || *context <= 0 {
		fmt.Fprintln(diagnostic, "require valid model/runtime SHA-256 pins and positive context size")
		return 2
	}
	var modes []Mode
	for _, value := range strings.Split(*mode, ",") {
		modes = append(modes, Mode(strings.TrimSpace(value)))
	}
	b := Observe()
	b.ModelSHA256 = *model
	b.RuntimeSHA256 = *runtime
	b.Backend = *backend
	b.ContextTokens = *context
	if b.Firmware == "" {
		b.Firmware = *firmware
	}
	template, err := Template(modes, b, time.Now())
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return 2
	}
	if *report == "" {
		if err := json.NewEncoder(out).Encode(template); err != nil {
			fmt.Fprintln(diagnostic, err)
			return 1
		}
		return 0
	}
	before, err := os.Lstat(*report)
	if err != nil || !before.Mode().IsRegular() || before.Size() > 1<<20 {
		fmt.Fprintln(diagnostic, "report must be a regular file of at most 1 MiB")
		return 2
	}
	file, err := os.Open(*report)
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) || info.Size() > 1<<20 {
		fmt.Fprintln(diagnostic, "report must be a regular file of at most 1 MiB")
		return 2
	}
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var r Report
	if err = decoder.Decode(&r); err != nil {
		fmt.Fprintln(diagnostic, err)
		return 2
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		fmt.Fprintln(diagnostic, "trailing report data")
		return 2
	}
	// The caller's requested modes cannot be dropped by an edited dossier.
	if !sameModes(modes, r.Modes) {
		fmt.Fprintln(diagnostic, "report modes differ from requested modes")
		return 2
	}
	result := Evaluate(r, b, time.Now())
	if *artifacts == "" {
		result.Blockers = append(result.Blockers, "require --artifact-dir to verify evidence contents")
		result.ReviewReady = false
	} else if err := VerifyArtifacts(r, *artifacts); err != nil {
		result.Blockers = append(result.Blockers, err.Error())
		result.ReviewReady = false
	}
	if err = json.NewEncoder(out).Encode(result); err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	if !result.ReviewReady {
		return 1
	}
	return 0
}

func sameModes(a, b []Mode) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[Mode]bool{}
	for _, m := range a {
		seen[m] = true
	}
	for _, m := range b {
		if !seen[m] {
			return false
		}
		delete(seen, m)
	}
	return len(seen) == 0
}
