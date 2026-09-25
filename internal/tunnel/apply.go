// Package tunnel applies a WireGuard (or equivalent) session allocated
// by the control plane. One peer per host, not per lease. No custom overlay.
package tunnel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	appliedFile  = "hypermesh.applied"
	defaultIface = "hypermesh"
	// persistentKeepaliveSec keeps NAT mappings warm on Path B cottage hosts.
	persistentKeepaliveSec = 25
)

var (
	// ErrNoSession means GET /agent/tunnel returned nothing to apply.
	ErrNoSession = errors.New("hypermesh tunnel: no session")
	// ErrNotAllocated means the session is not status=allocated.
	ErrNotAllocated = errors.New("hypermesh tunnel: session is not allocated")
	// ErrMissingPrivateKey means Apply was given no local host private key.
	// The control plane does not send one (Panopticon #128).
	ErrMissingPrivateKey = errors.New("hypermesh tunnel: local WireGuard private key missing")
)

// Session is the allocated host peer. PeerPrivateKey is local, never from GET.
// TunnelID and LeaseID are tickets on the host peer, not a new peer.
type Session struct {
	TunnelID        string
	LeaseID         string
	Status          string
	Endpoint        string
	ServerPublicKey string
	PeerPrivateKey  string
	AllowedIPs      string
	ClientAddress   string
}

// Applier installs or tears down the local WireGuard interface.
type Applier interface {
	Apply(ctx context.Context, sess Session) error
	Teardown(ctx context.Context) error
}

// Runner writes a wg-quick config and runs `wg-quick up|down` on kernel
// hosts. Userspace hosts leave wireguard-go to hypermesh-wg.service and
// only configure the existing interface.
type Runner struct {
	// Iface is the WireGuard interface name (default hypermesh, ≤15 chars).
	Iface string
	// ConfigDir holds <iface>.conf at mode 0600. The file contains the peer key.
	ConfigDir string
	// Quick overrides wg-quick. Args are ("up"|"down", configPath).
	// Kernel mode only. Userspace mode never calls it.
	Quick func(ctx context.Context, args ...string) error
	// Userspace is true when hypermesh-wg.service owns wireguard-go.
	// The agent configures that interface and never spawns the process.
	Userspace bool
	// Run overrides wg and ip. Tests fake the UAPI and address commands.
	// Output can contain the private key and must not be logged.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// Now is the clock for handshake age. Nil uses time.Now.
	Now func() time.Time
}

// NewRunner uses /etc/wireguard and wg-quick on PATH.
func NewRunner() *Runner {
	return &Runner{Iface: defaultIface, ConfigDir: "/etc/wireguard"}
}

func (r *Runner) iface() string {
	if r.Iface != "" {
		return r.Iface
	}
	return defaultIface
}

func (r *Runner) configDir() string {
	if r.ConfigDir != "" {
		return r.ConfigDir
	}
	return "/etc/wireguard"
}

func (r *Runner) confPath() string {
	return filepath.Join(r.configDir(), r.iface()+".conf")
}

func (r *Runner) appliedPath() string {
	return filepath.Join(r.configDir(), appliedFile)
}

// PeerFingerprint hashes the host-peer fields. tunnel_id and lease_id are
// tickets and are not included. Never log the inputs.
func PeerFingerprint(s Session) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(s.Endpoint))
	b.WriteByte(0)
	b.WriteString(strings.TrimSpace(s.ServerPublicKey))
	b.WriteByte(0)
	b.WriteString(strings.TrimSpace(s.PeerPrivateKey))
	b.WriteByte(0)
	b.WriteString(strings.TrimSpace(s.ClientAddress))
	b.WriteByte(0)
	b.WriteString(strings.TrimSpace(s.AllowedIPs))
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// Apply writes the wg-quick config and brings the interface up.
//
// One host peer: wg-quick up runs only when endpoint, server_public_key,
// the local private key, client_address, or allowed_ips change, or the on-disk
// conf is missing PersistentKeepalive. A new tunnel_id or lease_id on the
// same peer is a ticket and does not bounce the iface.
// The applied marker stores that fingerprint so a restart does not re-up
// an identical peer. The key is never written to the marker.
func (r *Runner) Apply(ctx context.Context, sess Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if sess.Status != "allocated" {
		return ErrNotAllocated
	}
	if strings.TrimSpace(sess.PeerPrivateKey) == "" {
		return ErrMissingPrivateKey
	}
	if r.Userspace {
		return r.applyUserspace(ctx, sess)
	}
	if r.alreadyApplied(sess) {
		return nil
	}
	if err := os.MkdirAll(r.configDir(), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(r.configDir(), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(r.confPath()); err == nil {
		if err := r.quick(ctx, "down", r.confPath()); err != nil && !missingIface(err) {
			return err
		}
	}
	body := wgQuickConfig(sess)
	if err := os.WriteFile(r.confPath(), []byte(body), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(r.confPath(), 0o600); err != nil {
		return err
	}
	if err := r.quick(ctx, "up", r.confPath()); err != nil {
		return err
	}
	return r.saveApplied(PeerFingerprint(sess))
}

// Teardown runs wg-quick down and shreds the conf so the peer key does not stay on disk.
// Call only when GET is 204 (no host session) or status=revoked (host peer revoked).
// Do not call because lease_id or tunnel_id changed.
func (r *Runner) Teardown(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.Userspace {
		return r.teardownUserspace(ctx)
	}
	if !r.hasLocal() {
		return nil
	}
	downErr := r.quick(ctx, "down", r.confPath())
	shredErr := shredFile(r.confPath())
	_ = os.Remove(r.appliedPath())
	if downErr != nil && !missingIface(downErr) {
		return downErr
	}
	return shredErr
}

func (r *Runner) alreadyApplied(sess Session) bool {
	if r.loadApplied() != PeerFingerprint(sess) {
		return false
	}
	return r.confMatchesPeer(sess)
}

func (r *Runner) hasLocal() bool {
	if _, err := os.Stat(r.confPath()); err == nil {
		return true
	}
	if _, err := os.Stat(r.appliedPath()); err == nil {
		return true
	}
	return false
}

func (r *Runner) confMatchesPeer(s Session) bool {
	raw, err := os.ReadFile(r.confPath())
	if err != nil {
		return false
	}
	text := string(raw)
	if !strings.Contains(text, "PrivateKey = "+s.PeerPrivateKey) {
		return false
	}
	if s.ClientAddress != "" && !strings.Contains(text, "Address = "+s.ClientAddress) {
		return false
	}
	if s.ServerPublicKey != "" && !strings.Contains(text, "PublicKey = "+s.ServerPublicKey) {
		return false
	}
	if s.Endpoint != "" && !strings.Contains(text, "Endpoint = "+s.Endpoint) {
		return false
	}
	if s.AllowedIPs != "" && !strings.Contains(text, "AllowedIPs = "+s.AllowedIPs) {
		return false
	}
	// Old confs written before the NAT pin must not count as already applied.
	if !strings.Contains(text, persistentKeepaliveLine()) {
		return false
	}
	return true
}

func (r *Runner) saveApplied(fp string) error {
	if fp == "" {
		return nil
	}
	return os.WriteFile(r.appliedPath(), []byte(fp+"\n"), 0o600)
}

func (r *Runner) loadApplied() string {
	b, err := os.ReadFile(r.appliedPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (r *Runner) quick(ctx context.Context, args ...string) error {
	if r.Quick != nil {
		return r.Quick(ctx, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "wg-quick", args...)
	// A userspace implementation set in the environment would fork
	// wireguard-go inside this cgroup. The kernel path must not do that.
	cmd.Env = withoutUserspaceWG(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Combined output may include keys if wg-quick dumps the conf.
		// Do not wrap stdout/stderr into the returned error.
		return fmt.Errorf("hypermesh tunnel: wg-quick %s: %w", args[0], err)
	}
	_ = out
	return nil
}

func missingIface(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "cannot find") || strings.Contains(msg, "does not exist") || strings.Contains(msg, "no such")
}

func shredFile(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	n := fi.Size()
	if n < 1 {
		n = 1
	}
	zeros := make([]byte, n)
	if err := os.WriteFile(path, zeros, 0o600); err != nil {
		return err
	}
	return os.Remove(path)
}

func wgQuickConfig(s Session) string {
	var b strings.Builder
	b.WriteString("# hypermesh host peer\n")
	b.WriteString("[Interface]\n")
	b.WriteString("PrivateKey = ")
	b.WriteString(s.PeerPrivateKey)
	b.WriteString("\n")
	if s.ClientAddress != "" {
		b.WriteString("Address = ")
		b.WriteString(s.ClientAddress)
		b.WriteString("\n")
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

func persistentKeepaliveLine() string {
	return fmt.Sprintf("PersistentKeepalive = %d", persistentKeepaliveSec)
}
