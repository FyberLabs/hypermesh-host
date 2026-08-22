// Package runtime starts and stops leased workloads with the NVIDIA
// Container Runtime.
//
// The GPU path is `docker --runtime=nvidia`. Do not use x86 `--gpus`.
// The control plane has no lease-start or workload pull yet, so Start
// without an image digest fails clearly instead of faking a container.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

var (
	// ErrNoWorkload means the control plane has not sent an image digest.
	ErrNoWorkload = errors.New("hypermesh: no workload image digest; control plane has no lease-start or workload pull yet")
	// ErrUsesGPUsFlag is returned if a caller tries the x86 GPU path.
	ErrUsesGPUsFlag = errors.New("hypermesh: GPU path is NVIDIA Container Runtime (--runtime=nvidia), not x86 --gpus")
)

// Spec is a later leased workload. ImageDigest is required to start.
type Spec struct {
	Name        string
	ImageDigest string
}

// Handle is a started container.
type Handle struct {
	Name        string
	ImageDigest string
	ContainerID string
}

// NVIDIA talks to docker with --runtime=nvidia.
type NVIDIA struct {
	Docker string
	Run    func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// NewNVIDIA uses the docker on PATH.
func NewNVIDIA() *NVIDIA {
	return &NVIDIA{Docker: "docker"}
}

// StartArgs is the docker argv for a spec. Tests assert --runtime=nvidia
// and the absence of --gpus.
func StartArgs(spec Spec) []string {
	args := []string{"run", "-d", "--runtime=nvidia"}
	if spec.Name != "" {
		args = append(args, "--name", spec.Name)
	}
	args = append(args, spec.ImageDigest)
	return args
}

// Start runs the image with the NVIDIA runtime. Empty digest is an error.
func (n *NVIDIA) Start(ctx context.Context, spec Spec) (*Handle, error) {
	if strings.TrimSpace(spec.ImageDigest) == "" {
		return nil, ErrNoWorkload
	}
	args := StartArgs(spec)
	for _, a := range args {
		if a == "--gpus" || strings.HasPrefix(a, "--gpus=") {
			return nil, ErrUsesGPUsFlag
		}
	}
	out, err := n.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("hypermesh runtime: docker start: %w", err)
	}
	id := strings.TrimSpace(string(out))
	return &Handle{Name: spec.Name, ImageDigest: spec.ImageDigest, ContainerID: id}, nil
}

// Stop sends docker stop for the handle.
func (n *NVIDIA) Stop(ctx context.Context, h *Handle) error {
	if h == nil {
		return errors.New("hypermesh runtime: nil handle")
	}
	target := h.ContainerID
	if target == "" {
		target = h.Name
	}
	if target == "" {
		return errors.New("hypermesh runtime: empty handle")
	}
	_, err := n.run(ctx, "stop", target)
	if err != nil {
		return fmt.Errorf("hypermesh runtime: docker stop: %w", err)
	}
	return nil
}

func (n *NVIDIA) run(ctx context.Context, args ...string) ([]byte, error) {
	name := n.Docker
	if name == "" {
		name = "docker"
	}
	if n.Run != nil {
		return n.Run(ctx, name, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
