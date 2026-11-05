package terms

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestEmbeddedTermsHash(t *testing.T) {
	const want = "46620be8f650c44e8188b0e6143be0193f70f6bd901a47d3d5169785e66c7045"
	sum := sha256.Sum256(Text)
	got := hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("embedded terms sha256 = %s, want %s", got, want)
	}
	if SHA256() != want {
		t.Fatalf("SHA256() = %s, want %s", SHA256(), want)
	}
	if Version != "hypermesh-terms-inline-2026-11-05-r1" {
		t.Fatalf("version = %s", Version)
	}
	if len(Text) == 0 || Text[len(Text)-1] != '\n' {
		t.Fatal("embedded terms must keep the trailing newline")
	}
}
