package installer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/buildinfo"
)

// SetupProfile is a revisioned policy, not hardware certification. Changing
// its matching rules, packages or mutation policy requires a new revision.
type SetupProfile struct {
	ID string `json:"id"`
	Revision int `json:"revision"`
	Validation string `json:"validation"`
	DriverPolicy string `json:"driver_policy"`
	RequiredPackages []string `json:"required_packages"`
}

func setupProfiles() []SetupProfile {
	packages := []string{"ca-certificates", "gnupg", "python3", "wireguard-go", "wireguard-tools"}
	return []SetupProfile{
		{"ubuntu-jetson-cuda", 1, "candidate; physical acceptance pending", "preserve installed vendor stack", append([]string{}, packages...)},
		{"ubuntu-amd-hip", 1, "candidate; physical acceptance pending", "preserve installed vendor stack", append([]string{}, packages...)},
		{"ubuntu-discrete-cuda", 1, "candidate; physical acceptance pending", "preserve installed vendor stack", append([]string{}, packages...)},
	}
}

type SetupPlatform struct {
	OS string `json:"os"`
	Version string `json:"version"`
	Kernel string `json:"kernel"`
	DefaultTarget string `json:"default_target"`
	PackageStateSHA256 string `json:"package_state_sha256"`
	ProtectedPackages []PackageChange `json:"protected_packages"`
}

type SetupChange struct {
	ID string `json:"id"`
	Kind string `json:"kind"`
	Required bool `json:"required"`
	Argv []string `json:"argv"`
	Reason string `json:"reason"`
}

type SetupPlan struct {
	SchemaVersion int `json:"schema_version"`
	PlanSHA256 string `json:"plan_sha256"`
	InstallerCommit string `json:"installer_commit"`
	InstallerVersion string `json:"installer_version"`
	InstallerBuild int `json:"installer_build"`
	Profile *SetupProfile `json:"profile"`
	Preflight Plan `json:"preflight"`
	Platform SetupPlatform `json:"platform"`
	Packages []PackageChange `json:"packages"`
	Changes []SetupChange `json:"changes"`
	Blockers []string `json:"blockers"`
	Pending []string `json:"pending"`
	Reviewable bool `json:"reviewable"`
	InstallAuthorized bool `json:"install_authorized"`
	RebootPolicy string `json:"reboot_policy"`
}

type setupSource struct {
	ReadFile func(string) ([]byte, error)
	Output func(context.Context, string, ...string) ([]byte, error)
}

type setupOutput struct { bytes.Buffer }
func (b *setupOutput) Write(p []byte) (int, error) {
	if len(p) > 4<<20-b.Len() { return 0, fmt.Errorf("setup probe output too large") }
	return b.Buffer.Write(p)
}

func (s setupSource) read(path string) ([]byte, error) {
	if s.ReadFile != nil { return s.ReadFile(path) }
	return os.ReadFile(path)
}
func (s setupSource) output(ctx context.Context, name string, args ...string) ([]byte, error) {
	if s.Output != nil { return s.Output(ctx, name, args...) }
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	// Make package-manager parsing independent of the operator's locale.
	env := []string{}
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "LC_") && !strings.HasPrefix(item, "LANG=") { env = append(env, item) }
	}
	cmd.Env = append(env, "LC_ALL=C", "LANG=C")
	var out setupOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil { return nil, fmt.Errorf("%s probe failed; inspect local tool configuration", name) }
	return out.Bytes(), nil
}

// RunSetupPlan only inspects the system and simulates APT; no approval or
// installation happens here. Shell redirection is the caller's output choice.
func RunSetupPlan(ctx context.Context, args []string, out, diagnostic io.Writer) int {
	if runtime.GOOS != "linux" {
		fmt.Fprintln(diagnostic, "plan-setup currently requires Linux")
		return 1
	}
	return runSetupPlan(ctx, args, out, diagnostic, setupSource{}, func(ctx context.Context, args []string, out, diagnostic io.Writer) int {
		return RunPlan(ctx, args, out, diagnostic)
	})
}

func runSetupPlan(ctx context.Context, args []string, out, diagnostic io.Writer, source setupSource,
	preflight func(context.Context, []string, io.Writer, io.Writer) int) int {
	flags := flag.NewFlagSet("plan-setup", flag.ContinueOnError)
	flags.SetOutput(diagnostic)
	mode := flags.String("memory-mode", "", "uma or discrete")
	directory := flags.String("model-dir", "", "existing model-cache directory")
	allocation := flags.Int64("uma-gib", 0, "proposed UMA allocation in GiB")
	address := flags.String("aiuditor-check-url", "", "HTTPS readiness URL")
	profile := flags.String("profile", "auto", "auto or a profile ID from --list-profiles")
	headless := flags.Bool("headless", false, "propose multi-user.target for the next boot; still requires later approval")
	list := flags.Bool("list-profiles", false, "list versioned candidate policies without probing")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp { return 0 }
		return 2
	}
	if flags.NArg() != 0 { fmt.Fprintln(diagnostic, "unexpected positional arguments"); return 2 }
	if *list {
		if err := json.NewEncoder(out).Encode(setupProfiles()); err != nil { return 1 }
		return 0
	}
	if *profile != "auto" {
		known := false
		for _, p := range setupProfiles() { if p.ID == *profile { known = true } }
		if !known { fmt.Fprintln(diagnostic, "unknown profile; use --list-profiles"); return 2 }
	}
	var preliminary bytes.Buffer
	code := preflight(ctx, []string{"--memory-mode", *mode, "--model-dir", *directory,
		"--uma-gib", strconv.FormatInt(*allocation, 10), "--aiuditor-check-url", *address},
		&preliminary, diagnostic)
	if code == 2 { return 2 }
	var p Plan
	if err := json.Unmarshal(preliminary.Bytes(), &p); err != nil {
		fmt.Fprintln(diagnostic, "cannot read preliminary preflight"); return 1
	}
	result := buildSetupPlan(ctx, p, *profile, *headless, source)
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil { fmt.Fprintln(diagnostic, "cannot write setup plan"); return 1 }
	if !result.Reviewable { return 1 }
	return 0
}

func normalizedSetupArch(arch string) string {
	if arch == "aarch64" { return "arm64" }
	return arch
}

func selectSetupProfile(p Plan, platform SetupPlatform, requested string) (*SetupProfile, error) {
	if platform.OS != "ubuntu" || (platform.Version != "22.04" && platform.Version != "24.04") {
		return nil, fmt.Errorf("no setup policy for this OS; use an explicitly supported Ubuntu release")
	}
	f := p.Hardware.Facts
	arch := f.Arch
	if arch == "aarch64" { arch = "arm64" }
	id := ""
	if p.Resources.Resources.UMA {
		r := p.Resources.Resources.UMARuntime
		if !validUMARuntime(r) { return nil, fmt.Errorf("valid integrated CUDA/HIP runtime evidence is required") }
		switch {
		case f.Tegra && arch == "arm64" && r.Backend == "cuda" && p.Hardware.Backend == "cuda":
			id = "ubuntu-jetson-cuda"
		case !f.Tegra && arch == "amd64" && r.Backend == "hip" && p.Hardware.Backend == "hip":
			id = "ubuntu-amd-hip"
		}
	} else if !f.Tegra && (arch == "arm64" || arch == "amd64") && p.Hardware.Backend == "cuda" {
		id = "ubuntu-discrete-cuda"
	}
	if id == "" { return nil, fmt.Errorf("no setup policy matches measured architecture, memory topology and runtime") }
	if requested != "auto" && requested != id { return nil, fmt.Errorf("requested profile does not match measured hardware; use auto or correct the runtime") }
	for _, profile := range setupProfiles() { if profile.ID == id { return &profile, nil } }
	return nil, fmt.Errorf("missing setup profile")
}

func parseSetupOS(data []byte) (string, string, error) {
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || (key != "ID" && key != "VERSION_ID") { continue }
		if _, exists := values[key]; exists { return "", "", fmt.Errorf("duplicate OS identity") }
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1:len(value)-1]
		}
		values[key] = value
	}
	if values["ID"] == "" || values["VERSION_ID"] == "" { return "", "", fmt.Errorf("missing OS identity") }
	return values["ID"], values["VERSION_ID"], nil
}

func buildSetupPlan(ctx context.Context, p Plan, requested string, headless bool, source setupSource) SetupPlan {
	result := SetupPlan{SchemaVersion: 1, InstallerCommit: buildinfo.Current(),
		InstallerVersion: buildinfo.CurrentVersion(), InstallerBuild: buildinfo.CurrentBuild(),
		Preflight: p, Packages: []PackageChange{}, Changes: []SetupChange{}, Blockers: append([]string{}, p.Blockers...),
		Pending: []string{"Approve a complete plan and revalidate its inputs immediately before applying",
			"Bind verified agent, rollback helper, configuration and service payloads in the apply milestone",
			"Validate this candidate profile on hardware, including actual model/context fit",
			"Complete device authorization, authenticated AIuditor checks and enrollment before selling"},
		RebootPolicy: "No reboot is executed. Kernel and driver transitions require a separately validated profile."}
	// Total bound as well as per-command bounds.
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	fail := func(message string) { result.Blockers = append(result.Blockers, message) }
	if !p.PreliminaryChecksPassed { fail("preliminary preflight must pass before setup can be approved") }
	raw, err := source.read("/etc/os-release")
	if err != nil { fail("cannot read OS identity") } else {
		result.Platform.OS, result.Platform.Version, err = parseSetupOS(raw)
		if err != nil { fail("cannot parse OS identity") }
	}
	raw, err = source.read("/proc/sys/kernel/osrelease")
	if err != nil || strings.TrimSpace(string(raw)) == "" { fail("cannot read running kernel") } else {
		result.Platform.Kernel = strings.TrimSpace(string(raw))
	}
	result.Profile, err = selectSetupProfile(p, result.Platform, requested)
	if err != nil { fail(err.Error()) }
	raw, err = source.output(ctx, "systemctl", "get-default")
	if err != nil { fail("cannot read systemd default target") } else {
		result.Platform.DefaultTarget = strings.TrimSpace(string(raw))
		if result.Platform.DefaultTarget != "multi-user.target" && result.Platform.DefaultTarget != "graphical.target" {
			fail("unrecognized default target; review the existing boot configuration")
		}
	}
	raw, err = source.output(ctx, "dpkg-query", "-W", "-f=${binary:Package}\t${Version}\t${db:Status-Abbrev}\n")
	var installed map[string]string
	if err != nil { fail("cannot inventory installed packages") } else {
		installed, err = parseInstalledPackages(string(raw))
		if err != nil { fail(err.Error()) } else {
			canonical, _ := json.Marshal(installed)
			digest := sha256.Sum256(canonical)
			result.Platform.PackageStateSHA256 = hex.EncodeToString(digest[:])
			result.Platform.ProtectedPackages = []PackageChange{}
			for name, version := range installed {
				if protectedSetupPackage(name) {
					result.Platform.ProtectedPackages = append(result.Platform.ProtectedPackages, PackageChange{name, version, version})
				}
			}
			sort.Slice(result.Platform.ProtectedPackages, func(i,j int) bool { return result.Platform.ProtectedPackages[i].Name < result.Platform.ProtectedPackages[j].Name })
		}
	}
	if result.Profile != nil && installed != nil {
		result.Packages, err = resolveSetupPackages(ctx, source, installed, result.Profile.RequiredPackages, normalizedSetupArch(p.Hardware.Facts.Arch))
		if err != nil { fail(err.Error()); result.Packages = []PackageChange{} }
		if len(result.Packages) > 0 {
			argv := []string{"apt-get", "--no-install-recommends", "--no-remove", "install"}
			for _, pkg := range result.Packages { argv = append(argv, pkg.Name+"="+pkg.To) }
			result.Changes = append(result.Changes, SetupChange{"prerequisite-packages", "packages", true, argv,
				"Exact versions include the simulated dependency closure; package maintainer scripts may restart services, so apply needs explicit review and recovery."})
		}
		vendorKnown := false
		for name := range installed {
			base := packageBase(name)
			if !hasNativePackage(installed, base, normalizedSetupArch(p.Hardware.Facts.Arch)) { continue }
			switch result.Profile.ID {
			case "ubuntu-jetson-cuda":
				vendorKnown = vendorKnown || base == "nvidia-l4t-core"
			case "ubuntu-amd-hip":
				vendorKnown = vendorKnown || base == "hip-runtime-amd"
			case "ubuntu-discrete-cuda":
				vendorKnown = vendorKnown || strings.HasPrefix(base, "nvidia-driver-")
			}
		}
		if !vendorKnown { fail("vendor runtime package version is unknown; use a package-managed vendor stack before planning") }
		// Do not silently replace an existing Docker distribution or configure
		// the vendor container runtime using a generic distro package.
		if !hasNativePackage(installed, "docker.io", normalizedSetupArch(p.Hardware.Facts.Arch)) && !hasNativePackage(installed, "docker-ce", normalizedSetupArch(p.Hardware.Facts.Arch)) {
			fail("Docker engine is not installed through a recognized package; install the profile-compatible engine and rerun planning")
		}
	}
	if headless && result.Platform.DefaultTarget == "graphical.target" {
		result.Changes = append(result.Changes, SetupChange{"headless-next-boot", "boot-target", false,
			[]string{"systemctl", "set-default", "multi-user.target"},
			"Disable graphical login on the next boot after explicit approval; do not isolate targets or stop SSH/NAS services."})
		result.RebootPolicy = "Headless target takes effect at the next operator-approved reboot; no automatic reboot or immediate target isolation."
	}
	if err := ctx.Err(); err != nil { fail("setup planning interrupted or exceeded its deadline; rerun") }
	result.Reviewable = len(result.Blockers) == 0
	// The digest binds immutable profile revision, installer build, observed
	// package/driver/kernel state, resource evidence and the exact proposed argv.
	raw, _ = json.Marshal(result)
	digest := sha256.Sum256(raw)
	result.PlanSHA256 = hex.EncodeToString(digest[:])
	return result
}

