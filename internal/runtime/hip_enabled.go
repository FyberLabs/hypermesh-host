//go:build rocm

package runtime

// hipImageEnabled is compile-time: this host image was built with -tags rocm.
// The default AGX / Jetson image is built without that tag and stub-fails
// hip-rocm. A future ROCm image enables the path; see deploy/rocm/README.md.
func hipImageEnabled() bool { return true }
