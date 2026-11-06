//go:build linux || darwin

package agent

import (
	"os"
	"syscall"
	"unsafe"
)

func stdinIsTerminal() bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, os.Stdin.Fd(), ioctlReadTermios, uintptr(unsafe.Pointer(&termios)), 0, 0, 0)
	return errno == 0
}
