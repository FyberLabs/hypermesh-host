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

// DefaultGGUFPath is the AGX64-1 host weights. Not baked into the image.
// Override with Spec.GGUFPath / HYPERMESH_GGUF_PATH.
const DefaultGGUFPath = "/home/chris/hypermesh/workload/model.gguf"

// ContainerGGUFPath is where llama-server looks inside the container.
const ContainerGGUFPath = "/models/model.gguf"

// Spec is a leased workload from a lease_start job.
// ImageDigest must be repo@digest (the locked job field). No extra CP fields.
type Spec struct {
	LeaseID     string
	CatalogID   string
	ImageDigest string
	Runtime     string
	// HostPort is published as -p <BindAddress>:<HostPort>:8080.
	HostPort string
	// BindAddress is the WireGuard client address (host or CIDR). Required
	// so completions are not published on the LAN.
	BindAddress string
	// GGUFPath is the host file bind-mounted at ContainerGGUFPath (:ro).
	// Empty uses DefaultGGUFPath. Do not invent a model or image_hash.
	GGUFPath string
	// Backend is the lease_start enum (optional). NVIDIA ignores it for argv.
	Backend string
}

// Handle is a started container or host process.
type Handle struct {
	Name        string
	LeaseID     string
	CatalogID   string
	ImageDigest string
	ContainerID string
	// Backend is the path that started this handle (omitempty for old lease.json).
	Backend string `json:"backend,omitempty"`
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

func TunnelBindHost(raw string) string {
	addr := strings.TrimSpace(raw)
	if addr == "" {
		return ""
	}
	if host, _, ok := strings.Cut(addr, "/"); ok {
		addr = host
	}
	return addr
}

func publishSpec(hostPort, bindAddress string) string {
	p := strings.TrimSpace(hostPort)
	if p == "" {
		p = DefaultListenPort
	}
	if bind := TunnelBindHost(bindAddress); bind != "" {
		return bind + ":" + p + ":8080"
	}
	return "127.0.0.1:" + p + ":8080"
}

func ggufBind(host string) string {
	h := strings.TrimSpace(host)
	if h == "" {
		h = DefaultGGUFPath
	}
	return h + ":" + ContainerGGUFPath + ":ro"
}

// StartArgs is the docker run argv for a resolved repo@digest.
// Publishes container 8080 on the tunnel address only so the LAN cannot
// reach completions. The process inside may listen on 0.0.0.0:8080.
// Bind-mounts the host GGUF at /models/model.gguf:ro (not baked into the image).
func StartArgs(spec Spec, ref string) []string {
	args := []string{
		"run", "-d", "--runtime=nvidia",
		"-p", publishSpec(spec.HostPort, spec.BindAddress),
		"-v", ggufBind(spec.GGUFPath),
	}
	if name := ContainerName(spec.LeaseID); name != "hypermesh-" {
		args = append(args, "--name", name)
	}
	args = append(args, ref)
	return args
}

// Start pulls repo@digest then runs it with the NVIDIA runtime.
// A failed pull is not fatal when that exact pin is already local
// (private GHCR unauthorized, image present). Do not invent a digest.
// A failed run removes a leftover Status=Created container so a
// same-lease retry is not blocked on --name.
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
	if out, err := n.run(ctx, PullArgs(ref)...); err != nil {
		if !n.imagePresent(ctx, ref) {
			return nil, wrapDocker("pull", out, err)
		}
	}
	args := StartArgs(spec, ref)
	for _, a := range args {
		if a == "--gpus" || strings.HasPrefix(a, "--gpus=") {
			return nil, ErrUsesGPUsFlag
		}
	}
	out, err := n.run(ctx, args...)
	if err != nil {
		n.removeCreated(ctx, name)
		return nil, wrapDocker("start", out, err)
	}
	return &Handle{
		Name:        name,
		LeaseID:     spec.LeaseID,
		CatalogID:   spec.CatalogID,
		ImageDigest: ref,
		ContainerID: strings.TrimSpace(string(out)),
		Backend:     NormalizeBackend(spec.Backend),
	}, nil
}

// imagePresent is docker image inspect of the exact repo@digest pin.
func (n *NVIDIA) imagePresent(ctx context.Context, ref string) bool {
	_, err := n.run(ctx, "image", "inspect", "--format", "{{.Id}}", ref)
	return err == nil
}

// removeCreated deletes a named container left in Status=Created after
// a failed docker run (nvidia-cdi-hook panic). Leave running/exited.
func (n *NVIDIA) removeCreated(ctx context.Context, name string) {
	if name == "" || name == "hypermesh-" {
		return
	}
	out, err := n.run(ctx, "inspect", "--format", "{{.State.Status}}", name)
	if err != nil {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(string(out)), "created") {
		return
	}
	_, _ = n.run(ctx, "rm", name)
}

func wrapDocker(op string, out []byte, err error) error {
	detail := strings.Join(strings.Fields(string(out)), " ")
	if detail == "" {
		return fmt.Errorf("hypermesh runtime: docker %s: %w", op, err)
	}
	return fmt.Errorf("hypermesh runtime: docker %s: %w: %s", op, err, detail)
}

// Stop sends docker stop then docker rm so hypermesh-<lease_id> is
// free for the next lease_start. An already-gone container is a soft
// success (do not fail lease_stop when the leftover is already gone).
func (n *NVIDIA) Stop(ctx context.Context, h *Handle) error {
	return stopAndRemove(ctx, n.run, h)
}

// stopAndRemove is docker stop then docker rm of the named container.
// Prefer Handle.Name (hypermesh-<lease_id>) for rm so --name is free.
// A missing container is a soft success. A failed stop does not rm.
func stopAndRemove(ctx context.Context, run func(ctx context.Context, args ...string) ([]byte, error), h *Handle) error {
	if h == nil {
		return errors.New("hypermesh runtime: nil handle")
	}
	target := strings.TrimSpace(h.ContainerID)
	if target == "" {
		target = strings.TrimSpace(h.Name)
	}
	if target == "" {
		return errors.New("hypermesh runtime: empty handle")
	}
	if out, err := run(ctx, "stop", target); err != nil && !dockerAbsent(out, err) {
		return wrapDocker("stop", out, err)
	}
	rmTarget := strings.TrimSpace(h.Name)
	if rmTarget == "" || rmTarget == "hypermesh-" {
		rmTarget = target
	}
	if out, err := run(ctx, "rm", rmTarget); err != nil && !dockerAbsent(out, err) {
		return wrapDocker("rm", out, err)
	}
	return nil
}

func dockerAbsent(out []byte, err error) bool {
	if err == nil {
		return false
	}
	blob := strings.ToLower(string(out) + " " + err.Error())
	return strings.Contains(blob, "no such container")
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
