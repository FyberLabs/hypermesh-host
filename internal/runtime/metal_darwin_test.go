//go:build darwin

package runtime

import "testing"

func TestResolveBackendLlamaCppCPUMetal(t *testing.T) {
	if !MetalImageEnabled() {
		t.Fatal("darwin must enable metal image")
	}
	path, reason := ResolveBackend("llama-cpp-cpu")
	if path != LlamaCppHost || reason != "" {
		t.Fatalf("path=%v reason=%q", path, reason)
	}
	if !path.Runnable() {
		t.Fatal("LlamaCppHost must be runnable")
	}
	if LogLabel("llama-cpp-cpu") != BackendLlamaCppCPU {
		t.Fatalf("enum log %q", LogLabel("llama-cpp-cpu"))
	}
	if HostLogLabel() != MetalLogLabel {
		t.Fatalf("host log %q", HostLogLabel())
	}
	path, reason = ResolveBackend("llama-cpp-vulkan")
	if path != StubFail || reason != "This image does not run llama-cpp-vulkan." {
		t.Fatalf("vulkan stays stub: path=%v reason=%q", path, reason)
	}
	path, reason = ResolveBackend("llama-cpp-metal")
	if path != StubFail || reason != "This image does not run llama-cpp-metal." {
		t.Fatalf("unknown to CP: path=%v reason=%q", path, reason)
	}
	path, reason = ResolveBackend("hip-rocm")
	if HIPImageEnabled() {
		if path != HIPROCm {
			t.Fatalf("rocm+darwin hip: %v", path)
		}
	} else if path != StubFail || reason != "This image does not run hip-rocm." {
		t.Fatalf("default darwin still stubs hip-rocm: path=%v reason=%q", path, reason)
	}
	path, reason = ResolveBackend("")
	if path != NVIDIAContainer {
		t.Fatalf("omitted backend stays cuda-jetson: %v", path)
	}
}
