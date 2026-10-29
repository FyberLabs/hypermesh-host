package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Supported AGX configuration. CUDA maintenance patches are eligible for
// requalification; preflight never grants or renews certification.
const (
	pinArch          = "aarch64"
	pinL4T           = "R39.2.1"
	pinKernel        = "6.8"
	pinUbuntu        = "24.04.5"
	pinCUDA          = "13.2.1"
	pinTarget        = "multi-user.target"
	pinNvpmodelID    = 2
	pinNvpmodelWatts = "30W"
	clocksStopped    = "stopped"
	clocksRunning    = "running"
)

// Facts is one reading of the box. Tests fill this in. CollectHost reads
// the live files and commands.
type Facts struct {
	MemoryBytes   int64
	Arch          string
	L4T           string
	Kernel        string
	Ubuntu        string
	CUDA          string
	DefaultTarget string
	// NvpmodelID is -1 when nvpmodel did not return an id.
	NvpmodelID    int
	NvpmodelWatts string
	// JetsonClocks is "stopped", "running", or "" when it cannot be proven.
	JetsonClocks  string
	Tegrastats    bool
	Docker        bool
	NvidiaRuntime bool
	WG            bool
}

// CheckAGX returns an error listing every pin that does not match.
// The command still stops before configure.
func CheckAGX(f Facts) error {
	var bad []string
	if f.Arch != pinArch {
		bad = append(bad, fmt.Sprintf("arch: got %q, want %s", f.Arch, pinArch))
	}
	if f.MemoryBytes < 56<<30 || f.MemoryBytes > 66<<30 {
		bad = append(bad, fmt.Sprintf("AGX memory: got %d bytes, want 56–66 GiB for 64 GB module", f.MemoryBytes))
	}
	if f.L4T != pinL4T {
		bad = append(bad, fmt.Sprintf("l4t: got %q, want %s", f.L4T, pinL4T))
	}
	if !kernelIs(f.Kernel, pinKernel) {
		bad = append(bad, fmt.Sprintf("kernel: got %q, want %s", f.Kernel, pinKernel))
	}
	if !ubuntuIs(f.Ubuntu, pinUbuntu) {
		bad = append(bad, fmt.Sprintf("ubuntu: got %q, want %s", f.Ubuntu, pinUbuntu))
	}
	if !cudaEligible(f.CUDA) {
		bad = append(bad, fmt.Sprintf("cuda: got %q, want 13.2 patch >= 1 (requires current certification)", f.CUDA))
	}
	if f.DefaultTarget != pinTarget {
		bad = append(bad, fmt.Sprintf("default target: got %q, want %s (headless)", f.DefaultTarget, pinTarget))
	}
	if f.NvpmodelID != pinNvpmodelID || !strings.EqualFold(f.NvpmodelWatts, pinNvpmodelWatts) {
		bad = append(bad, fmt.Sprintf("nvpmodel: got id %d %q, want %d %s", f.NvpmodelID, f.NvpmodelWatts, pinNvpmodelID, pinNvpmodelWatts))
	}
	if f.JetsonClocks != clocksStopped {
		bad = append(bad, fmt.Sprintf("jetson_clocks: got %q, want %s", f.JetsonClocks, clocksStopped))
	}
	if !f.Tegrastats {
		bad = append(bad, "tegrastats: missing")
	}
	if !f.Docker {
		bad = append(bad, "docker: missing")
	}
	if !f.NvidiaRuntime {
		bad = append(bad, "nvidia container runtime: missing")
	}
	if !f.WG {
		bad = append(bad, "wg: missing")
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("agx preflight failed: %s", strings.Join(bad, "; "))
}

// kernelIs accepts 6.8 and 6.8.12-tegra. It rejects 6.18 and 6.80.
func ubuntuIs(got, want string) bool {
	got = strings.TrimSpace(got)
	if got == want {
		return true
	}
	rest := got
	for {
		i := strings.Index(rest, want)
		if i < 0 {
			return false
		}
		after := i + len(want)
		beforeOK := i == 0 || !isDigit(rune(rest[i-1]))
		afterOK := after == len(rest) || !isDigit(rune(rest[after]))
		if beforeOK && afterOK {
			return true
		}
		rest = rest[i+1:]
	}
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func kernelIs(got, want string) bool {
	major, minor, ok := kernelParts(got)
	if !ok {
		return false
	}
	wMajor, wMinor, ok := kernelParts(want)
	if !ok {
		return false
	}
	return major == wMajor && minor == wMinor
}

func kernelParts(v string) (string, string, bool) {
	v = strings.TrimSpace(v)
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return "", "", false
	}
	minor := parts[1]
	for i, r := range minor {
		if r < '0' || r > '9' {
			minor = minor[:i]
			break
		}
	}
	if parts[0] == "" || minor == "" {
		return "", "", false
	}
	return parts[0], minor, true
}

// Source reads host facts. Nil funcs use the real box.
type Source struct {
	ReadFile   func(string) ([]byte, error)
	LookPath   func(string) (string, error)
	Output     func(context.Context, string, ...string) ([]byte, error)
	Tegra      string
	OSRelease  string
	Kernel     string
	CUDAJSON   string
	CUDAText   string
	DockerJSON string
}

func (s Source) read(path string) ([]byte, error) {
	if s.ReadFile != nil {
		return s.ReadFile(path)
	}
	return os.ReadFile(path)
}

func (s Source) look(name string) bool {
	look := s.LookPath
	if look == nil {
		look = exec.LookPath
	}
	_, err := look(name)
	return err == nil
}

func (s Source) output(ctx context.Context, name string, args ...string) ([]byte, error) {
	if s.Output != nil {
		return s.Output(ctx, name, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func (s Source) path(got, def string) string {
	if strings.TrimSpace(got) != "" {
		return got
	}
	return def
}

// Collect reads the pins from the local box. Missing tools become empty
// facts; CheckAGX then fails closed.
func Collect(ctx context.Context, s Source) Facts {
	var f Facts
	if out, err := s.output(ctx, "uname", "-m"); err == nil {
		f.Arch = strings.TrimSpace(string(out))
	}
	if b, err := s.read(s.path(s.Tegra, "/etc/nv_tegra_release")); err == nil {
		f.L4T = parseL4T(string(b))
	}
	if b, err := s.read(s.path(s.Kernel, "/proc/sys/kernel/osrelease")); err == nil {
		f.Kernel = strings.TrimSpace(string(b))
	}
	if b, err := s.read(s.path(s.OSRelease, "/etc/os-release")); err == nil {
		f.Ubuntu = ubuntuPoint(string(b))
	}
	if b, err := s.read("/proc/meminfo"); err == nil {
		if m := regexp.MustCompile(`(?m)^MemTotal:\s+([0-9]+)\s+kB\s*$`).FindStringSubmatch(string(b)); m != nil {
			kb, err := strconv.ParseInt(m[1], 10, 64)
			if err == nil && kb <= (1<<63-1)/1024 {
				f.MemoryBytes = kb * 1024
			}
		}
	}
	f.CUDA = s.cuda(ctx)
	if out, err := s.output(ctx, "systemctl", "get-default"); err == nil {
		f.DefaultTarget = strings.TrimSpace(string(out))
	}
	f.NvpmodelID = -1
	if out, err := s.output(ctx, "nvpmodel", "-q"); err == nil {
		f.NvpmodelID, f.NvpmodelWatts = parseNvpmodel(string(out))
	}
	f.JetsonClocks = s.clocks(ctx)
	f.Tegrastats = s.look("tegrastats")
	f.Docker, f.NvidiaRuntime = s.dockerState(ctx)
	f.WG = s.look("wg")
	return f
}

// CollectHost reads the live AGX.
func CollectHost(ctx context.Context) Facts {
	return Collect(ctx, Source{})
}

func (s Source) cuda(ctx context.Context) string {
	if b, err := s.read(s.path(s.CUDAJSON, "/usr/local/cuda/version.json")); err == nil {
		if v := cudaFromJSON(b); v != "" {
			return v
		}
	}
	if b, err := s.read(s.path(s.CUDAText, "/usr/local/cuda/version.txt")); err == nil {
		if v := cudaFromText(string(b)); v != "" {
			return v
		}
	}
	return ""
}

func (s Source) dockerState(ctx context.Context) (bool, bool) {
	b, err := s.output(ctx, "docker", "--host=unix:///var/run/docker.sock", "info", "--format", "{{json .Runtimes}}")
	if err != nil {
		return false, false
	}
	var runtimes map[string]struct{ Path string }
	if json.Unmarshal(b, &runtimes) != nil || runtimes == nil {
		return false, false
	}
	return true, s.runtimeMatches(runtimes["nvidia"].Path)
}

func (s Source) runtimeMatches(configured string) bool {
	look := s.LookPath
	if look == nil {
		look = exec.LookPath
	}
	installed, err := look("nvidia-container-runtime")
	if err != nil || installed == "" {
		return false
	}
	if configured == "" {
		return false
	}
	resolved, err := look(configured)
	if err != nil || resolved == "" {
		return false
	}
	canonical := func(path string) string {
		if real, err := filepath.EvalSymlinks(path); err == nil {
			return real
		}
		return filepath.Clean(path)
	}
	return canonical(installed) == canonical(resolved)
}

// Clock state is read from control limits, not instantaneous frequency or
// invented --show status phrases. Missing controls cannot pass the preflight.
func (s Source) clocks(ctx context.Context) string {
	if _, err := s.output(ctx, "jetson_clocks", "--show"); err != nil {
		return ""
	}
	online, err := s.read("/sys/devices/system/cpu/online")
	if err != nil {
		return ""
	}
	readInt := func(path string) (int64, error) {
		b, err := s.read(path)
		if err != nil {
			return 0, err
		}
		return strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	}
	cpus := map[int]bool{}
	for _, part := range strings.Split(strings.TrimSpace(string(online)), ",") {
		ends := strings.Split(part, "-")
		if len(ends) > 2 {
			return ""
		}
		first, e1 := strconv.Atoi(ends[0])
		last, e2 := strconv.Atoi(ends[len(ends)-1])
		if e1 != nil || e2 != nil || first < 0 || last < first || last > 255 {
			return ""
		}
		for cpu := first; cpu <= last; cpu++ {
			cpus[cpu] = true
		}
	}
	locked := false
	checkLimits := func(base string) bool {
		lo, e1 := readInt(base + "min_freq")
		hi, e2 := readInt(base + "max_freq")
		if e1 != nil || e2 != nil || lo < 0 || hi <= 0 || lo > hi {
			return false
		}
		locked = locked || lo == hi
		return true
	}
	for cpu := range cpus {
		if !checkLimits(fmt.Sprintf("/sys/devices/system/cpu/cpu%d/cpufreq/scaling_", cpu)) {
			return ""
		}
	}
	if !checkLimits("/sys/devices/platform/17000000.gpu/devfreq/17000000.gpu/") {
		return ""
	}
	for _, path := range []string{"/sys/kernel/debug/bpmp/debug/clk/emc/mrq_rate_locked", "/sys/kernel/debug/bpmp/debug/bwmgr/bwmgr_halt"} {
		value, err := readInt(path)
		if err != nil || (value != 0 && value != 1) {
			return ""
		}
		locked = locked || value == 1
	}
	if locked {
		return clocksRunning
	}
	return clocksStopped
}

var (
	l4tRelease  = regexp.MustCompile(`R(\d+)`)
	l4tRevision = regexp.MustCompile(`REVISION:\s*([0-9.]+)`)
	cudaRelease = regexp.MustCompile(`(?i)release\s+([0-9]+\.[0-9]+\.[0-9]+)`)
	cudaVersion = regexp.MustCompile(`(?i)cuda version\s+([0-9]+\.[0-9]+\.[0-9]+)`)
	wattsRE     = regexp.MustCompile(`(?i)(\d+)\s*W`)
	intLine     = regexp.MustCompile(`^-?\d+$`)
)

func parseL4T(text string) string {
	first := text
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		first = text[:i]
	}
	rel := l4tRelease.FindStringSubmatch(first)
	rev := l4tRevision.FindStringSubmatch(first)
	if rel == nil || rev == nil {
		return ""
	}
	return "R" + rel[1] + "." + rev[1]
}

func ubuntuPoint(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, pinUbuntu) {
			return pinUbuntu
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "VERSION_ID=") {
			return strings.Trim(strings.TrimPrefix(line, "VERSION_ID="), `"`)
		}
	}
	return ""
}

func cudaFromJSON(b []byte) string {
	var body struct {
		Version string `json:"version"`
		CUDA    struct {
			Version string `json:"version"`
		} `json:"cuda"`
	}
	if err := json.Unmarshal(b, &body); err != nil {
		return ""
	}
	if v := strings.TrimSpace(body.CUDA.Version); v != "" {
		return v
	}
	return strings.TrimSpace(body.Version)
}

func cudaFromText(text string) string {
	if m := cudaRelease.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	if m := cudaVersion.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

func parseNvpmodel(text string) (int, string) {
	id := -1
	watts := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if intLine.MatchString(line) {
			var n int
			if _, err := fmt.Sscan(line, &n); err == nil {
				id = n
			}
			continue
		}
		if m := wattsRE.FindStringSubmatch(line); m != nil {
			watts = m[1] + "W"
		}
	}
	return id, watts
}

// cudaEligible is a preflight policy, never a certification grant.
func cudaEligible(v string) bool {
	if !regexp.MustCompile(`^13\.2\.[0-9]+$`).MatchString(v) {
		return false
	}
	patch, err := strconv.ParseUint(strings.Split(v, ".")[2], 10, 32)
	return err == nil && patch >= 1
}
