package tunnel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	if live.matches(sess) {
		// Desired config is already on the link. Do not syncconf or
		// set the link down; either would drop a live handshake.
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
	if err := os.WriteFile(syncPath, []byte(wgSetConfig(sess)), 0o600); err != nil {
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

// matches is true when the live interface already has the desired host peer.
// tunnel_id and lease_id are not compared. A recent handshake is not required;
// an identical config must not be rewritten.
func (l liveIface) matches(s Session) bool {
	if !l.Exists || len(l.Peers) != 1 {
		return false
	}
	if strings.TrimSpace(l.PrivateKey) == "" || l.PrivateKey == "(none)" {
		return false
	}
	if l.PrivateKey != strings.TrimSpace(s.PeerPrivateKey) {
		return false
	}
	if addr := strings.TrimSpace(s.ClientAddress); addr != "" && l.Address != addr {
		return false
	}
	p := l.Peers[0]
	if p.PublicKey != strings.TrimSpace(s.ServerPublicKey) {
		return false
	}
	if p.Endpoint != strings.TrimSpace(s.Endpoint) {
		return false
	}
	if p.AllowedIPs != strings.TrimSpace(s.AllowedIPs) {
		return false
	}
	if p.Keepalive != persistentKeepaliveSec {
		return false
	}
	return true
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
	live := liveIface{Exists: true, PrivateKey: fields[0]}
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
			PublicKey:  p[0],
			Endpoint:   p[2],
			AllowedIPs: p[3],
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
func wgSetConfig(s Session) string {
	var b strings.Builder
	b.WriteString("[Interface]\n")
	b.WriteString("PrivateKey = ")
	b.WriteString(s.PeerPrivateKey)
	b.WriteString("\n\n[Peer]\n")
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
