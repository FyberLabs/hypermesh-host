//go:build linux && !amd64 && !arm64

package installer

// No renameat2 number is defined here; the swap falls back to compare and rename.
const sysRenameat2 = 0
