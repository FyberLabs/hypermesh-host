package tunnel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestUserspacePeerCompare is the fresh-process restart on AGX64-1.
// Each case builds a new Runner with no applied marker. The link is the
// live wireguard-go device, not memory from a previous Apply in this process.
func TestUserspacePeerCompare(t *testing.T) {
	now := time.Date(2026, 9, 26, 16, 38, 0, 0, time.UTC)
	priv, _, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	_, serverPub, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	base := allocated()
	base.PeerPrivateKey = stripPad(t, priv)
	base.ServerPublicKey = stripPad(t, serverPub)
	base.Endpoint = "wg.test.hyperme.sh:51820"
	base.AllowedIPs = "10.77.0.5/24, 10.77.0.1/32, 10.77.0.5/24"
	base.ClientAddress = "10.77.0.3/32"

	const liveIP = "20.65.85.229"

	type tweak struct {
		name   string
		sess   func(*Session)
		link   func(*scriptedLink)
		lookup func(context.Context, string) ([]string, error)
		extra  func(context.Context, string) ([]string, error)
		skip   bool
		port   string // substring required in the syncconf body when applying
	}
	resolve := func(_ context.Context, host string) ([]string, error) {
		if host == "wg.test.hyperme.sh" {
			return []string{liveIP}, nil
		}
		return nil, errors.New("no such host")
	}
	miss := func(context.Context, string) ([]string, error) {
		return nil, errors.New("server misbehaving")
	}
	cases := []tweak{
		{
			name:   "fresh process unchanged",
			lookup: resolve,
			skip:   true,
		},
		{
			name: "go resolver miss getent hit",
			lookup: miss,
			extra: func(context.Context, string) ([]string, error) {
				return []string{"::ffff:" + liveIP}, nil
			},
			skip: true,
		},
		{
			name: "endpoint address changed",
			lookup: func(context.Context, string) ([]string, error) {
				return []string{"198.51.100.99"}, nil
			},
			skip: false,
		},
		{
			name: "peer key changed",
			sess: func(s *Session) {
				_, other, err := GenerateKeypair()
				if err != nil {
					t.Fatal(err)
				}
				s.ServerPublicKey = other
			},
			lookup: resolve,
			skip:   false,
		},
		{
			name: "allowed ips changed",
			sess: func(s *Session) { s.AllowedIPs = "10.88.0.0/24" },
			lookup: resolve,
			skip:   false,
		},
		{
			name: "keepalive unset",
			link: func(l *scriptedLink) { l.ka = 0 },
			lookup: resolve,
			skip:   false,
		},
		{
			name: "listen port changed",
			sess: func(s *Session) { s.ListenPort = 54156 },
			lookup: resolve,
			skip:   false,
			port:   "ListenPort = 54156",
		},
		{
			name: "listen port only on device",
			link: func(l *scriptedLink) { l.port = 54155 },
			lookup: resolve,
			skip:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := base
			if tc.sess != nil {
				tc.sess(&sess)
			}
			link := newScriptedLink(sess, now)
			link.priv = priv
			link.pub = serverPub
			link.endpoint = liveIP + ":51820"
			link.allowed = "10.77.0.0/24,10.77.0.1/32"
			link.addr = "10.77.0.3"
			link.hs = now.Add(-122 * time.Second).Unix()
			link.port = 54155
			link.ka = persistentKeepaliveSec
			link.routes = []string{"10.77.0.0/24", "10.77.0.1/32"}
			if tc.link != nil {
				tc.link(link)
			}
			r := newUserspaceRunner(t, link, now)
			r.LookupHost = tc.lookup
			r.ExtraLookup = tc.extra
			if err := r.Apply(context.Background(), sess); err != nil {
				t.Fatal(err)
			}
			link.mu.Lock()
			defer link.mu.Unlock()
			if tc.skip {
				if link.synced || link.saw("syncconf") {
					t.Fatalf("unchanged peer called syncconf: %v", link.cmds)
				}
				if r.ApplyOutcome() != "skip" {
					t.Fatalf("outcome %q", r.ApplyOutcome())
				}
				if link.port != 54155 {
					t.Fatalf("listen port changed to %d", link.port)
				}
				return
			}
			if !link.synced || !link.saw("syncconf") {
				t.Fatalf("changed peer did not syncconf: %v", link.cmds)
			}
			if r.ApplyOutcome() != "syncconf" {
				t.Fatalf("outcome %q", r.ApplyOutcome())
			}
			if tc.port != "" && !strings.Contains(link.syncBody, tc.port) {
				t.Fatalf("syncconf body missing %q:\n%s", tc.port, link.syncBody)
			}
			for _, leak := range []string{priv, serverPub, sess.PeerPrivateKey, sess.ServerPublicKey} {
				if strings.Contains(strings.Join(link.cmds, "\n"), leak) {
					t.Fatal("command log contains key material")
				}
			}
		})
	}
}

func TestParseIfaceAddrPeerForm(t *testing.T) {
	line := "5: hypermesh    inet 10.77.0.3 peer 10.77.0.1/32 scope global hypermesh\\       valid_lft forever preferred_lft forever\n" +
		"5: hypermesh    inet 10.77.0.3/32 scope global hypermesh\\       valid_lft forever preferred_lft forever\n"
	if got := parseIfaceAddr(line); got != "10.77.0.3" {
		t.Fatalf("first address %q", got)
	}
	if !sameAddr(strings.Join(parseIfaceAddrs(line), ","), "10.77.0.3/32") {
		t.Fatal("bare local address must match the /32 from the control plane")
	}
	if sameAddr("10.77.0.3/24", "10.77.0.3/32") {
		t.Fatal("a different prefix is a real address change")
	}
}

func TestParseWGDumpKeepaliveOff(t *testing.T) {
	text := "priv\tpub\t54155\toff\n" +
		"peer\t(none)\t203.0.113.50:51820\t10.77.0.0/24\t0\t0\t148\toff\n"
	live, err := parseWGDump(text)
	if err != nil {
		t.Fatal(err)
	}
	if live.ListenPort != 54155 || len(live.Peers) != 1 {
		t.Fatalf("parsed %+v", live)
	}
	if live.Peers[0].Keepalive != 0 || live.Peers[0].Endpoint != "203.0.113.50:51820" {
		t.Fatalf("peer %+v", live.Peers[0])
	}
	if live.Peers[0].AllowedIPs != "10.77.0.0/24" {
		t.Fatalf("allowed %q", live.Peers[0].AllowedIPs)
	}
}
