package runtime

import (
	"context"
	"strings"
	"testing"
)

func TestStartArgsUseNVIDIARuntimeNotGPUs(t *testing.T) {
	args := StartArgs(Spec{Name: "p2", ImageDigest: "sha256:deadbeef"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--runtime=nvidia") {
		t.Fatalf("missing nvidia runtime: %v", args)
	}
	for _, a := range args {
		if a == "--gpus" || strings.HasPrefix(a, "--gpus=") {
			t.Fatalf("must not use --gpus: %v", args)
		}
	}
	if args[len(args)-1] != "sha256:deadbeef" {
		t.Fatalf("digest last: %v", args)
	}
}

func TestStartEmptyDigestIsNoop(t *testing.T) {
	n := &NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("docker must not run without a digest")
		return nil, nil
	}}
	h, err := n.Start(context.Background(), Spec{LeaseID: "lease-1", CatalogID: "llama-3.1-8b-q4", Runtime: "nvidia"})
	if err != nil {
		t.Fatal(err)
	}
	if h == nil || !h.Noop || h.LeaseID != "lease-1" {
		t.Fatalf("handle %+v", h)
	}
	if err := n.Stop(context.Background(), h); err != nil {
		t.Fatal(err)
	}
}

func TestStartWrongRuntime(t *testing.T) {
	n := NewNVIDIA()
	_, err := n.Start(context.Background(), Spec{ImageDigest: "sha256:x", Runtime: "runc"})
	if err != ErrWrongRuntime {
		t.Fatalf("got %v", err)
	}
}

func TestStartStop(t *testing.T) {
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
	h, err := n.Start(context.Background(), Spec{Name: "lease", ImageDigest: "sha256:img", Runtime: "nvidia"})
	if err != nil {
		t.Fatal(err)
	}
	if h.ContainerID != "cid123" || h.Noop {
		t.Fatalf("id %q noop=%v", h.ContainerID, h.Noop)
	}
	if err := n.Stop(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[1][0] != "stop" || seen[1][1] != "cid123" {
		t.Fatalf("calls %+v", seen)
	}
}
