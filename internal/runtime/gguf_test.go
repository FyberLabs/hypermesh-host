package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRequireGGUFFile(t *testing.T) {
	if err := RequireGGUFFile(""); !errors.Is(err, ErrNoGGUF) {
		t.Fatalf("empty: %v", err)
	}
	if err := RequireGGUFFile("   "); !errors.Is(err, ErrNoGGUF) {
		t.Fatalf("blank: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing.gguf")
	if err := RequireGGUFFile(missing); !errors.Is(err, ErrGGUFNotFile) {
		t.Fatalf("missing: %v", err)
	}
	dir := t.TempDir()
	if err := RequireGGUFFile(dir); !errors.Is(err, ErrGGUFNotFile) {
		t.Fatalf("directory: %v", err)
	}
	path := filepath.Join(dir, "model.gguf")
	if err := os.WriteFile(path, []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RequireGGUFFile(path); err != nil {
		t.Fatalf("regular file: %v", err)
	}
}

func TestHashGGUFFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model.gguf")
	body := []byte("fixture-gguf-weights")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := HashGGUFFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	want := "sha256:" + hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("hash %q want %q", got, want)
	}
	if _, err := HashGGUFFile(""); !errors.Is(err, ErrNoGGUF) {
		t.Fatalf("empty hash: %v", err)
	}
}
