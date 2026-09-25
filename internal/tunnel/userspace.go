package tunnel

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// recentHandshakeWindow is how long a completed handshake still counts as
// live. WireGuard drops a session after 180s without a new handshake.
const recentHandshakeWindow = 180 * time.Second

// UserspaceSelected reports whether the agent should configure an interface
// owned by hypermesh-wg.service instead of running wg-quick.
//
// mode is HYPERMESH_WG_USERSPACE: "1"/"true"/"userspace" forces the unit,
// "0"/"false"/"kernel" forces wg-quick, "auto" uses the kernel when the
// wireguard module is loaded or loadable. Any other value, including empty,
// is the kernel path so tests that leave the field unset stay on wg-quick.
func UserspaceSelected(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "1", "true", "yes", "on", "userspace":
		return true
	case "0", "false", "no", "off", "kernel":
		return false
	case "auto":
		return !KernelAvailable()
	default:
		return false
	}
}

// ValidWGMode reports whether mode is accepted by Config.Validate.
func ValidWGMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "auto", "0", "1", "true", "false", "yes", "no", "on", "off", "userspace", "kernel":
		return true
	default:
		return false
	}
}

// NewRunnerMode is NewRunner with Userspace set from mode.
func NewRunnerMode(mode string) *Runner {
	r := NewRunner()
	r.Userspace = UserspaceSelected(mode)
	return r
}

// KernelAvailable is true when the wireguard kernel module is already
// loaded or modprobe can load it. A missing modprobe means userspace.
func KernelAvailable() bool {
	if _, err := os.Stat("/sys/module/wireguard"); err == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "modprobe", "-n", "-q", "wireguard").Run(); err == nil {
		return true
	}
	return false
}

// OnSyncError runs when the plane fetch fails (for example HTTP 401 or 503).
// A userspace interface that already has a recent handshake is left as it is.
// This never removes peers, never sets the link down, and never starts wireguard-go.
// The bool is true when a handshaking interface was left up.
func (r *Runner) OnSyncError(ctx context.Context) (bool, error) {
	if !r.Userspace {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	live, err := r.inspect(ctx)
	if err != nil {
		return false, nil
	}
	if !live.handshaking(r.now()) {
		return false, nil
	}
	return true, nil
}

func (r *Runner) applyUserspace(ctx context.Context, sess Session) error {
	if err := os.MkdirAll(r.configDir(), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(r.configDir(), 0o700); err != nil {
		return err
	}
	live, err := r.inspect(ctx)
	if err != nil {
		return err
	}
	if !live.Exists {
		return fmt.Errorf("hypermesh tunnel: interface %s does not exist; enable hypermesh-wg.service", r.iface())
	}
	if r.liveMatches(ctx, live, sess) {
		// Desired peer is already on the link. Do not syncconf or set the
		// link down; either would drop a live handshake. Routes are still
		// reconciled: wg-quick installed them, syncconf does not.
		if err := r.reconcileRoutes(ctx, sess.AllowedIPs); err != nil {
			return err
		}
		if r.alreadyApplied(sess) {
			return nil
		}
		if !r.confMatchesPeer(sess) {
			if err := r.writeQuickConf(sess); err != nil {
				return err
			}
		}
		return r.saveApplied(PeerFingerprint(sess))
	}
	if err := r.writeQuickConf(sess); err != nil {
		return err
	}
	syncPath := filepath.Join(r.configDir(), r.iface()+".sync")
	if err := os.WriteFile(syncPath, []byte(wgSetConfig(sess, live.ListenPort)), 0o600); err != nil {
		return err
	}
	defer func() { _ = shredFile(syncPath) }()
	if _, err := r.runCmd(ctx, "wg", "syncconf", r.iface(), syncPath); err != nil {
		return err
	}
	if addr := strings.TrimSpace(sess.ClientAddress); addr != "" {
		if _, err := r.runCmd(ctx, "ip", "addr", "replace", addr, "dev", r.iface()); err != nil {
			return err
		}
	}
	if _, err := r.runCmd(ctx, "ip", "link", "set", "dev", r.iface(), "up"); err != nil {
		return err
	}
	if err := r.reconcileRoutes(ctx, sess.AllowedIPs); err != nil {
		return err
	}
	return r.saveApplied(PeerFingerprint(sess))
}

// teardownUserspace removes every peer and sets the link down.
// hypermesh-wg.service keeps running, so the interface device stays, but
// with no peer and the link down it cannot handshake. The agent brings
// the link back only when a later allocated session is applied.
func (r *Runner) teardownUserspace(ctx context.Context) error {
	live, err := r.inspect(ctx)
	if err != nil {
		return err
	}
	if live.Exists {
		if err := r.deleteDevRoutes(ctx); err != nil {
			return err
		}
		for _, p := range live.Peers {
			if strings.TrimSpace(p.PublicKey) == "" || p.PublicKey == "(none)" {
				continue
			}
			if _, err := r.runCmd(ctx, "wg", "set", r.iface(), "peer", p.PublicKey, "remove"); err != nil {
				return err
			}
		}
		// Flush is best-effort: an address-less link still has to go down.
		_, _ = r.runCmd(ctx, "ip", "addr", "flush", "dev", r.iface())
		if _, err := r.runCmd(ctx, "ip", "link", "set", "dev", r.iface(), "down"); err != nil {
			return err
		}
	}
	shredErr := shredFile(r.confPath())
	_ = os.Remove(r.appliedPath())
	_ = shredFile(filepath.Join(r.configDir(), r.iface()+".sync"))
	return shredErr
}

func (r *Runner) writeQuickConf(sess Session) error {
	body := []byte(wgQuickConfig(sess))
	if err := os.WriteFile(r.confPath(), body, 0o600); err != nil {
		return err
	}
	return os.Chmod(r.confPath(), 0o600)
}

type liveIface struct {
	Exists     bool
	PrivateKey string
	ListenPort int
	Address    string
	Peers      []livePeer
}

type livePeer struct {
	PublicKey  string
	Endpoint   string
	AllowedIPs string
	Keepalive  int
	Handshake  time.Time
}

func (l liveIface) handshaking(now time.Time) bool {
	if !l.Exists {
		return false
	}
	for _, p := range l.Peers {
		if p.recent(now) {
			return true
		}
	}
	return false
}

func (p livePeer) recent(now time.Time) bool {
	if p.Handshake.IsZero() || p.Handshake.Unix() <= 0 {
		return false
	}
	age := now.Sub(p.Handshake)
	return age >= -30*time.Second && age <= recentHandshakeWindow
}

// liveMatches is true when the live interface already has the desired host peer.
// tunnel_id and lease_id are not compared. A recent handshake is not required;
// an identical config must not be rewritten.
//
// wg show dump does not echo the config text. Keys are re-encoded, the endpoint
// is the resolved numeric address, and AllowedIPs are comma-joined with no
// spaces in trie order. Those differences are the same peer.
func (r *Runner) liveMatches(ctx context.Context, l liveIface, s Session) bool {
	if !l.Exists || len(l.Peers) != 1 {
		return false
	}
	if strings.TrimSpace(l.PrivateKey) == "" || l.PrivateKey == "(none)" {
		return false
	}
	if !sameWGKey(l.PrivateKey, s.PeerPrivateKey) {
		return false
	}
	if addr := strings.TrimSpace(s.ClientAddress); addr != "" && !sameAddr(l.Address, addr) {
		return false
	}
	p := l.Peers[0]
	if !sameWGKey(p.PublicKey, s.ServerPublicKey) {
		return false
	}
	if !r.endpointsMatch(ctx, s.Endpoint, p.Endpoint) {
		return false
	}
	if !sameAllowed(p.AllowedIPs, s.AllowedIPs) {
		return false
	}
	if p.Keepalive != persistentKeepaliveSec {
		return false
	}
	return true
}

func sameWGKey(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" || a == "(none)" || b == "(none)" {
		return false
	}
	if a == b {
		return true
	}
	ab, aok := decodeWGKey(a)
	bb, bok := decodeWGKey(b)
	if !aok || !bok {
		return false
	}
	return subtle.ConstantTimeCompare(ab, bb) == 1
}

func decodeWGKey(s string) ([]byte, bool) {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		raw, err := enc.DecodeString(s)
		if err == nil && len(raw) == 32 {
			return raw, true
		}
	}
	return nil, false
}

func sameAddr(live, want string) bool {
	live = strings.TrimSpace(live)
	want = strings.TrimSpace(want)
	if live == want {
		return true
	}
	lp, e1 := netip.ParsePrefix(live)
	wp, e2 := netip.ParsePrefix(want)
	if e1 != nil || e2 != nil {
		return false
	}
	return lp.String() == wp.String()
}

func sameAllowed(live, want string) bool {
	a := canonPrefixes(live)
	b := canonPrefixes(want)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func canonPrefixes(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "(none)" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			part = p.String()
		}
		out = append(out, part)
	}
	sort.Strings(out)
	return out
}

// endpointsMatch treats the numeric address from `wg show dump` as the same
// endpoint as the hostname the control plane sent, when that name resolves
// to the address. Ports must match. A different address is a real change.
func (r *Runner) endpointsMatch(ctx context.Context, desired, live string) bool {
	desired = strings.TrimSpace(desired)
	live = strings.TrimSpace(live)
	if desired == live {
		return true
	}
	dh, dp, err1 := net.SplitHostPort(desired)
	lh, lp, err2 := net.SplitHostPort(live)
	if err1 != nil || err2 != nil || dp != lp {
		return false
	}
	if strings.EqualFold(dh, lh) {
		return true
	}
	dip := net.ParseIP(dh)
	lip := net.ParseIP(lh)
	if dip != nil && lip != nil {
		return dip.Equal(lip)
	}
	if dip != nil || lip == nil {
		return false
	}
	ips, err := r.lookupHost(ctx, dh)
	if err != nil {
		return false
	}
	for _, ip := range ips {
		if net.ParseIP(ip).Equal(lip) {
			return true
		}
	}
	return false
}

func (r *Runner) lookupHost(ctx context.Context, host string) ([]string, error) {
	if r.LookupHost != nil {
		return r.LookupHost(ctx, host)
	}
	return net.DefaultResolver.LookupHost(ctx, host)
}

func (r *Runner) inspect(ctx context.Context) (liveIface, error) {
	out, err := r.runCmd(ctx, "wg", "show", r.iface(), "dump")
	if err != nil {
		if missingIface(err) {
			return liveIface{}, nil
		}
		return liveIface{}, err
	}
	live, err := parseWGDump(string(out))
	if err != nil {
		return liveIface{}, err
	}
	live.Exists = true
	addrOut, addrErr := r.runCmd(ctx, "ip", "-4", "-o", "addr", "show", "dev", r.iface())
	if addrErr == nil {
		live.Address = parseIfaceAddr(string(addrOut))
	}
	return live, nil
}

func parseWGDump(text string) (liveIface, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return liveIface{Exists: true}, nil
	}
	lines := strings.Split(text, "\n")
	fields := strings.Split(lines[0], "\t")
	if len(fields) < 4 {
		return liveIface{}, errors.New("hypermesh tunnel: unexpected wg dump")
	}
	port, _ := strconv.Atoi(strings.TrimSpace(fields[2]))
	live := liveIface{Exists: true, PrivateKey: strings.TrimSpace(fields[0]), ListenPort: port}
	for _, line := range lines[1:] {
		p := strings.Split(line, "\t")
		if len(p) < 8 {
			return liveIface{}, errors.New("hypermesh tunnel: unexpected wg dump")
		}
		hs, _ := strconv.ParseInt(strings.TrimSpace(p[4]), 10, 64)
		ka := 0
		if tail := strings.TrimSpace(p[7]); tail != "" && tail != "off" {
			ka, _ = strconv.Atoi(tail)
		}
		live.Peers = append(live.Peers, livePeer{
			PublicKey:  strings.TrimSpace(p[0]),
			Endpoint:   strings.TrimSpace(p[2]),
			AllowedIPs: strings.TrimSpace(p[3]),
			Keepalive:  ka,
			Handshake:  time.Unix(hs, 0),
		})
	}
	return live, nil
}

func parseIfaceAddr(out string) string {
	fields := strings.Fields(out)
	for i, f := range fields {
		if f == "inet" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) runCmd(ctx context.Context, name string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if refusesUserspaceSpawn(name, args) {
		return nil, errors.New("hypermesh tunnel: refusing to spawn wireguard-go")
	}
	if r.Run != nil {
		return r.Run(ctx, name, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = withoutUserspaceWG(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		// wg prints "No such device" on stderr. Do not fold that output
		// into the error: other failures can echo key material.
		if missingIface(err) || missingIfaceText(out) {
			return nil, fmt.Errorf("hypermesh tunnel: %s: no such device", name)
		}
		return nil, fmt.Errorf("hypermesh tunnel: %s %s failed: %w", name, subcommand(args), err)
	}
	return out, nil
}

func refusesUserspaceSpawn(name string, args []string) bool {
	if strings.Contains(name, "wireguard-go") {
		return true
	}
	for _, a := range args {
		if strings.Contains(a, "wireguard-go") {
			return true
		}
	}
	return false
}

func subcommand(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func missingIfaceText(out []byte) bool {
	return missingIface(errors.New(strings.ToLower(string(out))))
}

func withoutUserspaceWG(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, "WG_QUICK_USERSPACE_IMPLEMENTATION=") {
			continue
		}
		out = append(out, e)
	}
	return out
}

// wgSetConfig is the wg syncconf file. Address is not valid here; ip addr sets it.
//
// ListenPort is pinned to the port the interface already bound. wireguard-tools
// sets WGDEVICE_HAS_LISTEN_PORT even when the file omits the key, then sends
// listen_port=0. wireguard-go treats 0 as "bind a new port", which drops the
// handshake. A port of 0 means the process has not bound yet; omit the line.
func wgSetConfig(s Session, listenPort int) string {
	var b strings.Builder
	b.WriteString("[Interface]\n")
	b.WriteString("PrivateKey = ")
	b.WriteString(s.PeerPrivateKey)
	b.WriteString("\n")
	if listenPort > 0 && listenPort <= 65535 {
		fmt.Fprintf(&b, "ListenPort = %d\n", listenPort)
	}
	b.WriteString("\n[Peer]\n")
	if s.ServerPublicKey != "" {
		b.WriteString("PublicKey = ")
		b.WriteString(s.ServerPublicKey)
		b.WriteString("\n")
	}
	if s.Endpoint != "" {
		b.WriteString("Endpoint = ")
		b.WriteString(s.Endpoint)
		b.WriteString("\n")
	}
	if s.AllowedIPs != "" {
		b.WriteString("AllowedIPs = ")
		b.WriteString(s.AllowedIPs)
		b.WriteString("\n")
	}
	b.WriteString(persistentKeepaliveLine())
	b.WriteString("\n")
	return b.String()
}

// wantedRoutes is the AllowedIPs prefixes to install on the interface.
// 0.0.0.0/0 and ::/0 are omitted unless InstallDefaultRoute is set. Installing
// them would replace the LAN default route (on AGX64-1, 192.168.6.1 dev end0)
// that reaches the control plane and the WireGuard endpoint.
func wantedRoutes(allowed string, installDefault bool) ([]string, error) {
	allowed = strings.TrimSpace(allowed)
	if allowed == "" || allowed == "(none)" {
		return nil, nil
	}
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(allowed, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		p, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("hypermesh tunnel: allowed ip %q: %w", part, err)
		}
		s := p.String()
		if !installDefault && (s == "0.0.0.0/0" || s == "::/0") {
			continue
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out, nil
}

type devRoute struct {
	cidr string
	v6   bool
}

func (r *Runner) reconcileRoutes(ctx context.Context, allowed string) error {
	want, err := wantedRoutes(allowed, r.InstallDefaultRoute)
	if err != nil {
		return err
	}
	have, err := r.listDevRoutes(ctx)
	if err != nil {
		return err
	}
	wantSet := map[string]bool{}
	for _, cidr := range want {
		wantSet[cidr] = true
		if err := r.ipRoute(ctx, strings.Contains(cidr, ":"), "replace", cidr); err != nil {
			return err
		}
	}
	for _, h := range have {
		if wantSet[routeKey(h.cidr, h.v6)] {
			continue
		}
		if err := r.ipRoute(ctx, h.v6, "del", h.cidr); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) deleteDevRoutes(ctx context.Context) error {
	have, err := r.listDevRoutes(ctx)
	if err != nil {
		return err
	}
	for _, h := range have {
		if err := r.ipRoute(ctx, h.v6, "del", h.cidr); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) listDevRoutes(ctx context.Context) ([]devRoute, error) {
	var out []devRoute
	for _, v6 := range []bool{false, true} {
		args := []string{"route", "show", "dev", r.iface()}
		if v6 {
			args = append([]string{"-6"}, args...)
		}
		b, err := r.runCmd(ctx, "ip", args...)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(b), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			cidr := fields[0]
			if cidr != "default" && !strings.Contains(cidr, "/") {
				continue
			}
			out = append(out, devRoute{cidr: cidr, v6: v6})
		}
	}
	return out, nil
}

func (r *Runner) ipRoute(ctx context.Context, v6 bool, op, cidr string) error {
	args := []string{"route", op, cidr, "dev", r.iface()}
	if v6 {
		args = append([]string{"-6"}, args...)
	}
	_, err := r.runCmd(ctx, "ip", args...)
	return err
}

func routeKey(cidr string, v6 bool) string {
	if cidr == "default" {
		if v6 {
			return "::/0"
		}
		return "0.0.0.0/0"
	}
	if p, err := netip.ParsePrefix(cidr); err == nil {
		return p.String()
	}
	return cidr
}
