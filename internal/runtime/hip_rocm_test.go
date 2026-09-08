//go:build rocm

package runtime

import "testing"

func TestResolveBackendHIPROCmEnabled(t *testing.T) {
	if !HIPImageEnabled() {
		t.Fatal("rocm tag must enable hip image")
	}
	path, reason := ResolveBackend("hip-rocm")
	if path != HIPROCm || reason != "" {
		t.Fatalf("path=%v reason=%q", path, reason)
	}
	if !path.Runnable() {
		t.Fatal("HIPROCm must be runnable")
	}
	if LogLabel("hip-rocm") != BackendHIPROCm {
		t.Fatalf("log %q", LogLabel("hip-rocm"))
	}
	if HostLogLabel() != BackendHIPROCm {
		t.Fatalf("host log %q", HostLogLabel())
	}
	path, reason = ResolveBackend("llama-cpp-vulkan")
	if path != StubFail || reason != "This image does not run llama-cpp-vulkan." {
		t.Fatalf("vulkan stays stub: path=%v reason=%q", path, reason)
	}
	path, reason = ResolveBackend("")
	if path != NVIDIAContainer {
		t.Fatalf("omitted backend stays cuda-jetson: %v", path)
	}
}
