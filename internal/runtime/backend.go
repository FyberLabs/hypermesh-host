package runtime

import "strings"

// lease_start backend enum (host-runtimes.md). runtime stays "nvidia"
// for oci cuda-jetson. Do not overload that locked field. Loaders stay
// oci|gguf. disk-io is not in this enum (unknown → fail).
// llama-cpp-metal is not in this enum (unknown → FailReason).
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

// MetalLogLabel is the CP enum name used when the darwin Metal runner
// is selected. The enum stays llama-cpp-cpu; this is not CUDA and not
// a new llama-cpp-metal value.
const MetalLogLabel = BackendLlamaCppCPU

// MetalRunnerLabel is the host-log runner name on darwin (lab only).
const MetalRunnerLabel = "metal"

// Path says which start path this image may take. NVIDIAContainer is
// the AGX default. HIPROCm and LlamaCppHost exist only when this
// binary's build tag / GOOS says the image supports them.
type Path int

const (
	// NVIDIAContainer is docker --runtime=nvidia, repo@digest. Phase 1 AGX.
	NVIDIAContainer Path = iota
	// StubFail is a compile-time no-op. POST passed=false. Do not start work.
	StubFail
	// HIPROCm is the AMD ROCm scaffold (16GB+ class first). Only when
	// this image was built with -tags rocm. Do not set HSA_OVERRIDE.
	HIPROCm
	// LlamaCppHost is llama-cpp-cpu on a darwin binary (Metal). Jetson
	// Linux builds still stub llama-cpp-cpu.
	LlamaCppHost
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
// hip-rocm is HIPROCm only on a -tags rocm image; the default AGX /
// Jetson image stub-fails with FailReason. llama-cpp-cpu is LlamaCppHost
// only on darwin; Jetson Linux stub-fails. llama-cpp-vulkan stays stub
// on every image. Unknown (including disk-io, llama-cpp-metal) fails.
// Do not set HSA_OVERRIDE.
func ResolveBackend(backend string) (Path, string) {
	name := strings.TrimSpace(backend)
	switch name {
	case "", BackendCUDAJetson, BackendCUDASM87:
		return NVIDIAContainer, ""
	case BackendHIPROCm:
		if hipImageEnabled() {
			return HIPROCm, ""
		}
		return StubFail, FailReason(name)
	case BackendLlamaCppCPU:
		if metalImageEnabled() {
			return LlamaCppHost, ""
		}
		return StubFail, FailReason(name)
	default:
		return StubFail, FailReason(name)
	}
}

// Runnable is a start path this image may take (not StubFail).
func (p Path) Runnable() bool {
	switch p {
	case NVIDIAContainer, HIPROCm, LlamaCppHost:
		return true
	default:
		return false
	}
}

// LogLabel is the host-log backend name. NVIDIA path logs cuda-jetson.
// HIP logs hip-rocm. Darwin Metal logs llama-cpp-cpu (enum unchanged).
func LogLabel(backend string) string {
	path, _ := ResolveBackend(backend)
	switch path {
	case NVIDIAContainer:
		return NVIDIALogLabel
	case HIPROCm:
		return BackendHIPROCm
	case LlamaCppHost:
		return MetalLogLabel
	default:
		return strings.TrimSpace(backend)
	}
}

// HostLogLabel is this binary's default host-image name. AGX stays
// cuda-jetson. A -tags rocm image logs hip-rocm. darwin logs
// llama-cpp-cpu. No tok/s. TOPS is not tok/s.
func HostLogLabel() string {
	if hipImageEnabled() {
		return BackendHIPROCm
	}
	if metalImageEnabled() {
		return MetalLogLabel
	}
	return NVIDIALogLabel
}

// HIPImageEnabled is true only for a host image built with -tags rocm.
func HIPImageEnabled() bool { return hipImageEnabled() }

// MetalImageEnabled is true only for a darwin binary.
func MetalImageEnabled() bool { return metalImageEnabled() }
