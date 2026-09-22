package runtime

import (
	"context"
	"strings"
	"testing"
)

func TestHIPStartArgsNoNVIDIANoOverride(t *testing.T) {
	args := HIPStartArgs(Spec{LeaseID: "lease-hip"}, testRef)
	joined := strings.Join(args, " ")
	if !contains(args, "--device=/dev/kfd") || !contains(args, "--device=/dev/dri") {
		t.Fatalf("ROCm devices: %v", args)
	}
	if contains(args, "--runtime=nvidia") || strings.Contains(joined, "nvidia") {
		t.Fatalf("hip-rocm must not use nvidia: %v", args)
	}
	for _, a := range args {
		if a == "--gpus" || strings.HasPrefix(a, "--gpus=") {
			t.Fatalf("must not use --gpus: %v", args)
		}
		if strings.Contains(a, "HSA_OVERRIDE") {
			t.Fatalf("must not set HSA_OVERRIDE: %v", args)
		}
		if strings.Contains(a, "gfx1036") || strings.Contains(strings.ToLower(a), "gfx") {
			t.Fatalf("must not invent gfx: %v", args)
		}
	}
	if !contains(args, "127.0.0.1:8080:8080") || !contains(args, "hypermesh-lease-hip") {
		t.Fatalf("publish/name: %v", args)
	}
	assertWorkloadJail(t, args)
	if args[len(args)-1] != testRef {
		t.Fatalf("ref last: %v", args)
	}
	for _, leak := range []string{"tok/s", "TOPS", "tokens/sec"} {
		if strings.Contains(joined, leak) {
			t.Fatalf("do not invent %s: %v", leak, args)
		}
	}
}

func TestStripHSAOverride(t *testing.T) {
	got := StripHSAOverride([]string{
		"PATH=/usr/bin",
		"HSA_OVERRIDE_GFX_VERSION=10.3.0",
		"HSA_OVERRIDE=1",
		"HOME=/tmp",
	})
	joined := strings.Join(got, ",")
	if strings.Contains(joined, "HSA_OVERRIDE") {
		t.Fatalf("override leaked: %v", got)
	}
	if len(got) != 2 || got[0] != "PATH=/usr/bin" || got[1] != "HOME=/tmp" {
		t.Fatalf("kept %v", got)
	}
}

func TestHIPStartMockNoPull(t *testing.T) {
	var seen [][]string
	h := &HIP{
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			seen = append(seen, append([]string{}, args...))
			if len(args) > 0 && args[0] == "network" {
				return []byte{}, nil
			}
			return []byte("cid-hip\n"), nil
		},
	}
	got, err := h.Start(context.Background(), Spec{LeaseID: "lease-h", ImageDigest: testRef})
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != BackendHIPROCm || got.ContainerID != "cid-hip" {
		t.Fatalf("handle %+v", got)
	}
	runIdx := -1
	for i, call := range seen {
		if len(call) > 0 && call[0] == "run" {
			runIdx = i
			break
		}
	}
	if runIdx < 0 {
		t.Fatalf("missing run, calls=%v", seen)
	}
	for i := 0; i < runIdx; i++ {
		if seen[i][0] == "pull" {
			t.Fatalf("scaffold must not docker pull, calls=%v", seen)
		}
	}
	assertWorkloadJail(t, seen[runIdx])
	if err := h.Stop(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	if seen[len(seen)-2][0] != "stop" || seen[len(seen)-1][0] != "rm" {
		t.Fatalf("stop then rm: %v", seen)
	}
	if seen[len(seen)-1][len(seen[len(seen)-1])-1] != "hypermesh-lease-h" {
		t.Fatalf("rm name: %v", seen[len(seen)-1])
	}
}

func TestHIPStartEmptyDigest(t *testing.T) {
	h := &HIP{Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("must not run without digest")
		return nil, nil
	}}
	_, err := h.Start(context.Background(), Spec{LeaseID: "l"})
	if err != ErrNoImage {
		t.Fatalf("got %v", err)
	}
}

func TestHIPDefaultImageStillStubs(t *testing.T) {
	if HIPImageEnabled() {
		t.Skip("rocm-tagged image: see hip_rocm_test.go")
	}
	path, reason := ResolveBackend(BackendHIPROCm)
	want := "This image does not run hip-rocm."
	if path != StubFail || reason != want {
		t.Fatalf("default AGX image must stub hip-rocm: path=%v reason=%q", path, reason)
	}
}
