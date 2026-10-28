//go:build !linux || !cpu

package runtime

func CPUImageEnabled() bool { return false }
