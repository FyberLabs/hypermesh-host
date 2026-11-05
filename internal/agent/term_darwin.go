//go:build darwin

package agent

// TIOCGETA. The same value is used on Darwin amd64 and arm64.
const ioctlReadTermios = 0x40487413
