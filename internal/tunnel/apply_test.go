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
	if err := r.Apply(context.Background(), allocated()); err != nil {
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
	if !strings.Contains(text, "PrivateKey = PEERPRIVATE") || !strings.Contains(text, idPrefix+"tun-1") {
		t.Fatalf("config contents incomplete")
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
	if err := r.Apply(ctx, allocated()); err != nil {
		t.Fatal(err)
	}
	if ups != 1 {
		t.Fatalf("wg-quick up count %d", ups)
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
