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
}

func TestStartArgsCustomHostPort(t *testing.T) {
	args := StartArgs(Spec{LeaseID: "lease-9", HostPort: "9090"}, testRef)
	if !contains(args, "9090:8080") || contains(args, "8080:8080") {
		t.Fatalf("custom host port: %v", args)
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
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
