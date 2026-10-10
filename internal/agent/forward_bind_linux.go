//go:build linux

package agent

import (
	"context"
	"fmt"
	"net"
	"syscall"
)

// listenOnDevice listens on addr and binds the socket to the named network
// device with SO_BINDTODEVICE. An address bind alone still accepts packets
// that reach the physical NIC with the tunnel address as destination. A
// device bind accepts only traffic that arrived on iface. It fails when the
// device does not exist or cannot be bound, and never falls back to an
// address-only listener.
func listenOnDevice(addr, iface string) (net.Listener, error) {
	lc := net.ListenConfig{
		Control: func(_, _ string, c syscall.RawConn) error {
			var serr error
			if err := c.Control(func(fd uintptr) {
				serr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, iface)
			}); err != nil {
				return err
			}
			if serr != nil {
				return fmt.Errorf("bind to device %s: %w", iface, serr)
			}
			return nil
		},
	}
	return lc.Listen(context.Background(), "tcp", addr)
}
