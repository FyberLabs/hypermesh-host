package tunnel

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestLoadOrCreatePrivateKeySameAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	priv, pub, err := LoadOrCreatePrivateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if priv == "" || pub == "" || priv == pub {
		t.Fatalf("pair empty or equal")
	}
	if _, err := PublicFromPrivate(priv); err != nil {
		t.Fatal(err)
	}
	// A second call is a process restart: in-memory copies are gone.
	againPriv, againPub, err := LoadOrCreatePrivateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if againPriv != priv || againPub != pub {
		t.Fatalf("key must persist on this box")
	}
}

func TestLoadOrCreatePrivateKeyPermissions(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "wireguard")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	priv, _, err := LoadOrCreatePrivateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	assertMode(t, dir, 0o700)
	path := filepath.Join(dir, hostKeyFile)
	assertMode(t, path, 0o600)
	assertRootOwnedIfRoot(t, dir)
	assertRootOwnedIfRoot(t, path)

	// Reuse path must tighten a loose mode without rotating the key.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	again, _, err := LoadOrCreatePrivateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again != priv {
		t.Fatal("chmod must not rotate a valid key")
	}
	assertMode(t, dir, 0o700)
	assertMode(t, path, 0o600)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || string(raw) != priv+"\n" {
		t.Fatal("key file must be the private key plus a newline")
	}
}

func TestLoadOrCreateReplacesInvalidKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, hostKeyFile)
	if err := os.WriteFile(path, []byte("not-a-wireguard-key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	priv, pub, err := LoadOrCreatePrivateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if priv == "" || pub == "" || priv == "not-a-wireguard-key" {
		t.Fatalf("invalid key was not replaced")
	}
	if _, err := PublicFromPrivate(priv); err != nil {
		t.Fatal(err)
	}
	again, againPub, err := LoadOrCreatePrivateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again != priv || againPub != pub {
		t.Fatal("replaced key must stick")
	}
	assertMode(t, path, 0o600)
}

func TestPublicFromPrivateRejectsJunk(t *testing.T) {
	if _, err := PublicFromPrivate("not-a-key"); err == nil {
		t.Fatal("expected error")
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != want {
		t.Fatalf("%s mode %o, want %o", path, fi.Mode().Perm(), want)
	}
}

func assertRootOwnedIfRoot(t *testing.T, path string) {
	t.Helper()
	if os.Geteuid() != 0 {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("stat type")
	}
	if st.Uid != 0 || st.Gid != 0 {
		t.Fatalf("%s uid/gid %d/%d, want root", path, st.Uid, st.Gid)
	}
}
