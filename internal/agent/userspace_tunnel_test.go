package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	link := &cmdLog{dump: handshakingDump(now), routes: []string{"10.77.0.0/24"}}
	a.tunnels = userspaceRunner(t, link, now)
	if err := a.syncTunnel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !link.tornDown() || link.spawned {
		t.Fatalf("revoke cmds %v", link.commands())
	}
	if !link.saw("ip route del 10.77.0.0/24 dev hypermesh") || len(link.routeList()) != 0 {
		t.Fatalf("revoke left the route: %v", link.commands())
	}
	return link
}

// TestUserspace204RemovesRoutes is GET /agent/tunnel 204 (no host session).
// Routes go away with the peers and the address.
func TestUserspace204RemovesRoutes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case api.PathTunnelPeer:
			w.WriteHeader(http.StatusOK)
		case api.PathTunnel:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	a := newTestAgent(t, srv.URL, dir, filepath.Join(dir, "uptime"), "", "")
	a.state = State{DeviceID: testDevice, DeviceSecret: testSecret}
	a.client.DeviceSecret = testSecret
	if _, _, err := preloadPostedKey(a); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 25, 3, 0, 0, 0, time.UTC)
	link := &cmdLog{dump: handshakingDump(now), routes: []string{"10.77.0.0/24"}}
	a.tunnels = userspaceRunner(t, link, now)
	if err := a.syncTunnel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !link.saw("ip route del 10.77.0.0/24 dev hypermesh") || len(link.routeList()) != 0 {
		t.Fatalf("204 left the route: %v", link.commands())
	}
	if !link.tornDown() || link.spawned {
		t.Fatalf("204 cmds %v", link.commands())
	}
}

func TestHostPeerLogDistinguishesSkipAndSyncconf(t *testing.T) {
	now := time.Date(2026, 9, 25, 3, 0, 0, 0, time.UTC)

	t.Run("unchanged", func(t *testing.T) {
		a, buf, link := peerLogAgent(t, now, true)
		if err := a.syncTunnel(context.Background()); err != nil {
			t.Fatal(err)
		}
		if link.saw("syncconf") {
			t.Fatalf("unchanged peer called syncconf: %v", link.commands())
		}
		got := buf.String()
		if !strings.Contains(got, `msg="host peer unchanged (skip apply)"`) ||
			!strings.Contains(got, "tunnel_id=tun-1") || !strings.Contains(got, "lease_id=lease-1") {
			t.Fatalf("log: %s", got)
		}
		if strings.Contains(got, "host peer applied") {
			t.Fatalf("unchanged peer logged an apply: %s", got)
		}
		buf.Reset()
		if err := a.syncTunnel(context.Background()); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(buf.String(), "host peer") {
			t.Fatalf("repeat sync logged again: %s", buf.String())
		}
	})

	t.Run("syncconf", func(t *testing.T) {
		a, buf, link := peerLogAgent(t, now, false)
		if err := a.syncTunnel(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !link.saw("syncconf") {
			t.Fatalf("changed peer did not syncconf: %v", link.commands())
		}
		got := buf.String()
		if !strings.Contains(got, `msg="host peer applied (syncconf)"`) ||
			!strings.Contains(got, "tunnel_id=tun-1") || !strings.Contains(got, "lease_id=lease-1") {
			t.Fatalf("log: %s", got)
		}
		if strings.Contains(got, "host peer unchanged") {
			t.Fatalf("syncconf logged as unchanged: %s", got)
		}
		if !strings.Contains(got, `msg="route add"`) || !strings.Contains(got, "cidr=10.77.0.0/24") ||
			!strings.Contains(got, "tunnel_id=tun-1") || !strings.Contains(got, "lease_id=lease-1") {
			t.Fatalf("route add log: %s", got)
		}
	})
}

func peerLogAgent(t *testing.T, now time.Time, unchanged bool) (*Agent, *bytes.Buffer, *cmdLog) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	a := newTestAgent(t, srv.URL, dir, filepath.Join(dir, "uptime"), "", "")
	if r, ok := a.tunnels.(*tunnel.Runner); !ok || r.Log == nil {
		t.Fatal("agent logger was not passed to the tunnel runner")
	}
	var buf bytes.Buffer
	a.log = slog.New(slog.NewTextHandler(&buf, nil))
	a.state = State{DeviceID: testDevice, DeviceSecret: testSecret}
	a.client.DeviceSecret = testSecret
	pub, priv, err := preloadPostedKey(a)
	if err != nil {
		t.Fatal(err)
	}
	_, serverPub, err := tunnel.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	sess := api.TunnelSession{
		TunnelID:        "tun-1",
		LeaseID:         "lease-1",
		Status:          api.TunnelAllocated,
		Endpoint:        "198.51.100.10:51820",
		ServerPublicKey: serverPub,
		PeerPublicKey:   pub,
		AllowedIPs:      "10.77.0.0/24",
		ClientAddress:   "10.77.0.3/32",
	}
	mux.HandleFunc(api.PathTunnelAck, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(api.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, sess)
	})
	link := &cmdLog{addr: sess.ClientAddress}
	if unchanged {
		link.dump = fmt.Sprintf("%s\tifacepub\t33469\toff\n%s\t(none)\t%s\t%s\t%d\t0\t0\t25\n",
			priv, sess.ServerPublicKey, sess.Endpoint, sess.AllowedIPs, now.Unix())
		link.routes = []string{sess.AllowedIPs}
	} else {
		link.dump = fmt.Sprintf("%s\tifacepub\t33469\toff\n", priv)
	}
	runner := userspaceRunner(t, link, now)
	runner.Log = a.log
	a.tunnels = runner
	return a, &buf, link
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
	routes  []string
	routes6 []string
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
	if name == "ip" && strings.Contains(line, " route ") {
		return c.route(line, args)
	}
	if name == "wg" && len(args) > 0 && args[0] == "syncconf" {
		return nil, nil
	}
	if strings.Contains(line, " remove") || strings.Contains(line, " down") || strings.Contains(line, "flush") || strings.Contains(line, " replace") || strings.Contains(line, " up") {
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected %s", line)
}

func (c *cmdLog) route(line string, args []string) ([]byte, error) {
	v6 := strings.Contains(line, " -6 ")
	switch {
	case strings.Contains(line, " show"):
		list := c.routes
		if v6 {
			list = c.routes6
		}
		var b strings.Builder
		for _, cidr := range list {
			fmt.Fprintf(&b, "%s dev hypermesh scope link\n", cidr)
		}
		return []byte(b.String()), nil
	case strings.Contains(line, " replace"):
		c.addRoute(v6, routeToken(args))
		return nil, nil
	case strings.Contains(line, " del"):
		c.dropRoute(v6, routeToken(args))
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected route %s", line)
	}
}

func (c *cmdLog) addRoute(v6 bool, cidr string) {
	if cidr == "" {
		return
	}
	list := c.routes
	if v6 {
		list = c.routes6
	}
	for _, have := range list {
		if have == cidr {
			return
		}
	}
	if v6 {
		c.routes6 = append(c.routes6, cidr)
		return
	}
	c.routes = append(c.routes, cidr)
}

func (c *cmdLog) dropRoute(v6 bool, cidr string) {
	src := c.routes
	if v6 {
		src = c.routes6
	}
	dst := make([]string, 0, len(src))
	for _, have := range src {
		if have != cidr {
			dst = append(dst, have)
		}
	}
	if v6 {
		c.routes6 = dst
		return
	}
	c.routes = dst
}

func routeToken(args []string) string {
	for _, a := range args {
		if a == "default" || strings.Contains(a, "/") {
			return a
		}
	}
	return ""
}

func (c *cmdLog) routeList() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.routes))
	copy(out, c.routes)
	return out
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
