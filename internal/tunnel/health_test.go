package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSnapshotStates(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	priv := "LEAKED-WG-PRIVATE"
	pub := "LEAKED-WG-PUBLIC"
	recent := now.Add(-100 * time.Second).Unix()
	stale := now.Add(-200 * time.Second).Unix()

	cases := []struct {
		name      string
		userspace bool
		pid       bool
		dump      string
		dumpErr   error
		want      string
		age       *int
		port      *int
		wantPID   *int
	}{
		{
			name:      "up",
			userspace: true,
			pid:       true,
			dump:      wgDump(priv, pub, 51820, recent),
			want:      StateUp,
			age:       intPtr(100),
			port:      intPtr(51820),
			wantPID:   intPtr(4242),
		},
		{
			name:      "handshaking no handshake yet",
			userspace: true,
			pid:       true,
			dump:      wgDump(priv, pub, 51820, 0),
			want:      StateHandshaking,
			port:      intPtr(51820),
			wantPID:   intPtr(4242),
		},
		{
			name:      "handshaking stale",
			userspace: true,
			pid:       true,
			dump:      wgDump(priv, pub, 51820, stale),
			want:      StateHandshaking,
			age:       intPtr(200),
			port:      intPtr(51820),
			wantPID:   intPtr(4242),
		},
		{
			name:      "down no peer",
			userspace: true,
			pid:       true,
			dump:      priv + "\tifacepub\t51820\toff\n",
			want:      StateDown,
			port:      intPtr(51820),
			wantPID:   intPtr(4242),
		},
		{
			name:      "disabled no process",
			userspace: true,
			dumpErr:   errors.New("no such device"),
			want:      StateDisabled,
		},
		{
			name:      "handshaking process without iface",
			userspace: true,
			pid:       true,
			dumpErr:   errors.New("no such device"),
			want:      StateHandshaking,
			wantPID:   intPtr(4242),
		},
		{
			name:    "kernel down",
			dumpErr: errors.New("no such device"),
			want:    StateDown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runner{
				Iface:     "hypermesh",
				Userspace: tc.userspace,
				Now:       func() time.Time { return now },
				FindPID: func(string) (int, bool) {
					if !tc.pid {
						return 0, false
					}
					return 4242, true
				},
				Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
					if name == "wg" {
						if tc.dumpErr != nil {
							return nil, tc.dumpErr
						}
						return []byte(tc.dump), nil
					}
					if name == "ip" {
						return []byte(""), nil
					}
					return nil, errors.New("unexpected " + name)
				},
			}
			snap, err := r.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if snap.State != tc.want {
				t.Fatalf("state %q want %q", snap.State, tc.want)
			}
			if !intPtrEqual(snap.HandshakeAgeS, tc.age) {
				t.Fatalf("age %v want %v", ptrStr(snap.HandshakeAgeS), ptrStr(tc.age))
			}
			if !intPtrEqual(snap.ListenPort, tc.port) {
				t.Fatalf("port %v want %v", ptrStr(snap.ListenPort), ptrStr(tc.port))
			}
			if !intPtrEqual(snap.PID, tc.wantPID) {
				t.Fatalf("pid %v want %v", ptrStr(snap.PID), ptrStr(tc.wantPID))
			}
			raw, err := json.Marshal(snap)
			if err != nil {
				t.Fatal(err)
			}
			for _, leak := range []string{priv, pub, "AllowedIPs", "10.77.0.0/24"} {
				if strings.Contains(string(raw), leak) {
					t.Fatalf("snapshot leaked %q: %s", leak, raw)
				}
			}
		})
	}
}

func TestCmdlineIsWireGuard(t *testing.T) {
	raw := append([]byte("/usr/bin/wireguard-go"), 0)
	raw = append(raw, []byte("-f")...)
	raw = append(raw, 0)
	raw = append(raw, []byte("hypermesh")...)
	raw = append(raw, 0)
	if !cmdlineIsWireGuard(raw, "hypermesh") {
		t.Fatal("expected match")
	}
	if cmdlineIsWireGuard(raw, "wg0") {
		t.Fatal("other iface")
	}
	if cmdlineIsWireGuard([]byte("hypermesh-agent\x00hypermesh"), "hypermesh") {
		t.Fatal("agent must not match")
	}
}

func wgDump(priv, pub string, port int, handshake int64) string {
	return strings.Join([]string{
		strings.Join([]string{priv, "ifacepub", itoa(port), "off"}, "\t"),
		strings.Join([]string{pub, "(none)", "10.77.0.1:51820", "10.77.0.0/24", itoa64(handshake), "0", "0", "25"}, "\t"),
	}, "\n") + "\n"
}

func itoa(n int) string {
	return strconvItoa(int64(n))
}

func itoa64(n int64) string {
	return strconvItoa(n)
}

func strconvItoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func intPtr(n int) *int { return &n }

func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func ptrStr(p *int) string {
	if p == nil {
		return "nil"
	}
	return itoa(*p)
}
