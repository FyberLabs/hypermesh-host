package runtime

import "strings"

// Optional lease_start backend. This is a compile-time switch only.
//
// runtime stays "nvidia" for the OCI cuda-jetson path. Do not overload
// that locked field. Loaders stay oci|gguf. This image is AGX first:
// empty, cuda-jetson, and cuda-sm87 use docker --runtime=nvidia.
// Other names POST passed=false until a later image. Do not implement
// llama.cpp, Vulkan, HIP, or SSD here.
const (
	BackendCUDAJetson     = "cuda-jetson"
	BackendCUDASM87       = "cuda-sm87"
	BackendLlamaCppCPU    = "llama-cpp-cpu"
	BackendLlamaCppVulkan = "llama-cpp-vulkan"
	BackendHIPROCm        = "hip-rocm"
	BackendDiskIO         = "disk-io"
)

// NVIDIALogLabel is the log-only backend name on this AGX image.
// Heartbeat and Path B telem may print this. No tok/s or TOPS.
const NVIDIALogLabel = BackendCUDASM87

// Path says whether lease_start may call docker --runtime=nvidia.
type Path int

const (
	// NVIDIAContainer is docker --runtime=nvidia, repo@digest. Phase 1 AGX.
	NVIDIAContainer Path = iota
	// StubFail is a no-op. POST passed=false. Do not start a container.
	StubFail
)

// ResolveBackend classifies the optional lease_start backend.
// Empty, cuda-jetson, and cuda-sm87 are the existing NVIDIA path.
func ResolveBackend(backend string) (Path, string) {
	switch strings.TrimSpace(backend) {
	case "", BackendCUDAJetson, BackendCUDASM87:
		return NVIDIAContainer, ""
	case BackendLlamaCppCPU:
		return StubFail, "this image does not run CPU GGUF"
	case BackendLlamaCppVulkan:
		return StubFail, "Vulkan RADV not on this image (not 7945HX 610M)"
	case BackendHIPROCm:
		// Do not set HSA_OVERRIDE. Do not target gfx1036.
		return StubFail, "HIP/ROCm not on this image"
	case BackendDiskIO:
		// Not a Hypermesh runtime. Same fail as unknown, own reason.
		return StubFail, "not a certified runtime"
	default:
		return StubFail, "unknown backend"
	}
}

// LogLabel is the host-log backend name. NVIDIA path logs cuda-sm87.
func LogLabel(backend string) string {
	path, _ := ResolveBackend(backend)
	if path == NVIDIAContainer {
		return NVIDIALogLabel
	}
	return strings.TrimSpace(backend)
}
