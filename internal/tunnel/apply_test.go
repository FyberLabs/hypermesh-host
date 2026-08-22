package tunnel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyWritesConfigAndCallsUp(t *testing.T) {
	dir := t.TempDir()
	var got []string
	r := &Runner{
		Iface:     "hypermesh",
		ConfigDir: dir,
		Quick: func(_ context.Context, args ...string) error {
			got = append(got, args...)
			return nil
		},
	}
	err := r.Apply(context.Background(), Session{
		TunnelID:        "tun-1",
		Status:          "allocated",
		Endpoint:        "198.51.100.10:51820",
		ServerPublicKey: "serverpub",
		PeerPrivateKey:  "PEERPRIVATE",
		AllowedIPs:      "10.8.0.0/24",
		ClientAddress:   "10.8.0.2/32",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "up" {
		t.Fatalf("quick %v", got)
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
	if !strings.Contains(text, "PrivateKey = PEERPRIVATE") || !strings.Contains(text, "Endpoint = 198.51.100.10:51820") {
		t.Fatalf("config contents incomplete")
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

func TestTeardownCallsDown(t *testing.T) {
	var got []string
	r := &Runner{
		ConfigDir: t.TempDir(),
		Quick: func(_ context.Context, args ...string) error {
			got = append(got, args...)
			return nil
		},
	}
	if err := r.Teardown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "down" {
		t.Fatalf("%v", got)
	}
}

func TestApplyHonorsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &Runner{ConfigDir: t.TempDir()}
	if err := r.Apply(ctx, Session{Status: "allocated", PeerPrivateKey: "x"}); err == nil {
		t.Fatal("expected ctx error")
	}
}
