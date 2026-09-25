package tunnel

import (
	"context"
	"errors"
	"fmt"
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
	fmt.Fprintf(&b, "%s\tifacepub\t0\toff\n", s.priv)
	if s.peers > 0 {
		fmt.Fprintf(&b, "%s\t(none)\t%s\t%s\t%d\t0\t0\t%d\n", s.pub, s.endpoint, s.allowed, s.hs, s.ka)
	}
	return b.String()
}

func (s *scriptedLink) absorb(text string) {
	s.peers = 0
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
		}
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
