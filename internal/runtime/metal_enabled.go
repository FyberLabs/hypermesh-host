//go:build darwin

package runtime

// metalImageEnabled is true on a darwin binary. llama-cpp-cpu then
// resolves to the Metal host runner (lab / cert-limits). The CP enum
// stays llama-cpp-cpu. Jetson Linux builds use metal_enabled_default.go.
func metalImageEnabled() bool { return true }
