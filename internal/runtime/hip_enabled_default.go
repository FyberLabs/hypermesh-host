//go:build !rocm

package runtime

// hipImageEnabled is false on the default AGX / Jetson image.
// ResolveBackend("hip-rocm") stays StubFail with FailReason.
func hipImageEnabled() bool { return false }
