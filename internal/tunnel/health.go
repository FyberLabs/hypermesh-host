package tunnel

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Tunnel state strings for host health. up is a recent handshake.
// handshaking is a configured peer without a recent handshake, or a
// userspace process that has not created the interface yet.
// down is an interface with no peer, or a kernel interface that is absent.
// disabled is userspace mode with no wireguard-go process and no interface.
const (
	StateUp          = "up"
	StateHandshaking = "handshaking"
	StateDown        = "down"
	StateDisabled    = "disabled"
)

// Snapshot is tunnel health safe to send off the box.
// It has no private key, public key, or endpoint.
type Snapshot struct {
	State         string
	HandshakeAgeS *int
	ListenPort    *int
	PID           *int
}

// Snapshot reads the existing interface through the same wg show dump
// path Apply uses. It does not spawn wireguard-go and does not log dump
// output (that output contains the private key).
func (r *Runner) Snapshot(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	live, err := r.inspect(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	pid, pidOK := r.wireguardPID()
	out := Snapshot{State: tunnelState(live, r.now(), r.Userspace, pidOK)}
	if pidOK && pid > 0 {
		p := pid
		out.PID = &p
	}
	if live.Exists {
		port := live.ListenPort
		out.ListenPort = &port
		if age, ok := handshakeAge(live, r.now()); ok {
			a := age
			out.HandshakeAgeS = &a
		}
	}
	return out, nil
}

func (r *Runner) wireguardPID() (int, bool) {
	if r.FindPID != nil {
		return r.FindPID(r.iface())
	}
	if !r.Userspace {
		return 0, false
	}
	return findWireGuardPID(r.iface())
}

func tunnelState(live liveIface, now time.Time, userspace, pidOK bool) string {
	if !live.Exists {
		if userspace && pidOK {
			return StateHandshaking
		}
		if userspace {
			return StateDisabled
		}
		return StateDown
	}
	if !configuredPeer(live) {
		return StateDown
	}
	if live.handshaking(now) {
		return StateUp
	}
	return StateHandshaking
}

func configuredPeer(live liveIface) bool {
	for _, p := range live.Peers {
		k := strings.TrimSpace(p.PublicKey)
		if k != "" && k != "(none)" {
			return true
		}
	}
	return false
}

func handshakeAge(live liveIface, now time.Time) (int, bool) {
	var latest time.Time
	for _, p := range live.Peers {
		if p.Handshake.IsZero() || p.Handshake.Unix() <= 0 {
			continue
		}
		if latest.IsZero() || p.Handshake.After(latest) {
			latest = p.Handshake
		}
	}
	if latest.IsZero() {
		return 0, false
	}
	delta := now.Sub(latest)
	if delta < 0 {
		if delta < -30*time.Second {
			return 0, false
		}
		return 0, true
	}
	return int(delta.Seconds()), true
}

// findWireGuardPID scans /proc for wireguard-go serving iface.
// The command line is not logged. Kernel WireGuard has no such process.
func findWireGuardPID(iface string) (int, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false
	}
	best := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		raw, err := readCmdline(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || !cmdlineIsWireGuard(raw, iface) {
			continue
		}
		if best == 0 || pid < best {
			best = pid
		}
	}
	if best == 0 {
		return 0, false
	}
	return best, true
}

func readCmdline(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	if n == 0 {
		return nil, err
	}
	return buf[:n], nil
}

func cmdlineIsWireGuard(raw []byte, iface string) bool {
	if len(raw) == 0 || iface == "" {
		return false
	}
	parts := bytes.Split(raw, []byte{0})
	base := filepath.Base(string(parts[0]))
	if base != "wireguard-go" {
		return false
	}
	for _, p := range parts[1:] {
		if string(p) == iface {
			return true
		}
	}
	return false
}
