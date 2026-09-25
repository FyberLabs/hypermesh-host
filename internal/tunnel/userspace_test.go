package tunnel

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUserspaceSelected(t *testing.T) {
	if UserspaceSelected("") || UserspaceSelected("0") || UserspaceSelected("kernel") || UserspaceSelected("false") {
		t.Fatal("kernel mode must stay on wg-quick")
	}
	if !UserspaceSelected("1") || !UserspaceSelected("true") || !UserspaceSelected("userspace") {
		t.Fatal("explicit userspace")
	}
	if !ValidWGMode("auto") || ValidWGMode("banana") {
		t.Fatal("mode validation")
	}
}

func TestWithoutUserspaceWGStripsSpawnEnv(t *testing.T) {
	env := withoutUserspaceWG([]string{
		"PATH=/usr/bin",
		"WG_QUICK_USERSPACE_IMPLEMENTATION=/usr/bin/wireguard-go",
		"HOME=/root",
	})
	for _, e := range env {
		if strings.Contains(e, "wireguard-go") || strings.HasPrefix(e, "WG_QUICK_USERSPACE_IMPLEMENTATION=") {
			t.Fatalf("kernel wg-quick env must not spawn wireguard-go: %v", env)
		}
	}
}

// TestUserspaceRestartHandshakeFailedSyncNoTeardown is an agent restart:
// the interface is already handshaking with the desired peer, and the plane
// fetch fails. No peer remove, link down, or wireguard-go.
func TestUserspaceRestartHandshakeFailedSyncNoTeardown(t *testing.T) {
	now := time.Date(2026, 9, 25, 3, 0, 0, 0, time.UTC)
	sess := allocated()
	link := newScriptedLink(sess, now)
	r := newUserspaceRunner(t, link, now)
	kept, err := r.OnSyncError(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !kept {
		t.Fatal("recent handshake must be preserved when sync fails")
	}
	link.mu.Lock()
	defer link.mu.Unlock()
	if link.down || link.removed || link.spawned || link.synced {
		t.Fatalf("failed sync mutated the link: down=%v removed=%v spawned=%v synced=%v cmds=%v",
			link.down, link.removed, link.spawned, link.synced, link.cmds)
	}
	if !link.up || link.peers != 1 {
		t.Fatalf("handshake dropped: up=%v peers=%d", link.up, link.peers)
	}
}

// TestUserspaceRevokeTearsDownAndStaysDown removes peers and sets the link
// down. A later preserve, and Apply of a revoked session, must not bring it back.
// A new allocated session may.
func TestUserspaceRevokeTearsDownAndStaysDown(t *testing.T) {
	now := time.Date(2026, 9, 25, 3, 0, 0, 0, time.UTC)
	sess := allocated()
	link := newScriptedLink(sess, now)
	link.routes = []string{"10.77.0.0/24"}
	r := newUserspaceRunner(t, link, now)
	if err := os.WriteFile(r.confPath(), []byte(wgQuickConfig(sess)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Teardown(context.Background()); err != nil {
		t.Fatal(err)
	}
	link.mu.Lock()
	if !link.down || !link.removed || link.spawned || link.up || link.peers != 0 {
		link.mu.Unlock()
		t.Fatalf("revoke must drop peers and the link: %+v cmds=%v", link.snapshot(), link.cmds)
	}
	if !link.saw("ip route del 10.77.0.0/24 dev hypermesh") || len(link.routes) != 0 {
		link.mu.Unlock()
		t.Fatalf("revoke left the AllowedIPs route: routes=%v cmds=%v", link.routes, link.cmds)
	}
	link.mu.Unlock()
	if _, err := os.Stat(r.confPath()); !os.IsNotExist(err) {
		t.Fatal("conf must be shredded")
	}
	revoked := sess
	revoked.Status = "revoked"
	if err := r.Apply(context.Background(), revoked); err != ErrNotAllocated {
		t.Fatalf("revoked apply: %v", err)
	}
	if _, err := r.OnSyncError(context.Background()); err != nil {
		t.Fatal(err)
	}
	link.mu.Lock()
	if link.up || link.spawned || link.synced {
		link.mu.Unlock()
		t.Fatal("revoked interface came back")
	}
	link.mu.Unlock()

	if err := r.Apply(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	link.mu.Lock()
	defer link.mu.Unlock()
	if !link.up || link.peers != 1 || link.spawned {
		t.Fatalf("new allocated session should configure the existing interface: %+v", link.snapshot())
	}
	if !strings.Contains(link.syncBody, "PersistentKeepalive = 25") {
		t.Fatal("syncconf must keep PersistentKeepalive = 25")
	}
}

// TestUserspaceConfigChangeApplies syncs a new endpoint onto the live
// interface and does not tear the link down or spawn wireguard-go.
func TestUserspaceConfigChangeApplies(t *testing.T) {
	now := time.Date(2026, 9, 25, 3, 0, 0, 0, time.UTC)
	sess := allocated()
	link := newScriptedLink(sess, now)
	r := newUserspaceRunner(t, link, now)
	next := allocated()
	next.Endpoint = "198.51.100.99:51820"
	if err := r.Apply(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	link.mu.Lock()
	defer link.mu.Unlock()
	if !link.synced || link.down || link.removed || link.spawned {
		t.Fatalf("config change must sync without teardown: %+v cmds=%v", link.snapshot(), link.cmds)
	}
	if link.endpoint != next.Endpoint {
		t.Fatalf("endpoint %s", link.endpoint)
	}
	if !strings.Contains(link.syncBody, "PersistentKeepalive = 25") {
		t.Fatal("syncconf missing PersistentKeepalive")
	}
	if !strings.Contains(link.syncBody, "PrivateKey = "+sess.PeerPrivateKey) {
		t.Fatal("syncconf missing private key")
	}
	if !strings.Contains(link.syncBody, "ListenPort = 33469") {
		t.Fatal("syncconf must pin the current listen port")
	}
	if link.port != 33469 {
		t.Fatalf("listen port changed to %d", link.port)
	}
	if strings.Contains(link.syncBody, "Address = ") {
		t.Fatal("wg syncconf file must not include Address")
	}
	if !link.up {
		t.Fatal("link should be up after apply")
	}
}

func TestUserspaceMatchingHandshakeDoesNotReset(t *testing.T) {
	now := time.Date(2026, 9, 25, 3, 0, 0, 0, time.UTC)
	sess := allocated()
	link := newScriptedLink(sess, now)
	r := newUserspaceRunner(t, link, now)
	if err := r.Apply(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	link.mu.Lock()
	defer link.mu.Unlock()
	if link.synced || link.down || link.removed || link.spawned {
		t.Fatalf("matching handshake was reset: %+v cmds=%v", link.snapshot(), link.cmds)
	}
}

// TestUserspaceApplyInstallsAllowedIPRoute is the AGX64-1 miss: wg-quick
// routed AllowedIPs, syncconf does not. 0.0.0.0/0 and ::/0 are not installed
// unless the runner explicitly intends a full tunnel.
func TestUserspaceApplyInstallsAllowedIPRoute(t *testing.T) {
	now := time.Date(2026, 9, 25, 3, 0, 0, 0, time.UTC)
	sess := allocated()
	sess.ClientAddress = "10.77.0.3/32"
	sess.AllowedIPs = "0.0.0.0/0, ::/0, 10.77.0.0/24"
	link := newScriptedLink(sess, now)
	link.peers = 0
	r := newUserspaceRunner(t, link, now)
	if err := r.Apply(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	link.mu.Lock()
	if !link.saw("ip route replace 10.77.0.0/24 dev hypermesh") {
		link.mu.Unlock()
		t.Fatalf("missing overlay route: %v", link.cmds)
	}
	if link.saw("0.0.0.0/0") || link.saw("::/0") {
		link.mu.Unlock()
		t.Fatalf("default route installed: %v", link.cmds)
	}
	link.synced = false
	link.peers = 0
	link.mu.Unlock()

	r.InstallDefaultRoute = true
	if err := r.Apply(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	link.mu.Lock()
	defer link.mu.Unlock()
	if !link.saw("ip route replace 0.0.0.0/0 dev hypermesh") || !link.saw("ip -6 route replace ::/0 dev hypermesh") {
		t.Fatalf("explicit full tunnel did not install default routes: %v", link.cmds)
	}
}

// TestUserspaceRestartIdenticalConfigDoesNotSync is a successful plane sync
// after systemctl restart hypermesh-agent. The handshake is 122s old. wg show
// dump is not the config text: the endpoint is the resolved address, AllowedIPs
// drop spaces and change order, and keys may differ in base64 padding. A string
// compare used to fail, syncconf ran, and an omitted ListenPort was sent as
// listen_port=0, so wireguard-go rebound 33469 and forced a new handshake.
func TestUserspaceRestartIdenticalConfigDoesNotSync(t *testing.T) {
	now := time.Date(2026, 9, 25, 3, 0, 0, 0, time.UTC)
	priv, _, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	_, serverPub, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	sess := allocated()
	sess.PeerPrivateKey = stripPad(t, priv)
	sess.ServerPublicKey = stripPad(t, serverPub)
	sess.Endpoint = "wg.test.hyperme.sh:51820"
	sess.AllowedIPs = "10.77.0.1/32, 10.77.0.0/24"
	sess.ClientAddress = "10.77.0.3/32"
	link := newScriptedLink(sess, now)
	link.priv = priv
	link.pub = serverPub
	link.endpoint = "203.0.113.50:51820"
	link.allowed = "10.77.0.0/24,10.77.0.1/32"
	link.hs = now.Add(-122 * time.Second).Unix()
	link.port = 33469
	link.routes = []string{"10.77.0.0/24", "10.77.0.1/32"}
	r := newUserspaceRunner(t, link, now)
	r.LookupHost = func(_ context.Context, host string) ([]string, error) {
		if host == "wg.test.hyperme.sh" {
			return []string{"203.0.113.50"}, nil
		}
		return nil, errors.New("no such host")
	}
	if err := r.Apply(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	link.mu.Lock()
	defer link.mu.Unlock()
	if link.synced || link.saw("syncconf") {
		t.Fatalf("identical config called syncconf: %v", link.cmds)
	}
	if link.port != 33469 {
		t.Fatalf("listen port changed to %d", link.port)
	}
	if r.ApplyOutcome() != "skip" {
		t.Fatalf("outcome %q", r.ApplyOutcome())
	}
}

func TestUserspaceAllowedIPsChangeUpdatesRoutes(t *testing.T) {
	now := time.Date(2026, 9, 25, 3, 0, 0, 0, time.UTC)
	sess := allocated()
	sess.AllowedIPs = "10.77.0.0/24"
	sess.ClientAddress = "10.77.0.3/32"
	link := newScriptedLink(sess, now)
	link.routes = []string{"10.77.0.0/24"}
	r := newUserspaceRunner(t, link, now)
	next := sess
	next.AllowedIPs = "10.88.0.0/24, 10.77.0.5/32"
	if err := r.Apply(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	link.mu.Lock()
	defer link.mu.Unlock()
	if !link.synced {
		t.Fatal("AllowedIPs change must syncconf")
	}
	if r.ApplyOutcome() != "syncconf" {
		t.Fatalf("outcome %q", r.ApplyOutcome())
	}
	if !strings.Contains(link.syncBody, "ListenPort = 33469") || link.port != 33469 {
		t.Fatalf("listen port not pinned: port=%d body=%s", link.port, link.syncBody)
	}
	if !link.saw("ip route replace 10.88.0.0/24 dev hypermesh") || !link.saw("ip route replace 10.77.0.5/32 dev hypermesh") {
		t.Fatalf("new prefixes missing: %v", link.cmds)
	}
	if !link.saw("ip route del 10.77.0.0/24 dev hypermesh") {
		t.Fatalf("stale prefix kept: routes=%v cmds=%v", link.routes, link.cmds)
	}
	for _, cidr := range link.routes {
		if cidr == "10.77.0.0/24" {
			t.Fatalf("stale route still installed: %v", link.routes)
		}
	}
}

// TestUserspaceReconcileKeepsKernelAndLinkLocal is a fake `ip route` /
// `ip -6 route` listing. A proto kernel connected route and fe80::/64 stay.
// A stale agent prefix (proto boot, with scope link and metric) is deleted,
// and the current AllowedIPs prefix is installed.
func TestUserspaceReconcileKeepsKernelAndLinkLocal(t *testing.T) {
	now := time.Date(2026, 9, 25, 3, 0, 0, 0, time.UTC)
	sess := allocated()
	sess.AllowedIPs = "10.88.0.0/24"
	sess.ClientAddress = "10.77.0.3/24"
	link := newScriptedLink(sess, now)
	link.routes = []string{"10.77.0.0/24", "10.66.0.0/24"}
	link.routes6 = []string{"fe80::/64"}
	link.lineFor = func(cidr string, _ bool) string {
		switch cidr {
		case "10.77.0.0/24":
			// Connected route from a non-/32 address. dev is omitted, which
			// is what `ip route show dev hypermesh` prints, and metric follows
			// proto kernel scope link.
			return "10.77.0.0/24 proto kernel scope link src 10.77.0.3 metric 256"
		case "10.66.0.0/24":
			return "10.66.0.0/24 proto boot scope link metric 1024"
		case "fe80::/64":
			// No "proto kernel": the link-local check has to save this line
			// on its own. metric and pref are present so they are not parsed
			// as the destination.
			return "fe80::/64 metric 256 pref medium"
		default:
			return ""
		}
	}
	var logs bytes.Buffer
	r := newUserspaceRunner(t, link, now)
	r.Log = slog.New(slog.NewTextHandler(&logs, nil))
	if err := r.Apply(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	link.mu.Lock()
	if link.synced || link.saw("syncconf") {
		link.mu.Unlock()
		t.Fatalf("matching peer was rewritten: %v", link.cmds)
	}
	if r.ApplyOutcome() != "skip" {
		link.mu.Unlock()
		t.Fatalf("outcome %q", r.ApplyOutcome())
	}
	if !hasCIDR(link.routes, "10.77.0.0/24") {
		link.mu.Unlock()
		t.Fatalf("proto kernel route deleted: %v", link.routes)
	}
	if hasCIDR(link.routes, "10.66.0.0/24") {
		link.mu.Unlock()
		t.Fatalf("stale prefix kept: %v", link.routes)
	}
	if !hasCIDR(link.routes, "10.88.0.0/24") {
		link.mu.Unlock()
		t.Fatalf("wanted prefix missing: %v", link.routes)
	}
	if !hasCIDR(link.routes6, "fe80::/64") {
		link.mu.Unlock()
		t.Fatalf("link-local deleted: %v", link.routes6)
	}
	if link.saw("ip route del 10.77.0.0/24") || link.saw("ip -6 route del fe80::/64") {
		link.mu.Unlock()
		t.Fatalf("reconcile deleted an unmanaged route: %v", link.cmds)
	}
	if !link.saw("ip route del 10.66.0.0/24 dev hypermesh") {
		link.mu.Unlock()
		t.Fatalf("stale prefix not deleted: %v", link.cmds)
	}
	if !link.saw("ip route replace 10.88.0.0/24 dev hypermesh") {
		link.mu.Unlock()
		t.Fatalf("wanted prefix not installed: %v", link.cmds)
	}
	link.mu.Unlock()

	text := logs.String()
	if !strings.Contains(text, `msg="route add"`) || !strings.Contains(text, "cidr=10.88.0.0/24") ||
		!strings.Contains(text, "tunnel_id=tun-1") || !strings.Contains(text, "lease_id=lease-1") {
		t.Fatalf("route add log: %s", text)
	}
	if !strings.Contains(text, `msg="route delete"`) || !strings.Contains(text, "cidr=10.66.0.0/24") {
		t.Fatalf("route delete log: %s", text)
	}
	if strings.Contains(text, "10.77.0.0/24") || strings.Contains(text, "fe80::/64") {
		t.Fatalf("unmanaged route logged as a change: %s", text)
	}

	if err := r.Teardown(context.Background()); err != nil {
		t.Fatal(err)
	}
	link.mu.Lock()
	defer link.mu.Unlock()
	if !hasCIDR(link.routes, "10.77.0.0/24") || !hasCIDR(link.routes6, "fe80::/64") {
		t.Fatalf("teardown deleted unmanaged routes: v4=%v v6=%v", link.routes, link.routes6)
	}
	if hasCIDR(link.routes, "10.88.0.0/24") || hasCIDR(link.routes, "10.66.0.0/24") {
		t.Fatalf("teardown left an agent route: %v", link.routes)
	}
	if link.saw("ip route del 10.77.0.0/24") || link.saw("ip -6 route del fe80::/64") {
		t.Fatalf("teardown deleted an unmanaged route: %v", link.cmds)
	}
}

func TestParseDevRouteSkipsKernelAndLinkLocal(t *testing.T) {
	cases := []struct {
		line string
		keep bool
		cidr string
	}{
		{"10.77.0.0/24 proto kernel scope link src 10.77.0.3 metric 256", false, ""},
		{"10.77.0.0/24 dev hypermesh proto kernel scope link src 10.77.0.3 metric 256 linkdown", false, ""},
		{"10.77.0.0/24 metric 256 proto kernel scope link", false, ""},
		{"fe80::/64 proto kernel metric 256 pref medium", false, ""},
		{"fe80::/64 metric 256 pref medium", false, ""},
		{"FE80::/64 proto kernel metric 256", false, ""},
		{"fe80:0:0:0:0:0:0:0/64 metric 1024 pref medium", false, ""},
		{"10.66.0.0/24 dev hypermesh scope link metric 1024", true, "10.66.0.0/24"},
		{"10.66.0.0/24 proto boot scope link metric 20", true, "10.66.0.0/24"},
		{"unicast 10.88.0.0/24 proto static scope link metric 10", true, "10.88.0.0/24"},
		{"default dev hypermesh scope link metric 1024", true, "default"},
		{"default proto kernel metric 256 pref medium", false, ""},
		{"broadcast 10.77.0.255 proto kernel scope link src 10.77.0.3 metric 256", false, ""},
		{"local 10.77.0.3 proto kernel scope host src 10.77.0.3", false, ""},
		{"10.77.0.3 proto kernel scope link src 10.77.0.3 metric 256", false, ""},
		{"10.77.0.5 proto boot scope link metric 20", true, "10.77.0.5/32"},
		{"fd00::5 metric 1024 pref medium", true, "fd00::5/128"},
		{"fe80::1 proto kernel metric 256 pref medium", false, ""},
		{"", false, ""},
		{"proto kernel scope link metric 256", false, ""},
	}
	for _, tc := range cases {
		got, ok := parseDevRoute(tc.line)
		if ok != tc.keep || got != tc.cidr {
			t.Fatalf("parse %q: got (%q, %v) want (%q, %v)", tc.line, got, ok, tc.cidr, tc.keep)
		}
	}
}

func hasCIDR(list []string, cidr string) bool {
	for _, have := range list {
		if have == cidr {
			return true
		}
	}
	return false
}

func stripPad(t *testing.T, std string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(std)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawStdEncoding.EncodeToString(raw)
}

func TestUserspaceMissingIfaceDoesNotSpawn(t *testing.T) {
	now := time.Date(2026, 9, 25, 3, 0, 0, 0, time.UTC)
	link := newScriptedLink(allocated(), now)
	link.exists = false
	r := newUserspaceRunner(t, link, now)
	err := r.Apply(context.Background(), allocated())
	if err == nil || !strings.Contains(err.Error(), "hypermesh-wg.service") {
		t.Fatalf("err %v", err)
	}
	link.mu.Lock()
	defer link.mu.Unlock()
	if link.spawned || link.synced {
		t.Fatalf("missing iface spawned or configured: %+v", link.snapshot())
	}
}

func newUserspaceRunner(t *testing.T, link *scriptedLink, now time.Time) *Runner {
	t.Helper()
	return &Runner{
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

type scriptedLink struct {
	mu       sync.Mutex
	cmds     []string
	exists   bool
	up       bool
	peers    int
	priv     string
	pub      string
	endpoint string
	allowed  string
	addr     string
	ka       int
	hs       int64
	port     int
	routes   []string
	routes6  []string
	// lineFor is the text `ip route show` returns for a stored prefix.
	// Nil, or an empty result, prints "<cidr> dev hypermesh scope link".
	lineFor  func(cidr string, v6 bool) string
	down     bool
	removed  bool
	spawned  bool
	synced   bool
	syncBody string
}

func newScriptedLink(sess Session, now time.Time) *scriptedLink {
	return &scriptedLink{
		exists:   true,
		up:       true,
		peers:    1,
		priv:     sess.PeerPrivateKey,
		pub:      sess.ServerPublicKey,
		endpoint: sess.Endpoint,
		allowed:  sess.AllowedIPs,
		addr:     sess.ClientAddress,
		ka:       persistentKeepaliveSec,
		hs:       now.Unix(),
		port:     33469,
	}
}

func (s *scriptedLink) snapshot() string {
	return fmt.Sprintf("up=%v peers=%d down=%v removed=%v spawned=%v synced=%v endpoint=%s",
		s.up, s.peers, s.down, s.removed, s.spawned, s.synced, s.endpoint)
}

func (s *scriptedLink) run(_ context.Context, name string, args ...string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	line := name + " " + strings.Join(args, " ")
	s.cmds = append(s.cmds, line)
	if strings.Contains(line, "wireguard-go") {
		s.spawned = true
		return nil, errors.New("spawned wireguard-go")
	}
	if name == "wg" && len(args) >= 1 && args[0] == "show" {
		if !s.exists {
			return nil, errors.New("Unable to access interface: No such device")
		}
		return []byte(s.dump()), nil
	}
	if name == "ip" && containsAll(args, "-4", "addr", "show") {
		if s.addr == "" {
			return []byte(""), nil
		}
		return []byte("2: hypermesh    inet " + s.addr + " scope global hypermesh\n"), nil
	}
	if name == "wg" && len(args) >= 1 && args[0] == "syncconf" {
		body, err := os.ReadFile(args[len(args)-1])
		if err != nil {
			return nil, err
		}
		s.synced = true
		s.syncBody = string(body)
		s.absorb(s.syncBody)
		return nil, nil
	}
	if name == "wg" && containsAll(args, "set", "peer", "remove") {
		s.removed = true
		s.peers = 0
		s.hs = 0
		return nil, nil
	}
	if name == "ip" && containsAll(args, "route") {
		return s.route(args)
	}
	if name == "ip" && containsAll(args, "addr", "replace") {
		s.addr = argAfter(args, "replace")
		return nil, nil
	}
	if name == "ip" && containsAll(args, "addr", "flush") {
		s.addr = ""
		return nil, nil
	}
	if name == "ip" && containsAll(args, "link", "down") {
		s.down = true
		s.up = false
		return nil, nil
	}
	if name == "ip" && containsAll(args, "link", "up") {
		s.up = true
		s.down = false
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected command %s", line)
}

func (s *scriptedLink) dump() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\tifacepub\t%d\toff\n", s.priv, s.port)
	if s.peers > 0 {
		fmt.Fprintf(&b, "%s\t(none)\t%s\t%s\t%d\t0\t0\t%d\n", s.pub, s.endpoint, s.allowed, s.hs, s.ka)
	}
	return b.String()
}

func (s *scriptedLink) route(args []string) ([]byte, error) {
	v6 := containsAll(args, "-6")
	switch {
	case containsAll(args, "show"):
		list := s.routes
		if v6 {
			list = s.routes6
		}
		var b strings.Builder
		for _, cidr := range list {
			line := cidr + " dev hypermesh scope link"
			if s.lineFor != nil {
				if custom := s.lineFor(cidr, v6); custom != "" {
					line = custom
				}
			}
			fmt.Fprintln(&b, line)
		}
		return []byte(b.String()), nil
	case containsAll(args, "replace"):
		s.addRoute(v6, routeToken(args))
		return nil, nil
	case containsAll(args, "del"):
		s.dropRoute(v6, routeToken(args))
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected route command %v", args)
	}
}

func (s *scriptedLink) addRoute(v6 bool, cidr string) {
	if cidr == "" {
		return
	}
	list := s.routes
	if v6 {
		list = s.routes6
	}
	for _, have := range list {
		if have == cidr {
			return
		}
	}
	if v6 {
		s.routes6 = append(s.routes6, cidr)
		return
	}
	s.routes = append(s.routes, cidr)
}

func (s *scriptedLink) dropRoute(v6 bool, cidr string) {
	src := s.routes
	if v6 {
		src = s.routes6
	}
	dst := make([]string, 0, len(src))
	for _, have := range src {
		if have != cidr {
			dst = append(dst, have)
		}
	}
	if v6 {
		s.routes6 = dst
		return
	}
	s.routes = dst
}

func (s *scriptedLink) saw(frag string) bool {
	for _, cmd := range s.cmds {
		if strings.Contains(cmd, frag) {
			return true
		}
	}
	return false
}

func (s *scriptedLink) absorb(text string) {
	s.peers = 0
	sawPort := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "PrivateKey = "):
			s.priv = strings.TrimPrefix(line, "PrivateKey = ")
		case strings.HasPrefix(line, "PublicKey = "):
			s.pub = strings.TrimPrefix(line, "PublicKey = ")
			s.peers++
		case strings.HasPrefix(line, "Endpoint = "):
			s.endpoint = strings.TrimPrefix(line, "Endpoint = ")
		case strings.HasPrefix(line, "AllowedIPs = "):
			s.allowed = strings.TrimPrefix(line, "AllowedIPs = ")
		case strings.HasPrefix(line, "PersistentKeepalive = "):
			fmt.Sscan(strings.TrimPrefix(line, "PersistentKeepalive = "), &s.ka)
		case strings.HasPrefix(line, "ListenPort = "):
			fmt.Sscan(strings.TrimPrefix(line, "ListenPort = "), &s.port)
			sawPort = true
		}
	}
	if !sawPort {
		// wireguard-tools sends listen_port=0 when the file omits ListenPort,
		// and wireguard-go binds a new port.
		s.port = 35800
	}
}

func containsAll(args []string, want ...string) bool {
	have := map[string]bool{}
	for _, a := range args {
		have[a] = true
	}
	for _, w := range want {
		if !have[w] {
			return false
		}
	}
	return true
}

func routeToken(args []string) string {
	for _, a := range args {
		if a == "default" || strings.Contains(a, "/") {
			return a
		}
	}
	return ""
}

func argAfter(args []string, key string) string {
	for i, a := range args {
		if a == key && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestParseIfaceAddr(t *testing.T) {
	got := parseIfaceAddr("2: hypermesh    inet 10.8.0.2/32 scope global hypermesh\n")
	if got != "10.8.0.2/32" {
		t.Fatalf("%q", got)
	}
	if parseIfaceAddr("") != "" {
		t.Fatal("empty")
	}
}
