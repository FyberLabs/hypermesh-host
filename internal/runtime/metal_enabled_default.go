//go:build !darwin

package runtime

// metalImageEnabled is false on Jetson / Linux. llama-cpp-cpu stub-fails.
func metalImageEnabled() bool { return false }
