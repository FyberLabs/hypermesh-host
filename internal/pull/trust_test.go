package pull

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

func TestToFileAbortsWhenStreamExceedsApprovedSize(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "model.gguf")
	const approved int64 = 10
	err := ToFileWith(context.Background(), dest, "https://huggingface.co/bartowski/x/resolve/main/model.gguf",
		"sha256:"+strings.Repeat("ab", 32),
		func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(strings.Repeat("x", 100))), nil
		},
		plentyLimits(approved),
	)
	want := "size_exceeded: approved 10"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v want %q", err, want)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("dest written: %v", statErr)
	}
	if leftovers := partials(t, dir); len(leftovers) != 0 {
		t.Fatalf("partials %v", leftovers)
	}
}

func TestToFileAbortsWhenHEADLiesAboutLength(t *testing.T) {
	const approved = 10
	payload := strings.Repeat("y", 80)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(approved))
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	dest := filepath.Join(dir, "model.gguf")
	err := ToFileWith(context.Background(), dest, srv.URL,
		"sha256:"+strings.Repeat("ab", 32),
		HTTPGet(srv.Client()),
		Limits{
			FloorSet:  true,
			MarginSet: true,
			Stat: func(string) (int64, error) {
				return 1 << 40, nil
			},
		},
	)
	want := "size_exceeded: approved 10"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v want %q", err, want)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("dest written: %v", statErr)
	}
	if leftovers := partials(t, dir); len(leftovers) != 0 {
		t.Fatalf("partials %v", leftovers)
	}
}

func TestToFileReportsCopiedBytes(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "model.gguf")
	payload := []byte("fixture-weights")
	sum := sha256.Sum256(payload)
	pin := "sha256:" + hex.EncodeToString(sum[:])
	lim := plentyLimits(int64(len(payload)))
	var n int64
	var d time.Duration
	lim.Copied = func(got int64, elapsed time.Duration) {
		n = got
		d = elapsed
	}
	err := ToFileWith(context.Background(), dest, "https://huggingface.co/bartowski/x/resolve/main/model.gguf", pin,
		func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(string(payload))), nil
		},
		lim,
	)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(payload)) || d < 0 {
		t.Fatalf("copied %d duration %s", n, d)
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
