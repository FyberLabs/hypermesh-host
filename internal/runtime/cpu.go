package runtime

import "strings"

// CPU uses the same digest-pinned, read-only, network-isolated model container
// as CUDA, without accelerator passthrough. It is an explicit backend choice,
// never an automatic fallback for a requested accelerator.
func NewCPU() *NVIDIA { return &NVIDIA{Docker: "docker", cpu: true} }

func CPUStartArgs(spec Spec, ref string) []string {
	args := StartArgs(spec, ref)
	result := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--runtime=nvidia" {
			continue
		}
		if arg == llamaServerCommand {
			arg = strings.Replace(arg, "exec llama-server ", "exec llama-server --n-gpu-layers 0 ", 1)
		}
		result = append(result, arg)
	}
	return result
}
