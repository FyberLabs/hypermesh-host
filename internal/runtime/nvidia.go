// Package runtime starts and stops leased workloads with the NVIDIA
// Container Runtime.
//
// The GPU path is `docker --runtime=nvidia`. Do not use x86 `--gpus`.
//
// lease_start needs a real image ref `repo@digest`. A bare sha256:hex is
// not runnable. An empty digest is not a running instance: Start fails
// so the agent POSTs passed=false and the control plane does not go active.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode"
)

var (
	// ErrUsesGPUsFlag is returned if a caller tries the x86 GPU path.
	ErrUsesGPUsFlag = errors.New("hypermesh: GPU path is NVIDIA Container Runtime (--runtime=nvidia), not x86 --gpus")
	// ErrWrongRuntime means the job asked for something other than nvidia.
	ErrWrongRuntime = errors.New("hypermesh: lease runtime must be nvidia")
	// ErrNoImage means lease_start has no image_digest. Not a running instance.
	ErrNoImage = errors.New("hypermesh: no image_digest; no soak image")
	// ErrBareDigest means we were given sha256:hex without a repository.
	ErrBareDigest = errors.New("hypermesh: image_digest is not repo@digest")
	// ErrNoLeaseID means we cannot name the container without a lease_id.
	ErrNoLeaseID = errors.New("hypermesh: lease_start requires lease_id")
)

// DefaultListenPort is the host port published to the container's 8080
// (OpenAI-shaped POST /v1/chat/completions). The Fyber router proxies to
// http://<client_address>:8080.
const DefaultListenPort = "8080"

// Spec is a leased workload from a lease_start job.
// ImageDigest must be repo@digest (the locked job field). No extra CP fields.
type Spec struct {
	LeaseID     string
	CatalogID   string
	ImageDigest string
	Runtime     string
	// HostPort is published as -p <HostPort>:8080. Empty uses DefaultListenPort.
	HostPort string
}

// Handle is a started container.
type Handle struct {
	Name        string
	LeaseID     string
	CatalogID   string
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

// ImageRef accepts the locked image_digest field. It must be repo@digest.
// Bare sha256:hex fails; we will not invent a registry or repository.
func ImageRef(imageDigest string) (string, error) {
	s := strings.TrimSpace(imageDigest)
	if s == "" {
		return "", ErrNoImage
	}
	if !strings.Contains(s, "/") && strings.HasPrefix(s, "sha256:") {
		return "", ErrBareDigest
	}
	repo, dig, ok := strings.Cut(s, "@")
	if !ok || strings.TrimSpace(repo) == "" || !strings.HasPrefix(dig, "sha256:") {
		return "", ErrBareDigest
	}
	return s, nil
}

// ContainerName is unique per lease so a second lease_start is not blocked.
func ContainerName(leaseID string) string {
	var b strings.Builder
	b.WriteString("hypermesh-")
	for _, r := range leaseID {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// PullArgs is `docker pull repo@digest`.
func PullArgs(ref string) []string {
	return []string{"pull", ref}
}

func publishSpec(hostPort string) string {
	p := strings.TrimSpace(hostPort)
	if p == "" {
		p = DefaultListenPort
	}
	return p + ":8080"
}

// StartArgs is the docker run argv for a resolved repo@digest.
// Publishes container 8080 on the host so the WG client_address can reach
// /v1/chat/completions. The process inside must listen on 0.0.0.0:8080.
func StartArgs(spec Spec, ref string) []string {
	args := []string{"run", "-d", "--runtime=nvidia", "-p", publishSpec(spec.HostPort)}
	if name := ContainerName(spec.LeaseID); name != "hypermesh-" {
		args = append(args, "--name", name)
	}
	args = append(args, ref)
	return args
}

// Start pulls repo@digest then runs it with the NVIDIA runtime.
func (n *NVIDIA) Start(ctx context.Context, spec Spec) (*Handle, error) {
	if spec.Runtime != "" && spec.Runtime != "nvidia" {
		return nil, ErrWrongRuntime
	}
	if strings.TrimSpace(spec.LeaseID) == "" {
		return nil, ErrNoLeaseID
	}
	ref, err := ImageRef(spec.ImageDigest)
	if err != nil {
		return nil, err
	}
	name := ContainerName(spec.LeaseID)
	if _, err := n.run(ctx, PullArgs(ref)...); err != nil {
		return nil, fmt.Errorf("hypermesh runtime: docker pull: %w", err)
	}
	args := StartArgs(spec, ref)
	for _, a := range args {
		if a == "--gpus" || strings.HasPrefix(a, "--gpus=") {
			return nil, ErrUsesGPUsFlag
		}
	}
	out, err := n.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("hypermesh runtime: docker start: %w", err)
	}
	return &Handle{
		Name:        name,
		LeaseID:     spec.LeaseID,
		CatalogID:   spec.CatalogID,
		ImageDigest: ref,
		ContainerID: strings.TrimSpace(string(out)),
	}, nil
}

// Stop sends docker stop.
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
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
