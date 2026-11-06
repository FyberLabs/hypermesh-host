//go:build linux

package agent

// TCGETS from asm-generic/ioctls.h. The same value is used on amd64 and arm64.
const ioctlReadTermios = 0x5401
