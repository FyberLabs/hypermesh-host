package runtime

import "strings"

// lease_start backend enum (host-runtimes.md). runtime stays "nvidia"
// for oci cuda-jetson. Do not overload that locked field. Loaders stay
// oci|gguf. disk-io is not in this enum (unknown → fail).
const (
	BackendCUDAJetson     = "cuda-jetson"
	BackendCUDASM87       = "cuda-sm87"
	BackendLlamaCppCPU    = "llama-cpp-cpu"
	BackendLlamaCppVulkan = "llama-cpp-vulkan"
	BackendHIPROCm        = "hip-rocm"
)

// NVIDIALogLabel is the log-only name on this AGX image (omitted = cuda-jetson).
// Heartbeat and Path B telem may print this. No tok/s. TOPS is not tok/s.
const NVIDIALogLabel = BackendCUDAJetson

// Path says whether this box may call docker --runtime=nvidia.
type Path int

const (
	// NVIDIAContainer is docker --runtime=nvidia, repo@digest. Phase 1 AGX.
	NVIDIAContainer Path = iota
	// StubFail is a compile-time no-op. POST passed=false. Do not start work.
	StubFail
)

// NormalizeBackend maps omitted and cuda-sm87 to cuda-jetson.
func NormalizeBackend(backend string) string {
	switch strings.TrimSpace(backend) {
	case "", BackendCUDAJetson, BackendCUDASM87:
		return BackendCUDAJetson
	default:
		return strings.TrimSpace(backend)
	}
}

// FailReason is the customer-safe stub/unknown string (host-runtimes.md).
func FailReason(backend string) string {
	return "This image does not run " + strings.TrimSpace(backend) + "."
}

// ResolveBackend classifies the optional backend.
// Omitted and cuda-sm87 are cuda-jetson (NVIDIA container path).
// Stubs and unknown POST passed=false. Do not set HSA_OVERRIDE.
func ResolveBackend(backend string) (Path, string) {
	name := strings.TrimSpace(backend)
	switch name {
	case "", BackendCUDAJetson, BackendCUDASM87:
		return NVIDIAContainer, ""
	default:
		return StubFail, FailReason(name)
	}
}

// LogLabel is the host-log backend name. NVIDIA path logs cuda-jetson.
func LogLabel(backend string) string {
	path, _ := ResolveBackend(backend)
	if path == NVIDIAContainer {
		return NVIDIALogLabel
	}
	return strings.TrimSpace(backend)
}
