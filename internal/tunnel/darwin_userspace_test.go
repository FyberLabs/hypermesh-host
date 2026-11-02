package tunnel

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUserspaceIfaceForDarwin(t *testing.T) {
	if got := UserspaceIfaceFor("linux", ""); got != "hypermesh" {
		t.Fatalf("linux iface %q", got)
	}
	if got := UserspaceIfaceFor("darwin", ""); got != DarwinUserspaceIface {
		t.Fatalf("darwin iface %q", got)
	}
	if got := UserspaceIfaceFor("darwin", "utun3"); got != "utun3" {
		t.Fatalf("override %q", got)
	}
}

func TestParseDarwinRoute(t *testing.T) {
	cases := []struct {
		line string
		v6   bool
		want string
		ok   bool
	}{
		{"10.8.0.0/24        utun8              USc                 utun8", false, "10.8.0.0/24", true},
		{"10.8.0.2           10.8.0.2           UH                  utun8", false, "", false},
		{"10.77.0.1          utun8              UHS                 utun8", false, "10.77.0.1/32", true},
		{"default            192.168.1.1        UGScg               en0", false, "", false},
		{"default            utun8              UGSc                utun8", false, "0.0.0.0/0", true},
		{"default            utun8              UGSc                utun8", true, "::/0", true},
		{"fe80::%utun8/64    fe80::1%utun8      Uc                  utun8", true, "", false},
		{"Internet:", false, "", false},
	}
	for _, tc := range cases {
		got, ok := parseDarwinRoute(tc.line, "utun8", tc.v6)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("%q ok=%v got %q want ok=%v %q", tc.line, ok, got, tc.ok, tc.want)
		}
	}
}

func TestParseIfconfigAddrsSkipsLinkLocal(t *testing.T) {
	out := "" +
		"utun8: flags=8051<UP,POINTOPOINT,RUNNING,MULTICAST> mtu 1420\n" +
		"\tinet 10.8.0.2 --> 10.8.0.2 netmask 0xffffffff\n" +
		"\tinet6 fe80::1%utun8 prefixlen 64 scopeid 0x12\n"
	got := parseIfconfigAddrs(out)
	if len(got) != 1 || got[0] != "10.8.0.2" {
		t.Fatalf("%v", got)
	}
}

func TestDarwinUserspaceApplyUsesIfconfig(t *testing.T) {
	now := time.Date(2026, 9, 25, 3, 0, 0, 0, time.UTC)
	sess := allocated()
	link := newDarwinLink(sess, now)
	link.peers = 0
	r := &Runner{
		Iface:     DarwinUserspaceIface,
		ConfigDir: t.TempDir(),
		Userspace: true,
		LinkOS:    "darwin",
		Now:       func() time.Time { return now },
		Run:       link.run,
		Quick: func(context.Context, ...string) error {
			t.Fatal("userspace mode must not call wg-quick")
			return nil
		},
	}
	if err := r.Apply(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	link.mu.Lock()
	if link.spawned || !link.synced || !link.up {
		link.mu.Unlock()
		t.Fatalf("apply %+v cmds=%v", link.synced, link.cmds)
	}
	want := []string{
		"ifconfig utun8 inet 10.8.0.2/32 10.8.0.2 alias",
		"ifconfig utun8 up",
		"route -q -n add -inet 10.8.0.0/24 -interface utun8",
	}
	for _, cmd := range want {
		if !link.saw(cmd) {
			link.mu.Unlock()
			t.Fatalf("missing %s in %v", cmd, link.cmds)
		}
	}
	for _, cmd := range link.cmds {
		if strings.HasPrefix(cmd, "ip ") || strings.Contains(cmd, "wireguard-go") {
			link.mu.Unlock()
			t.Fatalf("linux or spawn command %s", cmd)
		}
	}
	link.mu.Unlock()

	if err := r.Teardown(context.Background()); err != nil {
		t.Fatal(err)
	}
	link.mu.Lock()
	defer link.mu.Unlock()
	if !link.saw("route -q -n delete -inet 10.8.0.0/24 -interface utun8") || !link.saw("ifconfig utun8 down") {
		t.Fatalf("teardown cmds %v", link.cmds)
	}
	if !link.saw("ifconfig utun8 inet 10.8.0.2 -alias") {
		t.Fatalf("address left configured: %v", link.cmds)
	}
}

func TestDarwinMissingIfaceNamesLaunchd(t *testing.T) {
	now := time.Date(2026, 9, 25, 3, 0, 0, 0, time.UTC)
	link := newDarwinLink(allocated(), now)
	link.exists = false
	r := &Runner{
		Iface:     DarwinUserspaceIface,
		ConfigDir: t.TempDir(),
		Userspace: true,
		LinkOS:    "darwin",
		Now:       func() time.Time { return now },
		Run:       link.run,
	}
	err := r.Apply(context.Background(), allocated())
	if err == nil || !strings.Contains(err.Error(), "com.fyberlabs.hypermesh-wg") || !strings.Contains(err.Error(), "utun8") {
		t.Fatal(err)
	}
	link.mu.Lock()
	defer link.mu.Unlock()
	if link.spawned {
		t.Fatal("agent spawned wireguard-go")
	}
}

type darwinLink struct {
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
	port     int
	hs       int64
	routes   []string
	synced   bool
	spawned  bool
}

func newDarwinLink(sess Session, now time.Time) *darwinLink {
	return &darwinLink{
		exists:   true,
		up:       true,
		peers:    1,
		priv:     sess.PeerPrivateKey,
		pub:      sess.ServerPublicKey,
		endpoint: sess.Endpoint,
		allowed:  sess.AllowedIPs,
		addr:     sess.ClientAddress,
		port:     33469,
		hs:       now.Unix(),
	}
}

func (d *darwinLink) saw(cmd string) bool {
	for _, have := range d.cmds {
		if have == cmd {
			return true
		}
	}
	return false
}

func (d *darwinLink) run(_ context.Context, name string, args ...string) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	line := name + " " + strings.Join(args, " ")
	d.cmds = append(d.cmds, line)
	if strings.Contains(line, "wireguard-go") {
		d.spawned = true
		return nil, fmt.Errorf("spawned wireguard-go")
	}
	switch name {
	case "wg":
		return d.wg(args)
	case "ifconfig":
		return d.ifconfig(args)
	case "netstat":
		return d.netstat(args)
	case "route":
		return d.route(args)
	default:
		return nil, fmt.Errorf("unexpected %s", line)
	}
}

func (d *darwinLink) wg(args []string) ([]byte, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("wg")
	}
	switch args[0] {
	case "show":
		if !d.exists {
			return nil, fmt.Errorf("Unable to access interface: No such device")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s\tifacepub\t%d\toff\n", d.priv, d.port)
		if d.peers > 0 {
			fmt.Fprintf(&b, "%s\t(none)\t%s\t%s\t%d\t0\t148\t25\n", d.pub, d.endpoint, d.allowed, d.hs)
		}
		return []byte(b.String()), nil
	case "syncconf":
		body, err := os.ReadFile(args[len(args)-1])
		if err != nil {
			return nil, err
		}
		d.synced = true
		d.peers = 1
		d.up = true
		if ep := confValue(string(body), "Endpoint"); ep != "" {
			d.endpoint = ep
		}
		if allowed := confValue(string(body), "AllowedIPs"); allowed != "" {
			d.allowed = allowed
		}
		return nil, nil
	case "set":
		d.peers = 0
		d.hs = 0
		return nil, nil
	default:
		return nil, fmt.Errorf("wg %v", args)
	}
}

func confValue(body, key string) string {
	for _, line := range strings.Split(body, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (d *darwinLink) ifconfig(args []string) ([]byte, error) {
	if len(args) == 1 {
		if d.addr == "" {
			return []byte("utun8: flags=0<> mtu 1420\n"), nil
		}
		host := d.addr
		if i := strings.IndexByte(host, '/'); i >= 0 {
			host = host[:i]
		}
		return []byte("utun8: flags=8051<UP,POINTOPOINT,RUNNING,MULTICAST> mtu 1420\n\tinet " + host + " --> " + host + " netmask 0xffffffff\n"), nil
	}
	if len(args) >= 2 && (args[1] == "up" || args[1] == "down") {
		d.up = args[1] == "up"
		return nil, nil
	}
	if containsAll(args, "inet", "alias") {
		d.addr = args[2]
		return nil, nil
	}
	if containsAll(args, "-alias") {
		d.addr = ""
		return nil, nil
	}
	return nil, fmt.Errorf("ifconfig %v", args)
}

func (d *darwinLink) netstat(args []string) ([]byte, error) {
	v6 := containsAll(args, "inet6")
	list := d.routes
	if v6 {
		list = nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Destination        Gateway            Flags           Netif Expire\n")
	if d.addr != "" && !v6 {
		host := d.addr
		if i := strings.IndexByte(host, '/'); i >= 0 {
			host = host[:i]
		}
		fmt.Fprintf(&b, "%s %s UH utun8\n", host, host)
	}
	for _, cidr := range list {
		fmt.Fprintf(&b, "%s utun8 USc utun8\n", cidr)
	}
	return []byte(b.String()), nil
}

func (d *darwinLink) route(args []string) ([]byte, error) {
	if len(args) < 5 {
		return nil, fmt.Errorf("route %v", args)
	}
	op := args[2]
	cidr := args[4]
	switch op {
	case "change":
		for _, have := range d.routes {
			if have == cidr {
				return nil, nil
			}
		}
		return nil, fmt.Errorf("route: not in table")
	case "add":
		for _, have := range d.routes {
			if have == cidr {
				return nil, fmt.Errorf("route: file exists")
			}
		}
		d.routes = append(d.routes, cidr)
		return nil, nil
	case "delete":
		dst := d.routes[:0]
		for _, have := range d.routes {
			if have != cidr {
				dst = append(dst, have)
			}
		}
		d.routes = dst
		return nil, nil
	default:
		return nil, fmt.Errorf("route %s", strconv.Quote(op))
	}
}
