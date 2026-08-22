// Package tunnel applies a WireGuard (or equivalent) session allocated
// by the control plane. No custom overlay.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var (
	// ErrNoSession means GET /agent/tunnel returned nothing to apply.
	ErrNoSession = errors.New("hypermesh tunnel: no session")
	// ErrNotAllocated means the session is not status=allocated.
	ErrNotAllocated = errors.New("hypermesh tunnel: session is not allocated")
	// ErrMissingPrivateKey means allocated GET omitted peer_private_key.
	ErrMissingPrivateKey = errors.New("hypermesh tunnel: allocated session missing peer_private_key")
)

// Session is the locked GET /agent/tunnel body. Never log PeerPrivateKey.
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

// Runner writes a wg-quick config and runs `wg-quick up|down`.
type Runner struct {
	// Iface is the WireGuard interface name (default hypermesh, ≤15 chars).
	Iface string
	// ConfigDir holds <iface>.conf at mode 0600. The file contains the peer key.
	ConfigDir string
	// Quick overrides wg-quick. Args are ("up"|"down", configPath).
	Quick func(ctx context.Context, args ...string) error
}

// NewRunner uses /etc/wireguard and wg-quick on PATH.
func NewRunner() *Runner {
	return &Runner{Iface: "hypermesh", ConfigDir: "/etc/wireguard"}
}

func (r *Runner) iface() string {
	if r.Iface != "" {
		return r.Iface
	}
	return "hypermesh"
}

func (r *Runner) confPath() string {
	dir := r.ConfigDir
	if dir == "" {
		dir = "/etc/wireguard"
	}
	return filepath.Join(dir, r.iface()+".conf")
}

// Apply writes the wg-quick config and brings the interface up.
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
	if err := os.MkdirAll(filepath.Dir(r.confPath()), 0o700); err != nil {
		return err
	}
	body := wgQuickConfig(sess)
	if err := os.WriteFile(r.confPath(), []byte(body), 0o600); err != nil {
		return err
	}
	return r.quick(ctx, "up", r.confPath())
}

// Teardown runs wg-quick down. Missing interface is not an error.
func (r *Runner) Teardown(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := r.quick(ctx, "down", r.confPath())
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "cannot find") || strings.Contains(msg, "does not exist") || strings.Contains(msg, "no such") {
		return nil
	}
	return err
}

func (r *Runner) quick(ctx context.Context, args ...string) error {
	if r.Quick != nil {
		return r.Quick(ctx, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "wg-quick", args...).CombinedOutput()
	if err != nil {
		// Combined output may include keys if wg-quick dumps the conf.
		// Do not wrap stdout/stderr into the returned error.
		return fmt.Errorf("hypermesh tunnel: wg-quick %s: %w", args[0], err)
	}
	_ = out
	return nil
}

func wgQuickConfig(s Session) string {
	var b strings.Builder
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
	return b.String()
}
