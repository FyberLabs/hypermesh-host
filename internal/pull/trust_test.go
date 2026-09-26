package pull

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrustedDownloadURLAllowlist(t *testing.T) {
	got, err := TrustedDownloadURL("bartowski/Llama-3.1-8B-Instruct-GGUF@main/model.gguf")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "https://huggingface.co/bartowski/") {
		t.Fatalf("url %q", got)
	}

	_, err = TrustedDownloadURL("evilcorp/secret@main/model.gguf")
	if err == nil || !strings.Contains(err.Error(), "untrusted publisher") {
		t.Fatalf("want untrusted publisher, got %v", err)
	}

	_, err = TrustedDownloadURL("https://evil.example/weights.gguf")
	if err == nil || !strings.Contains(err.Error(), "untrusted source host") {
		t.Fatalf("want untrusted host, got %v", err)
	}
}

func TestToFileVerifiesPinWithoutNetwork(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "model.gguf")
	payload := []byte("fixture-weights")
	sum := sha256.Sum256(payload)
	pin := "sha256:" + hex.EncodeToString(sum[:])

	var hits int
	err := ToFileWith(context.Background(), dest, "https://huggingface.co/bartowski/x/resolve/main/model.gguf", pin,
		func(_ context.Context, url string) (io.ReadCloser, error) {
			hits++
			if !strings.Contains(url, "huggingface.co") {
				t.Fatalf("url %q", url)
			}
			return io.NopCloser(strings.NewReader(string(payload))), nil
		},
		plentyLimits(int64(len(payload))),
	)
	if err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("hits=%d", hits)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("dest %q err=%v", got, err)
	}

	err = ToFileWith(context.Background(), dest, "https://huggingface.co/bartowski/x/resolve/main/model.gguf",
		"sha256:"+strings.Repeat("ab", 32),
		func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(string(payload))), nil
		},
		plentyLimits(int64(len(payload))),
	)
	if err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("want mismatch, got %v", err)
	}
	if leftovers := partials(t, dir); len(leftovers) != 0 {
		t.Fatalf("mismatch left partial files: %v", leftovers)
	}
}

func plentyLimits(n int64) Limits {
	return Limits{
		Expected:  n,
		Floor:     0,
		Margin:    0,
		FloorSet:  true,
		MarginSet: true,
		Stat: func(string) (int64, error) {
			return 1 << 40, nil
		},
	}
}

func partials(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "lab-pull-*.gguf"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}
