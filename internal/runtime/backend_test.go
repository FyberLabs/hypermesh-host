package runtime

import "testing"

func TestResolveBackendNVIDIAPath(t *testing.T) {
	for _, name := range []string{"", "cuda-jetson", "cuda-sm87", "  cuda-jetson  "} {
		path, reason := ResolveBackend(name)
		if path != NVIDIAContainer || reason != "" {
			t.Fatalf("%q: path=%v reason=%q", name, path, reason)
		}
		if NormalizeBackend(name) != BackendCUDAJetson {
			t.Fatalf("%q normalize %q", name, NormalizeBackend(name))
		}
		if LogLabel(name) != BackendCUDAJetson {
			t.Fatalf("%q log label %q", name, LogLabel(name))
		}
	}
}

func TestResolveBackendStubsAndUnknown(t *testing.T) {
	cases := []string{
		"llama-cpp-cpu",
		"llama-cpp-vulkan",
		"hip-rocm",
		"disk-io",
		"not-a-backend",
	}
	for _, backend := range cases {
		if backend == BackendLlamaCppCPU && (MetalImageEnabled() || CPUImageEnabled()) {
			continue
		}
		if backend == BackendHIPROCm && HIPImageEnabled() {
			continue
		}
		path, reason := ResolveBackend(backend)
		want := "This image does not run " + backend + "."
		if path != StubFail || reason != want {
			t.Fatalf("%s: path=%v reason=%q want %q", backend, path, reason, want)
		}
		if FailReason(backend) != want {
			t.Fatalf("FailReason %q", FailReason(backend))
		}
	}
}

func TestHostLogLabelDefaultAGX(t *testing.T) {
	if HIPImageEnabled() || MetalImageEnabled() {
		t.Skip("non-AGX image")
	}
	if HostLogLabel() != BackendCUDAJetson {
		t.Fatalf("AGX host log %q", HostLogLabel())
	}
	if !NVIDIAContainer.Runnable() || StubFail.Runnable() {
		t.Fatal("runnable flags")
	}
}

func TestFailReasonExact(t *testing.T) {
	if FailReason("llama-cpp-cpu") != "This image does not run llama-cpp-cpu." {
		t.Fatal(FailReason("llama-cpp-cpu"))
	}
	if FailReason("llama-cpp-vulkan") != "This image does not run llama-cpp-vulkan." {
		t.Fatal(FailReason("llama-cpp-vulkan"))
	}
	if FailReason("hip-rocm") != "This image does not run hip-rocm." {
		t.Fatal(FailReason("hip-rocm"))
	}
	if FailReason("disk-io") != "This image does not run disk-io." {
		t.Fatal(FailReason("disk-io"))
	}
}
