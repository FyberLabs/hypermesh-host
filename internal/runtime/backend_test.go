package runtime

import "testing"

func TestResolveBackendNVIDIAPath(t *testing.T) {
	for _, name := range []string{"", "cuda-jetson", "cuda-sm87", "  cuda-jetson  "} {
		path, reason := ResolveBackend(name)
		if path != NVIDIAContainer || reason != "" {
			t.Fatalf("%q: path=%v reason=%q", name, path, reason)
		}
		if LogLabel(name) != NVIDIALogLabel {
			t.Fatalf("%q log label %q", name, LogLabel(name))
		}
	}
}

func TestResolveBackendStubs(t *testing.T) {
	cases := []struct {
		backend string
		reason  string
	}{
		{"llama-cpp-cpu", "this image does not run CPU GGUF"},
		{"llama-cpp-vulkan", "Vulkan RADV not on this image (not 7945HX 610M)"},
		{"hip-rocm", "HIP/ROCm not on this image"},
		{"disk-io", "not a certified runtime"},
		{"not-a-backend", "unknown backend"},
	}
	for _, tc := range cases {
		path, reason := ResolveBackend(tc.backend)
		if path != StubFail || reason != tc.reason {
			t.Fatalf("%s: path=%v reason=%q want %q", tc.backend, path, reason, tc.reason)
		}
		if LogLabel(tc.backend) != tc.backend {
			t.Fatalf("%s log label %q", tc.backend, LogLabel(tc.backend))
		}
	}
}
