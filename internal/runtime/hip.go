package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// HIPMinVRAMGB is the first class this scaffold targets (docs only).
// Not a tok/s claim. TOPS is not tok/s.
const HIPMinVRAMGB = 16

var (
	// ErrNoROCm means this -tags rocm image has no AMD KFD node.
	// Honest fail; do not invent a gfx target or set HSA_OVERRIDE.
	ErrNoROCm = errors.New("hypermesh: hip-rocm scaffold: no /dev/kfd (16GB+ AMD class first)")
)

const (
	hsaOverridePrefix = "HSA_OVERRIDE"
	kfdDevice         = "/dev/kfd"
	driDevice         = "/dev/dri"
)

// HIP is the AMD ROCm start path for a future host image built with
// -tags rocm. Structure only on the default AGX image (ResolveBackend
// still stub-fails hip-rocm there).
//
// Do not set HSA_OVERRIDE / HSA_OVERRIDE_GFX_VERSION. Do not invent a
// gfx target. repo@digest is the cert pin; this slice does not pull GHCR.
type HIP struct {
	Docker string
	Run    func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// NewHIP uses the docker on PATH.
func NewHIP() *HIP {
	return &HIP{Docker: "docker"}
}

// StripHSAOverride drops HSA_OVERRIDE* from an env block. This path
// never sets those variables.
func StripHSAOverride(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, hsaOverridePrefix) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// HIPStartArgs is docker run argv for a resolved repo@digest on ROCm.
// Device nodes only. Not --runtime=nvidia. Not x86 --gpus.
func HIPStartArgs(spec Spec, ref string) []string {
	args := []string{
		"run", "-d",
		"--device=" + kfdDevice,
		"--device=" + driDevice,
		"--group-add=video",
		"-p", publishSpec(spec.HostPort, spec.BindAddress),
	}
	if name := ContainerName(spec.LeaseID); name != "hypermesh-" {
		args = append(args, "--name", name)
	}
	args = append(args, ref)
	return args
}

// HIPDevicesPresent is the runtime probe for AMD KFD. Compile-time
// -tags rocm is still required before ResolveBackend returns HIPROCm.
func HIPDevicesPresent() bool {
	_, err := os.Stat(kfdDevice)
	return err == nil
}

// Start runs repo@digest with ROCm device nodes. It does not pull GHCR,
// does not set HSA_OVERRIDE, and does not invent completions or tok/s.
func (h *HIP) Start(ctx context.Context, spec Spec) (*Handle, error) {
	if strings.TrimSpace(spec.LeaseID) == "" {
		return nil, ErrNoLeaseID
	}
	ref, err := ImageRef(spec.ImageDigest)
	if err != nil {
		return nil, err
	}
	if h.Run == nil && !HIPDevicesPresent() {
		return nil, ErrNoROCm
	}
	name := ContainerName(spec.LeaseID)
	args := HIPStartArgs(spec, ref)
	for _, a := range args {
		if a == "--gpus" || strings.HasPrefix(a, "--gpus=") {
			return nil, ErrUsesGPUsFlag
		}
		if a == "--runtime=nvidia" || strings.HasPrefix(a, "HSA_OVERRIDE") {
			return nil, errors.New("hypermesh: hip-rocm must not use nvidia runtime or HSA_OVERRIDE")
		}
	}
	out, err := h.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("hypermesh runtime: hip-rocm start: %w", err)
	}
	return &Handle{
		Name:        name,
		LeaseID:     spec.LeaseID,
		CatalogID:   spec.CatalogID,
		ImageDigest: ref,
		ContainerID: strings.TrimSpace(string(out)),
		Backend:     BackendHIPROCm,
	}, nil
}

// Stop sends docker stop then docker rm so hypermesh-<lease_id> is
// free for the next lease_start. Same leftover-name path as NVIDIA.
func (h *HIP) Stop(ctx context.Context, handle *Handle) error {
	return stopAndRemove(ctx, h.run, handle)
}

func (h *HIP) run(ctx context.Context, args ...string) ([]byte, error) {
	name := h.Docker
	if name == "" {
		name = "docker"
	}
	if h.Run != nil {
		return h.Run(ctx, name, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = StripHSAOverride(os.Environ())
	return cmd.CombinedOutput()
}
