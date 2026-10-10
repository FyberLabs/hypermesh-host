//go:build !linux

package agent

import (
	"fmt"
	"net"
)

// listenOnDevice has no device bind on this OS, so lease ingress fails
// closed instead of listening on the tunnel address alone.
func listenOnDevice(_, iface string) (net.Listener, error) {
	return nil, fmt.Errorf("binding lease ingress to device %s is not supported on this OS", iface)
}
