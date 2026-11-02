package tunnel

import (
	"context"
	"fmt"
	"net/netip"
	"runtime"
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
	if err != nil || addr.Is6() != v6 {
		return "", false
	}
	bits := 32
	if addr.Is6() {
		bits = 128
	}
	return netip.PrefixFrom(addr, bits).String(), true
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
