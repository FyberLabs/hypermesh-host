package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/tunnel"
)

// TestRestartHandshakingIfaceSurvivesFailedSync drives a real userspace
// Runner. GET /agent/tunnel returns 503. The handshaking interface is not torn down.
func TestRestartHandshakingIfaceSurvivesFailedSync(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(api.PathTunnelPeer, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	a := newTestAgent(t, srv.URL, dir, filepath.Join(dir, "uptime"), "", "")
	a.state = State{DeviceID: testDevice, DeviceSecret: testSecret}
	a.client.DeviceSecret = testSecret
	if _, _, err := preloadPostedKey(a); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 25, 3, 0, 0, 0, time.UTC)
	link := &cmdLog{dump: handshakingDump(now)}
	a.tunnels = userspaceRunner(t, link, now)

	err := a.syncTunnel(context.Background())
	if err == nil {
		t.Fatal("expected plane sync error")
	}
	var se *api.StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("sync err %v", err)
	}
	if link.tornDown() || link.spawned {
		t.Fatalf("failed sync tore the tunnel down: %v", link.commands())
	}
	if !link.saw("wg show") {
		t.Fatalf("preserve should read the live interface: %v", link.commands())
	}
}

// TestRevokeTearsDownUserspaceIface covers GET 200 status=revoked and
// POST 409 "Tunnel is revoked". Both must remove the peer and set the link down,
// and neither may spawn wireguard-go or bring the link back.
func TestRevokeTearsDownUserspaceIface(t *testing.T) {
	t.Run("get", func(t *testing.T) {
		link := assertRevokeTearsDown(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == api.PathTunnelPeer {
				w.WriteHeader(http.StatusOK)
				return
			}
			writeJSON(w, http.StatusOK, api.TunnelSession{
				TunnelID: "tun-1",
				LeaseID:  "lease-1",
				Status:   api.TunnelRevoked,
			})
		})
		if link.broughtUp() {
			t.Fatalf("link came back: %v", link.commands())
		}
	})
	t.Run("post409", func(t *testing.T) {
		link := assertRevokeTearsDown(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == api.PathTunnelPeer {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"detail":"Tunnel is revoked"}`))
				return
			}
			t.Errorf("GET after revoke POST: %s", r.URL.Path)
			w.WriteHeader(http.StatusOK)
		})
		if link.broughtUp() {
			t.Fatalf("link came back: %v", link.commands())
		}
	})
}

func assertRevokeTearsDown(t *testing.T, h http.HandlerFunc) *cmdLog {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	a := newTestAgent(t, srv.URL, dir, filepath.Join(dir, "uptime"), "", "")
	a.state = State{DeviceID: testDevice, DeviceSecret: testSecret}
	a.client.DeviceSecret = testSecret
	now := time.Date(2026, 9, 25, 3, 0, 0, 0, time.UTC)
	link := &cmdLog{dump: handshakingDump(now)}
	a.tunnels = userspaceRunner(t, link, now)
	if err := a.syncTunnel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !link.tornDown() || link.spawned {
		t.Fatalf("revoke cmds %v", link.commands())
	}
	return link
}

func userspaceRunner(t *testing.T, link *cmdLog, now time.Time) *tunnel.Runner {
	t.Helper()
	return &tunnel.Runner{
		Iface:     "hypermesh",
		ConfigDir: t.TempDir(),
		Userspace: true,
		Now:       func() time.Time { return now },
		Run:       link.run,
		Quick: func(context.Context, ...string) error {
			t.Fatal("userspace mode must not call wg-quick")
			return nil
		},
	}
}

func handshakingDump(now time.Time) string {
	return fmt.Sprintf("%s\tifacepub\t0\toff\n%s\t(none)\t%s\t%s\t%d\t0\t0\t25\n",
		"PEERPRIVATE", "serverpub", "198.51.100.10:51820", "10.8.0.0/24", now.Unix())
}

type cmdLog struct {
	mu      sync.Mutex
	cmds    []string
	dump    string
	addr    string
	spawned bool
}

func (c *cmdLog) run(_ context.Context, name string, args ...string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	line := name + " " + strings.Join(args, " ")
	c.cmds = append(c.cmds, line)
	if strings.Contains(line, "wireguard-go") {
		c.spawned = true
		return nil, errors.New("spawned wireguard-go")
	}
	if name == "wg" && len(args) > 0 && args[0] == "show" {
		return []byte(c.dump), nil
	}
	if name == "ip" && strings.Contains(line, "addr") && strings.Contains(line, "show") {
		if c.addr == "" {
			return []byte(""), nil
		}
		return []byte("2: hypermesh    inet " + c.addr + " scope global hypermesh\n"), nil
	}
	if name == "wg" && len(args) > 0 && args[0] == "syncconf" {
		return nil, nil
	}
	if strings.Contains(line, " remove") || strings.Contains(line, " down") || strings.Contains(line, "flush") || strings.Contains(line, " replace") || strings.Contains(line, " up") {
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected %s", line)
}

func (c *cmdLog) commands() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.cmds))
	copy(out, c.cmds)
	return out
}

func (c *cmdLog) saw(frag string) bool {
	for _, cmd := range c.commands() {
		if strings.Contains(cmd, frag) {
			return true
		}
	}
	return false
}

func (c *cmdLog) tornDown() bool {
	return c.saw(" remove") || c.saw(" down") || c.saw("flush")
}

func (c *cmdLog) broughtUp() bool {
	return c.saw(" link ") && c.saw(" up")
}
