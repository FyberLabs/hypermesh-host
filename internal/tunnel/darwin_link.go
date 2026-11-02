package tunnel

import (
	"context"
	"fmt"
	"net/netip"
	"runtime"
	"strconv"
	"strings"
)

// DarwinUserspaceIface is the utun unit the experimental launchd job
// creates. wireguard-go on Darwin rejects a name that is not utun[0-9]+.
const DarwinUserspaceIface = "utun8"

// UserspaceIface is the interface the agent configures. An explicit
// HYPERMESH_WG_IFACE wins. Otherwise Darwin uses utun8 and Linux uses
// hypermesh, which is the name hypermesh-wg.service passes to wireguard-go.
func UserspaceIface(configured string) string {
	return UserspaceIfaceFor(runtime.GOOS, configured)
}

// UserspaceIfaceFor is UserspaceIface with an explicit GOOS so tests on
// Linux can check the Darwin default.
func UserspaceIfaceFor(goos, configured string) string {
	if name := strings.TrimSpace(configured); name != "" {
		return name
	}
	if goos == "darwin" {
		return DarwinUserspaceIface
	}
	return defaultIface
}

// UserspaceService is the process that owns wireguard-go. The agent
// configures that interface and does not start the process.
func UserspaceService() string {
	if runtime.GOOS == "darwin" {
		return "com.fyberlabs.hypermesh-wg"
	}
	return "hypermesh-wg.service"
}

func (r *Runner) linkGOOS() string {
	if r != nil && strings.TrimSpace(r.LinkOS) != "" {
		return strings.TrimSpace(r.LinkOS)
	}
	return runtime.GOOS
}

func (r *Runner) darwinLink() bool {
	return r.linkGOOS() == "darwin"
}

func (r *Runner) errNoUserspaceIface() error {
	if r.darwinLink() {
		return fmt.Errorf("hypermesh tunnel: interface %s does not exist; load com.fyberlabs.hypermesh-wg (wireguard-go -f %s)", r.iface(), r.iface())
	}
	return fmt.Errorf("hypermesh tunnel: interface %s does not exist; enable hypermesh-wg.service", r.iface())
}

func (r *Runner) setIfaceAddr(ctx context.Context, addr string) error {
	if r.darwinLink() {
		return r.setDarwinAddr(ctx, addr)
	}
	_, err := r.runCmd(ctx, "ip", "addr", "replace", addr, "dev", r.iface())
	return err
}

// setDarwinAddr matches wg-quick's Darwin address commands. IPv4 keeps
// the prefix on the first argument and the host address as the
// destination. IPv6 is passed through as a single address.
func (r *Runner) setDarwinAddr(ctx context.Context, addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}
	if strings.Contains(addr, ":") {
		_, err := r.runCmd(ctx, "ifconfig", r.iface(), "inet6", addr, "alias")
		return err
	}
	host := addr
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		host = addr[:i]
	}
	_, err := r.runCmd(ctx, "ifconfig", r.iface(), "inet", addr, host, "alias")
	return err
}

func (r *Runner) setLinkUp(ctx context.Context, up bool) error {
	if r.darwinLink() {
		op := "up"
		if !up {
			op = "down"
		}
		_, err := r.runCmd(ctx, "ifconfig", r.iface(), op)
		return err
	}
	op := "up"
	if !up {
		op = "down"
	}
	_, err := r.runCmd(ctx, "ip", "link", "set", "dev", r.iface(), op)
	return err
}

func (r *Runner) flushAddrs(ctx context.Context) {
	if !r.darwinLink() {
		_, _ = r.runCmd(ctx, "ip", "addr", "flush", "dev", r.iface())
		return
	}
	out, err := r.runCmd(ctx, "ifconfig", r.iface())
	if err != nil {
		return
	}
	for _, addr := range parseIfconfigAddrs(string(out)) {
		if strings.Contains(addr, ":") {
			_, _ = r.runCmd(ctx, "ifconfig", r.iface(), "inet6", addr, "-alias")
			continue
		}
		_, _ = r.runCmd(ctx, "ifconfig", r.iface(), "inet", addr, "-alias")
	}
}

func (r *Runner) readIfaceAddrs(ctx context.Context) (string, error) {
	if r.darwinLink() {
		out, err := r.runCmd(ctx, "ifconfig", r.iface())
		if err != nil {
			return "", err
		}
		return strings.Join(parseIfconfigAddrs(string(out)), ","), nil
	}
	out, err := r.runCmd(ctx, "ip", "-4", "-o", "addr", "show", "dev", r.iface())
	if err != nil {
		return "", err
	}
	return strings.Join(parseIfaceAddrs(string(out)), ","), nil
}

// parseIfconfigAddrs reads `ifconfig` inet and inet6 lines. Link-local
// fe80 addresses are omitted. A zone id (`%utun8`) is stripped.
func parseIfconfigAddrs(out string) []string {
	var addrs []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] != "inet" && fields[0] != "inet6" {
			continue
		}
		addr := fields[1]
		if i := strings.IndexByte(addr, '%'); i >= 0 {
			addr = addr[:i]
		}
		if strings.HasPrefix(strings.ToLower(addr), "fe80:") {
			continue
		}
		if addr != "" {
			addrs = append(addrs, addr)
		}
	}
	return addrs
}

func (r *Runner) listDarwinRoutes(ctx context.Context) ([]devRoute, error) {
	var out []devRoute
	seen := map[string]bool{}
	for _, fam := range []struct {
		flag string
		v6   bool
	}{{"inet", false}, {"inet6", true}} {
		b, err := r.runCmd(ctx, "netstat", "-rn", "-f", fam.flag)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(b), "\n") {
			cidr, ok := parseDarwinRoute(line, r.iface(), fam.v6)
			if !ok {
				continue
			}
			key := routeKey(cidr, fam.v6)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, devRoute{cidr: cidr, v6: fam.v6})
		}
	}
	return out, nil
}

// parseDarwinRoute reads one `netstat -rn -f inet` or `inet6` line.
// The destination is kept when Netif is this interface. The kernel host
// route for the interface address (flags UH, not static) is skipped, as
// is fe80::/64. A static host route (UHS) is an AllowedIPs /32 the agent
// may have installed, so it stays.
func parseDarwinRoute(line, iface string, v6 bool) (string, bool) {
	fields := strings.Fields(line)
	if len(fields) < 4 || fields[3] != iface {
		return "", false
	}
	flags := fields[2]
	if strings.Contains(flags, "H") && !strings.Contains(flags, "S") && !strings.Contains(flags, "G") {
		return "", false
	}
	return normalizeDarwinDest(fields[0], v6)
}

func normalizeDarwinDest(dest string, v6 bool) (string, bool) {
	if strings.Contains(dest, "%") || strings.HasPrefix(strings.ToLower(dest), "fe80:") {
		return "", false
	}
	if dest == "default" {
		if v6 {
			return "::/0", true
		}
		return "0.0.0.0/0", true
	}
	if !v6 {
		return expandDarwinIPv4(dest)
	}
	if strings.Contains(dest, "/") {
		p, err := netip.ParsePrefix(dest)
		if err != nil {
			return "", false
		}
		s := p.Masked().String()
		if isLinkLocal64(s) {
			return "", false
		}
		return s, true
	}
	addr, err := netip.ParseAddr(dest)
	if err != nil || !addr.Is6() {
		return "", false
	}
	return netip.PrefixFrom(addr, 128).String(), true
}

// expandDarwinIPv4 undoes the abbreviations in Apple's netname().
// Trailing zero octets are omitted, so 10.8.0.0/24 is printed as 10.8/24.
// A mask that matches the classful mask is omitted entirely: 10 is
// 10.0.0.0/8, 172.16 is 172.16.0.0/16, and 192.168.1 and other class C/D/E
// networks are /24. 10.8.1 is class A with that /8 omitted, so the masked
// prefix is 10.0.0.0/8. Four octets and no slash is a host route from
// routename(), so it is /32.
// The result is masked, which is the prefix reconcileRoutes deletes.
func expandDarwinIPv4(dest string) (string, bool) {
	dest = strings.TrimSpace(dest)
	if dest == "" || strings.ContainsAny(dest, ":%&") {
		return "", false
	}
	addrPart := dest
	bits := -1
	if i := strings.IndexByte(dest, '/'); i >= 0 {
		addrPart = dest[:i]
		n, err := strconv.Atoi(dest[i+1:])
		if err != nil || n < 0 || n > 32 || dest[i+1:] == "" {
			return "", false
		}
		bits = n
	}
	parts := strings.Split(addrPart, ".")
	if len(parts) < 1 || len(parts) > 4 {
		return "", false
	}
	var octets [4]byte
	for i, part := range parts {
		if part == "" {
			return "", false
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 255 {
			return "", false
		}
		octets[i] = byte(n)
	}
	if bits < 0 {
		if len(parts) == 4 {
			bits = 32
		} else {
			bits = darwinClassfulBits(octets[0])
		}
	}
	return netip.PrefixFrom(netip.AddrFrom4(octets), bits).Masked().String(), true
}

// darwinClassfulBits is forgemask() in Apple's netstat: class A /8,
// class B /16, and class C, D, and E /24.
func darwinClassfulBits(first byte) int {
	switch {
	case first&0x80 == 0:
		return 8
	case first&0xc0 == 0x80:
		return 16
	default:
		return 24
	}
}

func (r *Runner) darwinRoute(ctx context.Context, v6 bool, op, cidr string) error {
	fam := "-inet"
	if v6 {
		fam = "-inet6"
	}
	switch op {
	case "replace":
		if _, err := r.runCmd(ctx, "route", "-q", "-n", "change", fam, cidr, "-interface", r.iface()); err == nil {
			return nil
		}
		_, err := r.runCmd(ctx, "route", "-q", "-n", "add", fam, cidr, "-interface", r.iface())
		return err
	case "del":
		_, err := r.runCmd(ctx, "route", "-q", "-n", "delete", fam, cidr, "-interface", r.iface())
		return err
	default:
		return fmt.Errorf("hypermesh tunnel: unknown route op %s", op)
	}
}
