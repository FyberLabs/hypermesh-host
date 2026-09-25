package tunnel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func allocated() Session {
	return Session{
		TunnelID:        "tun-1",
		LeaseID:         "lease-1",
		Status:          "allocated",
		Endpoint:        "198.51.100.10:51820",
		ServerPublicKey: "serverpub",
		PeerPrivateKey:  "PEERPRIVATE",
		AllowedIPs:      "10.8.0.0/24",
		ClientAddress:   "10.8.0.2/32",
	}
}

func TestApplyWritesConfigAndCallsUp(t *testing.T) {
	dir := t.TempDir()
	var ops []string
	r := &Runner{
		Iface:     "hypermesh",
		ConfigDir: dir,
		Quick: func(_ context.Context, args ...string) error {
			ops = append(ops, args[0])
			return nil
		},
	}
	sess := allocated()
	if err := r.Apply(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0] != "up" {
		t.Fatalf("quick %v", ops)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "hypermesh.conf"))
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "hypermesh.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("conf mode %o", fi.Mode().Perm())
	}
	text := string(raw)
	if !strings.Contains(text, "PrivateKey = PEERPRIVATE") {
		t.Fatalf("config contents incomplete")
	}
	if !strings.Contains(text, "PersistentKeepalive = 25") {
		t.Fatalf("generated conf must pin PersistentKeepalive = 25")
	}
	if strings.Contains(text, "tun-1") || strings.Contains(text, "lease-1") {
		t.Fatal("conf must not treat ticket ids as a new peer")
	}
	marker, err := os.ReadFile(filepath.Join(dir, appliedFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(marker), "PEERPRIVATE") {
		t.Fatal("applied marker must not store the key")
	}
	if strings.TrimSpace(string(marker)) != PeerFingerprint(sess) {
		t.Fatalf("applied marker should be the host-peer fingerprint")
	}
}

func TestApplyIdempotentSkipsSecondUp(t *testing.T) {
	dir := t.TempDir()
	var ups int
	r := &Runner{
		ConfigDir: dir,
		Quick: func(_ context.Context, args ...string) error {
			if args[0] == "up" {
				ups++
			}
			return nil
		},
	}
	ctx := context.Background()
	if err := r.Apply(ctx, allocated()); err != nil {
		t.Fatal(err)
	}
	if r.ApplyOutcome() != "" {
		t.Fatalf("first apply outcome %q", r.ApplyOutcome())
	}
	if err := r.Apply(ctx, allocated()); err != nil {
		t.Fatal(err)
	}
	if r.ApplyOutcome() != "skip" {
		t.Fatalf("second apply outcome %q", r.ApplyOutcome())
	}
	if ups != 1 {
		t.Fatalf("wg-quick up count %d", ups)
	}
}

func TestApplySamePeerDifferentTicketsSkipsUp(t *testing.T) {
	dir := t.TempDir()
	var ops []string
	r := &Runner{
		ConfigDir: dir,
		Quick: func(_ context.Context, args ...string) error {
			ops = append(ops, args[0])
			return nil
		},
	}
	ctx := context.Background()
	first := allocated()
	second := allocated()
	second.TunnelID = "tun-2"
	second.LeaseID = "lease-2"
	if err := r.Apply(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := r.Apply(ctx, second); err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0] != "up" {
		t.Fatalf("ticket change must not bounce the host peer: %v", ops)
	}
}

func TestApplyRestartDoesNotBounceIdenticalPeer(t *testing.T) {
	dir := t.TempDir()
	var ups int
	quick := func(_ context.Context, args ...string) error {
		if args[0] == "up" {
			ups++
		}
		return nil
	}
	ctx := context.Background()
	if err := (&Runner{ConfigDir: dir, Quick: quick}).Apply(ctx, allocated()); err != nil {
		t.Fatal(err)
	}
	again := allocated()
	again.TunnelID = "tun-restart"
	again.LeaseID = "lease-restart"
	if err := (&Runner{ConfigDir: dir, Quick: quick}).Apply(ctx, again); err != nil {
		t.Fatal(err)
	}
	if ups != 1 {
		t.Fatalf("restart must not re-up an identical host peer: ups=%d", ups)
	}
}

func TestApplyChangedEndpointReUps(t *testing.T) {
	dir := t.TempDir()
	var ops []string
	r := &Runner{
		ConfigDir: dir,
		Quick: func(_ context.Context, args ...string) error {
			ops = append(ops, args[0])
			return nil
		},
	}
	ctx := context.Background()
	if err := r.Apply(ctx, allocated()); err != nil {
		t.Fatal(err)
	}
	next := allocated()
	next.Endpoint = "198.51.100.99:51820"
	if err := r.Apply(ctx, next); err != nil {
		t.Fatal(err)
	}
	if len(ops) != 3 || ops[0] != "up" || ops[1] != "down" || ops[2] != "up" {
		t.Fatalf("peer field change should down then up: %v", ops)
	}
}

func TestWgQuickConfigIncludesPersistentKeepalive(t *testing.T) {
	text := wgQuickConfig(allocated())
	if !strings.Contains(text, "PersistentKeepalive = 25") {
		t.Fatalf("missing PersistentKeepalive:\n%s", text)
	}
	if strings.Count(text, "[Peer]") != 1 {
		t.Fatal("one peer per host")
	}
	if strings.Contains(text, "tun-1") || strings.Contains(text, "lease-1") {
		t.Fatal("conf must not treat ticket ids as a new peer")
	}
}

func TestConfMatchesPeerRequiresPersistentKeepalive(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{ConfigDir: dir}
	sess := allocated()
	old := strings.ReplaceAll(wgQuickConfig(sess), "PersistentKeepalive = 25\n", "")
	if strings.Contains(old, "PersistentKeepalive") {
		t.Fatal("fixture must omit PersistentKeepalive")
	}
	if err := os.WriteFile(r.confPath(), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if r.confMatchesPeer(sess) {
		t.Fatal("conf without PersistentKeepalive must not match")
	}
	if err := os.WriteFile(r.confPath(), []byte(wgQuickConfig(sess)), 0o600); err != nil {
		t.Fatal(err)
	}
	if !r.confMatchesPeer(sess) {
		t.Fatal("generated conf must match")
	}
}

func TestApplyRewritesConfMissingKeepalive(t *testing.T) {
	dir := t.TempDir()
	var ops []string
	r := &Runner{
		ConfigDir: dir,
		Quick: func(_ context.Context, args ...string) error {
			ops = append(ops, args[0])
			return nil
		},
	}
	sess := allocated()
	old := strings.ReplaceAll(wgQuickConfig(sess), "PersistentKeepalive = 25\n", "")
	if err := os.WriteFile(filepath.Join(dir, "hypermesh.conf"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, appliedFile), []byte(PeerFingerprint(sess)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Apply(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 || ops[0] != "down" || ops[1] != "up" {
		t.Fatalf("missing keepalive must rewrite even when fingerprint matches: %v", ops)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "hypermesh.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "PersistentKeepalive = 25") {
		t.Fatal("rewritten conf must include PersistentKeepalive = 25")
	}
}

func TestPeerFingerprintIgnoresTickets(t *testing.T) {
	a := allocated()
	b := allocated()
	b.TunnelID = "other-tun"
	b.LeaseID = "other-lease"
	if PeerFingerprint(a) != PeerFingerprint(b) {
		t.Fatal("tunnel_id and lease_id must not change the host-peer fingerprint")
	}
	b.Endpoint = "203.0.113.1:51820"
	if PeerFingerprint(a) == PeerFingerprint(b) {
		t.Fatal("endpoint change must change the fingerprint")
	}
}

func TestWgQuickConfigUsesLocalPrivateKey(t *testing.T) {
	dir := t.TempDir()
	priv, pub, err := LoadOrCreatePrivateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess := allocated()
	sess.PeerPrivateKey = priv
	text := wgQuickConfig(sess)
	if !strings.Contains(text, "PrivateKey = "+priv) {
		t.Fatal("config must use the local private key")
	}
	if strings.Contains(text, pub) {
		t.Fatal("config must not embed the public key")
	}
	if !strings.Contains(text, "PersistentKeepalive = 25") {
		t.Fatal("keepalive")
	}
	var ops []string
	r := &Runner{
		Iface:     "hypermesh",
		ConfigDir: t.TempDir(),
		Quick: func(_ context.Context, args ...string) error {
			ops = append(ops, args[0])
			return nil
		},
	}
	if err := r.Apply(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	if r.iface() != "hypermesh" {
		t.Fatalf("iface %s", r.iface())
	}
	raw, err := os.ReadFile(r.confPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "PrivateKey = "+priv) {
		t.Fatal("on-disk conf must use the local private key")
	}
	if !strings.HasSuffix(r.confPath(), "hypermesh.conf") {
		t.Fatalf("conf path %s", r.confPath())
	}
	if len(ops) != 1 || ops[0] != "up" {
		t.Fatalf("quick %v", ops)
	}
}

func TestApplyRequiresAllocatedKey(t *testing.T) {
	r := &Runner{ConfigDir: t.TempDir(), Quick: func(context.Context, ...string) error { return nil }}
	if err := r.Apply(context.Background(), Session{Status: "revoked", PeerPrivateKey: "x"}); err != ErrNotAllocated {
		t.Fatalf("revoked: %v", err)
	}
	if err := r.Apply(context.Background(), Session{Status: "allocated"}); err != ErrMissingPrivateKey {
		t.Fatalf("no key: %v", err)
	}
}

func TestTeardownShredsConf(t *testing.T) {
	dir := t.TempDir()
	var ops []string
	r := &Runner{
		ConfigDir: dir,
		Quick: func(_ context.Context, args ...string) error {
			ops = append(ops, args[0])
			return nil
		},
	}
	if err := r.Apply(context.Background(), allocated()); err != nil {
		t.Fatal(err)
	}
	if err := r.Teardown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 || ops[0] != "up" || ops[1] != "down" {
		t.Fatalf("%v", ops)
	}
	if _, err := os.Stat(filepath.Join(dir, "hypermesh.conf")); !os.IsNotExist(err) {
		t.Fatal("conf must be shredded")
	}
	if _, err := os.Stat(filepath.Join(dir, appliedFile)); !os.IsNotExist(err) {
		t.Fatal("applied marker must be removed")
	}
}

func TestTeardownNoLocalIsNoop(t *testing.T) {
	r := &Runner{
		ConfigDir: t.TempDir(),
		Quick: func(context.Context, ...string) error {
			t.Fatal("wg-quick must not run when nothing is local")
			return nil
		},
	}
	if err := r.Teardown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestApplyHonorsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &Runner{ConfigDir: t.TempDir()}
	if err := r.Apply(ctx, Session{Status: "allocated", PeerPrivateKey: "x", TunnelID: "t"}); err == nil {
		t.Fatal("expected ctx error")
	}
}
