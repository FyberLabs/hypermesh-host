package tunnel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreatePrivateKeyStaysOnBox(t *testing.T) {
	dir := t.TempDir()
	priv, pub, err := LoadOrCreatePrivateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if priv == "" || pub == "" || priv == pub {
		t.Fatalf("pair empty or equal")
	}
	againPriv, againPub, err := LoadOrCreatePrivateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if againPriv != priv || againPub != pub {
		t.Fatalf("key must persist on this box")
	}
	raw, err := os.ReadFile(filepath.Join(dir, hostKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(dir, hostKeyFile)); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", st, err)
	}
	if string(raw) == "" {
		t.Fatal("empty key file")
	}
}

func TestPublicFromPrivateRejectsJunk(t *testing.T) {
	if _, err := PublicFromPrivate("not-a-key"); err == nil {
		t.Fatal("expected error")
	}
}
