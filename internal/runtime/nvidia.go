// Package runtime starts and stops leased workloads with the NVIDIA
// Container Runtime.
//
// The GPU path is `docker --runtime=nvidia`. Do not use x86 `--gpus`.
//
// Empty image_digest: Start does not pull or invent a catalog image.
// It records a no-op handle for the lease and returns success. That is
// not a tokens/sec claim and not a running model.
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
	// ErrUsesGPUsFlag is returned if a caller tries the x86 GPU path.
	ErrUsesGPUsFlag = errors.New("hypermesh: GPU path is NVIDIA Container Runtime (--runtime=nvidia), not x86 --gpus")
	// ErrWrongRuntime means the job asked for something other than nvidia.
	ErrWrongRuntime = errors.New("hypermesh: lease runtime must be nvidia")
)

// Spec is a leased workload from a lease_start job.
type Spec struct {
	Name        string
	LeaseID     string
	CatalogID   string
	ImageDigest string
	Runtime     string
}

// Handle is a started container or a recorded no-op.
type Handle struct {
	Name        string
	LeaseID     string
	CatalogID   string
	ImageDigest string
	ContainerID string
	Noop        bool
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

// StartArgs is the docker argv for a spec with a digest.
func StartArgs(spec Spec) []string {
	args := []string{"run", "-d", "--runtime=nvidia"}
	if spec.Name != "" {
		args = append(args, "--name", spec.Name)
	}
	args = append(args, spec.ImageDigest)
	return args
}

// Start runs the image with the NVIDIA runtime.
//
// Empty ImageDigest: no docker invocation. Returns a Noop handle so the
// agent can record the lease and POST passed=true. A later soak hash
// replaces this.
func (n *NVIDIA) Start(ctx context.Context, spec Spec) (*Handle, error) {
	if spec.Runtime != "" && spec.Runtime != "nvidia" {
		return nil, ErrWrongRuntime
	}
	if strings.TrimSpace(spec.ImageDigest) == "" {
		return &Handle{
			Name:      spec.Name,
			LeaseID:   spec.LeaseID,
			CatalogID: spec.CatalogID,
			Noop:      true,
		}, nil
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
	return &Handle{
		Name:        spec.Name,
		LeaseID:     spec.LeaseID,
		CatalogID:   spec.CatalogID,
		ImageDigest: spec.ImageDigest,
		ContainerID: id,
	}, nil
}

// Stop sends docker stop, or is a no-op for a recorded empty-digest start.
func (n *NVIDIA) Stop(ctx context.Context, h *Handle) error {
	if h == nil {
		return errors.New("hypermesh runtime: nil handle")
	}
	if h.Noop {
		return nil
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
