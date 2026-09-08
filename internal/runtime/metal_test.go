package runtime

import (
	"context"
	"strings"
	"testing"
)

func TestMetalStartArgsNoCUDA(t *testing.T) {
	args := MetalStartArgs(Spec{LeaseID: "lease-m", HostPort: "8080"}, "llama-server")
	joined := strings.Join(args, " ")
	if args[0] != "llama-server" || !contains(args, "--host") || !contains(args, "0.0.0.0") {
		t.Fatalf("llama-server listen: %v", args)
	}
	if !contains(args, "--port") || !contains(args, "8080") {
		t.Fatalf("port: %v", args)
	}
	for _, a := range args {
		al := strings.ToLower(a)
		if strings.Contains(al, "cuda") || strings.Contains(al, "nvidia") ||
			a == "--gpus" || strings.HasPrefix(a, "--gpus=") {
			t.Fatalf("must not invent CUDA on Mac: %v", args)
		}
	}
	for _, leak := range []string{"tok/s", "TOPS", "tokens/sec", "gfx1036"} {
		if strings.Contains(joined, leak) {
			t.Fatalf("do not invent %s: %v", leak, args)
		}
	}
}

func TestMetalStartMock(t *testing.T) {
	var seen [][]string
	m := &Metal{
		Bin: "llama-server",
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			seen = append(seen, append([]string{name}, args...))
			return []byte("42\n"), nil
		},
	}
	got, err := m.Start(context.Background(), Spec{LeaseID: "lease-m", ImageDigest: testRef})
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != BackendLlamaCppCPU || got.ContainerID != "42" {
		t.Fatalf("handle %+v", got)
	}
	if len(seen) != 1 || seen[0][0] != "llama-server" {
		t.Fatalf("calls %v", seen)
	}
	if err := m.Stop(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[1][0] != "kill" {
		t.Fatalf("stop %v", seen)
	}
}

func TestMetalStartEmptyDigest(t *testing.T) {
	m := &Metal{Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("must not run without digest")
		return nil, nil
	}}
	_, err := m.Start(context.Background(), Spec{LeaseID: "l"})
	if err != ErrNoImage {
		t.Fatalf("got %v", err)
	}
}

func TestMetalDefaultImageStubsLlamaCppCPU(t *testing.T) {
	if MetalImageEnabled() {
		t.Skip("darwin image: see metal_darwin_test.go")
	}
	path, reason := ResolveBackend(BackendLlamaCppCPU)
	want := "This image does not run llama-cpp-cpu."
	if path != StubFail || reason != want {
		t.Fatalf("Jetson image must stub llama-cpp-cpu: path=%v reason=%q", path, reason)
	}
	path, reason = ResolveBackend("llama-cpp-metal")
	if path != StubFail || reason != "This image does not run llama-cpp-metal." {
		t.Fatalf("llama-cpp-metal is not in the enum: path=%v reason=%q", path, reason)
	}
}
