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
	// ErrNoMetalBin means this darwin image has no llama-server on PATH.
	// Fails without faking CUDA or a completion.
	ErrNoMetalBin = errors.New("hypermesh: llama-cpp-cpu metal scaffold: llama-server not on PATH")
)

// Metal is the Apple Silicon llama.cpp path behind llama-cpp-cpu.
// ResolveBackend selects it only on darwin. Jetson Linux still stubs.
//
// This is not CUDA. llama-cpp-metal is not in the CP enum; that name
// still FailReasons. Lab / cert-limits: Fyber-published hashes only.
type Metal struct {
	Bin string
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// NewMetal uses llama-server on PATH.
func NewMetal() *Metal {
	return &Metal{Bin: "llama-server"}
}

// MetalStartArgs is the host llama-server argv. Metal is the llama.cpp
// default on Apple Silicon. No CUDA flags. No invented -ngl tok/s.
func MetalStartArgs(spec Spec, bin string) []string {
	if strings.TrimSpace(bin) == "" {
		bin = "llama-server"
	}
	port := strings.TrimSpace(spec.HostPort)
	if port == "" {
		port = DefaultListenPort
	}
	host := TunnelBindHost(spec.BindAddress)
	if host == "" {
		host = "127.0.0.1"
	}
	return []string{
		bin,
		"--host", host,
		"--port", port,
	}
}

// Start execs llama-server for a cert-pinned repo@digest. It does not
// pull GHCR, does not start docker --runtime=nvidia, and does not
// invent a GGUF or a completion. A later slice wires the local weights.
func (m *Metal) Start(ctx context.Context, spec Spec) (*Handle, error) {
	if strings.TrimSpace(spec.LeaseID) == "" {
		return nil, ErrNoLeaseID
	}
	ref, err := ImageRef(spec.ImageDigest)
	if err != nil {
		return nil, err
	}
	bin := strings.TrimSpace(m.Bin)
	if bin == "" {
		bin = "llama-server"
	}
	if m.Run == nil {
		if _, lookErr := exec.LookPath(bin); lookErr != nil {
			return nil, ErrNoMetalBin
		}
	}
	args := MetalStartArgs(spec, bin)
	for _, a := range args {
		al := strings.ToLower(a)
		if a == "--gpus" || strings.HasPrefix(a, "--gpus=") ||
			strings.Contains(al, "cuda") || strings.Contains(al, "nvidia") {
			return nil, errors.New("hypermesh: llama-cpp-cpu metal must not use CUDA")
		}
	}
	out, err := m.run(ctx, args[0], args[1:]...)
	if err != nil {
		return nil, fmt.Errorf("hypermesh runtime: metal start: %w", err)
	}
	return &Handle{
		Name:        ContainerName(spec.LeaseID),
		LeaseID:     spec.LeaseID,
		CatalogID:   spec.CatalogID,
		ImageDigest: ref,
		ContainerID: strings.TrimSpace(string(out)),
		Backend:     BackendLlamaCppCPU,
	}, nil
}

// Stop kills the host llama-server process.
func (m *Metal) Stop(ctx context.Context, h *Handle) error {
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
	_, err := m.run(ctx, "kill", target)
	if err != nil {
		return fmt.Errorf("hypermesh runtime: metal stop: %w", err)
	}
	return nil
}

func (m *Metal) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if m.Run != nil {
		return m.Run(ctx, name, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
