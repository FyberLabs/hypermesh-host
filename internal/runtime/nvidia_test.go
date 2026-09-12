package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const testRef = "example.invalid/hypermesh/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestStartArgsUseNVIDIARuntimeNotGPUs(t *testing.T) {
	args := StartArgs(Spec{LeaseID: "lease-9"}, testRef)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--runtime=nvidia") {
		t.Fatalf("missing nvidia runtime: %v", args)
	}
	if !strings.Contains(joined, "--name") || !strings.Contains(joined, "hypermesh-lease-9") {
		t.Fatalf("lease-scoped name: %v", args)
	}
	for _, a := range args {
		if a == "--gpus" || strings.HasPrefix(a, "--gpus=") {
			t.Fatalf("must not use --gpus: %v", args)
		}
	}
	if args[len(args)-1] != testRef {
		t.Fatalf("ref last: %v", args)
	}
	if !contains(args, "-p") || !contains(args, "8080:8080") {
		t.Fatalf("must publish 8080:8080: %v", args)
	}
	if !contains(args, "-v") || !contains(args, DefaultGGUFPath+":"+ContainerGGUFPath+":ro") {
		t.Fatalf("must bind-mount default GGUF: %v", args)
	}
}

func TestStartArgsCustomHostPort(t *testing.T) {
	args := StartArgs(Spec{LeaseID: "lease-9", HostPort: "9090"}, testRef)
	if !contains(args, "9090:8080") || contains(args, "8080:8080") {
		t.Fatalf("custom host port: %v", args)
	}
}

func TestStartArgsGGUFBindMount(t *testing.T) {
	args := StartArgs(Spec{LeaseID: "lease-9", GGUFPath: "/tmp/custom.gguf"}, testRef)
	if !contains(args, "-v") || !contains(args, "/tmp/custom.gguf:/models/model.gguf:ro") {
		t.Fatalf("custom GGUF mount: %v", args)
	}
	if contains(args, DefaultGGUFPath+":"+ContainerGGUFPath+":ro") {
		t.Fatalf("override must replace default GGUF: %v", args)
	}
}

func TestImageRef(t *testing.T) {
	if _, err := ImageRef(""); !errors.Is(err, ErrNoImage) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := ImageRef("sha256:deadbeef"); !errors.Is(err, ErrBareDigest) {
		t.Fatalf("bare: %v", err)
	}
	if _, err := ImageRef("example.invalid/foo:latest"); !errors.Is(err, ErrBareDigest) {
		t.Fatalf("tag only: %v", err)
	}
	got, err := ImageRef(testRef)
	if err != nil || got != testRef {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestStartEmptyDigestFails(t *testing.T) {
	n := &NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("docker must not run without a digest")
		return nil, nil
	}}
	_, err := n.Start(context.Background(), Spec{LeaseID: "lease-1", CatalogID: "llama-3.1-8b-q4", Runtime: "nvidia"})
	if !errors.Is(err, ErrNoImage) {
		t.Fatalf("got %v", err)
	}
}

func TestStartBareDigestFails(t *testing.T) {
	n := &NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("docker must not run a bare digest")
		return nil, nil
	}}
	_, err := n.Start(context.Background(), Spec{LeaseID: "lease-1", ImageDigest: "sha256:abc", Runtime: "nvidia"})
	if !errors.Is(err, ErrBareDigest) {
		t.Fatalf("got %v", err)
	}
}

func TestStartWrongRuntime(t *testing.T) {
	n := NewNVIDIA()
	_, err := n.Start(context.Background(), Spec{LeaseID: "l", ImageDigest: testRef, Runtime: "runc"})
	if err != ErrWrongRuntime {
		t.Fatalf("got %v", err)
	}
}

func TestStartPullThenRunStop(t *testing.T) {
	var seen [][]string
	n := &NVIDIA{
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			seen = append(seen, append([]string{}, args...))
			if len(args) > 0 && args[0] == "run" {
				return []byte("cid123\n"), nil
			}
			return []byte{}, nil
		},
	}
	h, err := n.Start(context.Background(), Spec{LeaseID: "lease-a", ImageDigest: testRef, Runtime: "nvidia"})
	if err != nil {
		t.Fatal(err)
	}
	if h.ContainerID != "cid123" || h.Name != "hypermesh-lease-a" || h.Backend != BackendCUDAJetson {
		t.Fatalf("handle %+v", h)
	}
	if err := n.Stop(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 || seen[0][0] != "pull" || seen[1][0] != "run" || seen[2][0] != "stop" {
		t.Fatalf("calls %+v", seen)
	}
	if seen[0][1] != testRef || seen[1][len(seen[1])-1] != testRef {
		t.Fatalf("ref not used: %+v", seen)
	}
	if !contains(seen[1], "hypermesh-lease-a") {
		t.Fatalf("name: %v", seen[1])
	}
	if !contains(seen[1], "--runtime=nvidia") || !contains(seen[1], "-p") || !contains(seen[1], "8080:8080") {
		t.Fatalf("nvidia + publish: %v", seen[1])
	}
	if !contains(seen[1], "-v") || !contains(seen[1], DefaultGGUFPath+":"+ContainerGGUFPath+":ro") {
		t.Fatalf("GGUF bind-mount: %v", seen[1])
	}
}

func TestStartPullUnauthorizedUsesLocalImage(t *testing.T) {
	var seen [][]string
	n := &NVIDIA{
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			seen = append(seen, append([]string{}, args...))
			switch {
			case len(args) > 0 && args[0] == "pull":
				return []byte("Error response from daemon: unauthorized"), errors.New("exit status 1")
			case len(args) >= 2 && args[0] == "image" && args[1] == "inspect":
				if args[len(args)-1] != testRef {
					t.Fatalf("inspect must use job pin, got %v", args)
				}
				return []byte("sha256:localpin\n"), nil
			case len(args) > 0 && args[0] == "run":
				return []byte("cid-local\n"), nil
			default:
				t.Fatalf("unexpected docker %v", args)
				return nil, nil
			}
		},
	}
	h, err := n.Start(context.Background(), Spec{LeaseID: "lease-local", ImageDigest: testRef, Runtime: "nvidia"})
	if err != nil {
		t.Fatal(err)
	}
	if h.ContainerID != "cid-local" || h.ImageDigest != testRef {
		t.Fatalf("must use job pin, not invent a digest: %+v", h)
	}
	if len(seen) != 3 || seen[0][0] != "pull" || seen[1][0] != "image" || seen[2][0] != "run" {
		t.Fatalf("pull then inspect then run: %+v", seen)
	}
}

func TestStartPullFailsWhenImageAbsent(t *testing.T) {
	n := &NVIDIA{
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "run" {
				t.Fatal("must not docker run when pin is absent")
			}
			if len(args) > 0 && args[0] == "pull" {
				return []byte("Error response from daemon: unauthorized"), errors.New("exit status 1")
			}
			return nil, errors.New("no such image")
		},
	}
	_, err := n.Start(context.Background(), Spec{LeaseID: "lease-missing", ImageDigest: testRef, Runtime: "nvidia"})
	if err == nil {
		t.Fatal("expected pull error")
	}
	got := err.Error()
	if !strings.Contains(got, "docker pull") || !strings.Contains(got, "unauthorized") {
		t.Fatalf("wrapped pull error: %v", err)
	}
}

func TestStartRunErrorIncludesDockerOutput(t *testing.T) {
	n := &NVIDIA{
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "run" {
				return []byte("nvidia-cdi-hook cudacompat: slice bounds out of range [:73] with capacity 71\n"), errors.New("exit status 1")
			}
			if len(args) > 0 && args[0] == "inspect" {
				return nil, errors.New("no such container")
			}
			return []byte{}, nil
		},
	}
	_, err := n.Start(context.Background(), Spec{LeaseID: "lease-cdi", ImageDigest: testRef, Runtime: "nvidia"})
	if err == nil {
		t.Fatal("expected start error")
	}
	got := err.Error()
	if !strings.Contains(got, "docker start") || !strings.Contains(got, "nvidia-cdi-hook") || !strings.Contains(got, "slice bounds") {
		t.Fatalf("wrapped start error: %v", err)
	}
}

func TestStartRemovesCreatedLeftover(t *testing.T) {
	var seen [][]string
	n := &NVIDIA{
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			seen = append(seen, append([]string{}, args...))
			switch {
			case len(args) > 0 && args[0] == "run":
				return []byte("nvidia-cdi-hook create failed"), errors.New("exit status 1")
			case len(args) > 0 && args[0] == "inspect":
				return []byte("Created\n"), nil
			case len(args) > 0 && args[0] == "rm":
				if args[len(args)-1] != "hypermesh-lease-cdi" {
					t.Fatalf("rm target %v", args)
				}
				return []byte{}, nil
			default:
				return []byte{}, nil
			}
		},
	}
	_, err := n.Start(context.Background(), Spec{LeaseID: "lease-cdi", ImageDigest: testRef, Runtime: "nvidia"})
	if err == nil {
		t.Fatal("expected start error")
	}
	if len(seen) < 4 || seen[0][0] != "pull" || seen[1][0] != "run" || seen[2][0] != "inspect" || seen[3][0] != "rm" {
		t.Fatalf("run fail must inspect+rm Created leftover: %+v", seen)
	}
}

func TestStartLeavesNonCreatedContainer(t *testing.T) {
	var seen [][]string
	n := &NVIDIA{
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			seen = append(seen, append([]string{}, args...))
			switch {
			case len(args) > 0 && args[0] == "run":
				return []byte("Conflict: name already in use"), errors.New("exit status 125")
			case len(args) > 0 && args[0] == "inspect":
				return []byte("running\n"), nil
			case len(args) > 0 && args[0] == "rm":
				t.Fatal("must not rm a running container")
				return nil, errors.New("rm running")
			default:
				return []byte{}, nil
			}
		},
	}
	_, err := n.Start(context.Background(), Spec{LeaseID: "lease-live", ImageDigest: testRef, Runtime: "nvidia"})
	if err == nil {
		t.Fatal("expected start error")
	}
	for _, call := range seen {
		if len(call) > 0 && call[0] == "rm" {
			t.Fatalf("rm on non-created: %+v", seen)
		}
	}
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
