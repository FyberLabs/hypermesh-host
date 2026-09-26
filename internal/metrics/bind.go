package metrics

import (
	"fmt"
	"net"
	"strings"
)

// DefaultPort is the /metrics port when the process derives the address.
const DefaultPort = "9464"

// ResolveAddr chooses the /metrics listen address.
// override is HYPERMESH_METRICS_ADDR. Empty means derive: the WireGuard
// address on port 9464, or 127.0.0.1:9464 when tunnelHost is empty.
// An explicit override is accepted only for a literal loopback address,
// or for tunnelHost when that host is a specific private address.
// A refused override returns the derived address and refused=true.
// The result is never 0.0.0.0, ::, or a public address.
func ResolveAddr(override, tunnelHost string) (addr string, refused bool) {
	tunnelHost = hostOnly(tunnelHost)
	derived := DefaultAddr
	if ip := net.ParseIP(tunnelHost); usableTunnelIP(ip) {
		derived = net.JoinHostPort(ip.String(), DefaultPort)
	}
	override = strings.TrimSpace(override)
	if override == "" {
		return derived, false
	}
	if err := AuthorizeBind(override, tunnelHost); err != nil {
		return derived, true
	}
	return override, false
}

// AuthorizeBind accepts a literal loopback address, or tunnelHost when
// that host is a specific private unicast address. Unspecified addresses,
// public addresses, hostnames, and any other private address are refused.
// The check does not open a socket.
func AuthorizeBind(addr, tunnelHost string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return fmt.Errorf("hypermesh metrics: refusing %s: %w", addr, err)
	}
	if port == "" {
		return fmt.Errorf("hypermesh metrics: refusing %s: missing port", addr)
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("hypermesh metrics: refusing %s: not a specific loopback or WireGuard address", addr)
	}
	if ip.IsLoopback() {
		return nil
	}
	if !usableTunnelIP(ip) {
		return fmt.Errorf("hypermesh metrics: refusing %s: public or non-tunnel address", addr)
	}
	want := net.ParseIP(hostOnly(tunnelHost))
	if !usableTunnelIP(want) || !ip.Equal(want) {
		return fmt.Errorf("hypermesh metrics: refusing %s: not the WireGuard address", addr)
	}
	return nil
}

// usableTunnelIP is a specific private or link-local unicast address.
// Loopback, unspecified, multicast, and public addresses are not.
func usableTunnelIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLoopback() {
		return false
	}
	return ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

func hostOnly(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if host, _, ok := strings.Cut(raw, "/"); ok {
		raw = host
	}
	if h, _, err := net.SplitHostPort(raw); err == nil {
		raw = h
	}
	return strings.Trim(raw, "[]")
}

// IfaceAddr returns the first private IPv4 address on name, else a private
// IPv6 address. An unknown interface or a public address returns "".
// name empty uses hypermesh. This does not log interface output.
func IfaceAddr(name string) string {
	if strings.TrimSpace(name) == "" {
		name = "hypermesh"
	}
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return ""
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		return ""
	}
	var v4, v6 string
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if !usableTunnelIP(ip) {
			continue
		}
		if ip.To4() != nil {
			if v4 == "" {
				v4 = ip.To4().String()
			}
			continue
		}
		if v6 == "" {
			v6 = ip.String()
		}
	}
	if v4 != "" {
		return v4
	}
	return v6
}
