package tunnel

import (
	"crypto/rand"
	"encoding/base64"
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

func TestGenerateKeypairClampsPrivateKey(t *testing.T) {
	// A random scalar is already clamped about 1/32 of the time. Several
	// draws make a missing clamp fail instead of flaking.
	for i := 0; i < 32; i++ {
		priv, pub, err := GenerateKeypair()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := base64.StdEncoding.DecodeString(priv)
		if err != nil || len(raw) != 32 {
			t.Fatal("generated private key is not 32-byte standard base64")
		}
		if raw[0]&7 != 0 || raw[31]&128 != 0 || raw[31]&64 == 0 {
			t.Fatal("generated private key is not clamped")
		}
		got, err := PublicFromPrivate(priv)
		if err != nil || got != pub {
			t.Fatal("public key does not match the clamped private key")
		}
	}
}

func TestLoadOrCreateLeavesUnclampedKeyFile(t *testing.T) {
	dir := t.TempDir()
	raw := forcedUnclampedScalar(t)
	stored := base64.StdEncoding.EncodeToString(raw)
	path := filepath.Join(dir, hostKeyFile)
	body := stored + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	priv, pub, err := LoadOrCreatePrivateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if priv != stored {
		t.Fatal("unclamped key was replaced")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("existing key file was rewritten")
	}
	clamped := append([]byte(nil), raw...)
	clampX25519(clamped)
	want, err := PublicFromPrivate(base64.StdEncoding.EncodeToString(clamped))
	if err != nil || want == "" || want != pub {
		t.Fatal("unclamped file and its clamped form diverged")
	}
	again, _, err := LoadOrCreatePrivateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again != stored {
		t.Fatal("second load rewrote the unclamped key")
	}
}

func TestSameWGPrivateKeyClamp(t *testing.T) {
	raw := forcedUnclampedScalar(t)
	clamped := append([]byte(nil), raw...)
	clampX25519(clamped)
	stored := base64.StdEncoding.EncodeToString(raw)
	live := base64.StdEncoding.EncodeToString(clamped)
	if stored == live {
		t.Fatal("fixture encodings did not differ")
	}
	if !sameWGPrivateKey(stored, live) || !sameWGPrivateKey(live, stored) {
		t.Fatal("unclamped stored key and clamped live key must match")
	}
	if !sameWGPrivateKey(base64.RawStdEncoding.EncodeToString(raw), live) {
		t.Fatal("unclamped and clamped private keys must match across base64 forms")
	}
	// Public keys are not scalars. The raw compare must still see this difference.
	if sameWGKey(stored, live) {
		t.Fatal("public key compare must not ignore clamp bits")
	}

	other := append([]byte(nil), raw...)
	other[1] ^= 0x01
	otherKey := base64.StdEncoding.EncodeToString(other)
	if sameWGPrivateKey(stored, otherKey) || sameWGPrivateKey(live, otherKey) {
		t.Fatal("different private keys must not match")
	}
	a, _, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	if sameWGPrivateKey(a, b) || sameWGPrivateKey(stored, a) {
		t.Fatal("different private keys must not match")
	}

	good := live
	malformed := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"blank", "   "},
		{"none", "(none)"},
		{"text", "not-a-key"},
		{"short", base64.StdEncoding.EncodeToString(make([]byte, 16))},
		{"len31", base64.StdEncoding.EncodeToString(make([]byte, 31))},
		{"len33", base64.StdEncoding.EncodeToString(make([]byte, 33))},
	}
	for _, bad := range malformed {
		if sameWGPrivateKey(bad.key, good) || sameWGPrivateKey(good, bad.key) {
			t.Fatalf("malformed key %s matched a private key", bad.name)
		}
	}
	if sameWGPrivateKey("not-a-key", "also-not-a-key") {
		t.Fatal("different malformed keys matched")
	}
}

// forcedUnclampedScalar is 32 random bytes with the bits the X25519 clamp
// clears set, so its encoding differs from the form WireGuard reports.
func forcedUnclampedScalar(t *testing.T) []byte {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	raw[0] |= 0x07
	raw[31] |= 0x80
	raw[31] &^= 0x40
	return raw
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
