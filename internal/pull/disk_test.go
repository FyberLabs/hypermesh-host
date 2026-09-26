package pull

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCapacityFromBlocksOverflow(t *testing.T) {
	_, err := capacityFromBlocks(uint64(math.MaxInt64)/4096+1, 4096)
	if err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("overflow: %v", err)
	}
	dir := t.TempDir()
	got, err := CapacityBytes(dir)
	if err != nil {
		t.Fatal(err)
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		t.Fatal(err)
	}
	want := int64(st.Blocks) * int64(st.Bsize)
	if got != want {
		t.Fatalf("CapacityBytes %d want %d", got, want)
	}
	var free, size int64
	var gotFree int64
	var matched bool
	for i := 0; i < 20 && !matched; i++ {
		free, size, err = StatfsBytes(dir)
		if err != nil {
			t.Fatal(err)
		}
		gotFree, err = FreeBytes(dir)
		if err != nil {
			t.Fatal(err)
		}
		matched = gotFree == free && size == want
	}
	if !matched {
		t.Fatalf("FreeBytes %d statfs free %d size %d want %d", gotFree, free, size, want)
	}
}

func TestPreflightRefusesBeforeAnyWrite(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "models", "model.gguf")
	const (
		size   int64  = 1000
		floor  uint64 = 2000
		margin uint64 = 50
		free   int64  = 100
	)
	var hits int
	err := ToFileWith(context.Background(), dest, "https://huggingface.co/bartowski/x/resolve/main/model.gguf",
		"sha256:"+strings.Repeat("ab", 32),
		func(context.Context, string) (io.ReadCloser, error) {
			hits++
			return io.NopCloser(strings.NewReader("nope")), nil
		},
		Limits{
			Expected:  size,
			Floor:     floor,
			Margin:    margin,
			FloorSet:  true,
			MarginSet: true,
			Stat: func(string) (int64, error) {
				return free, nil
			},
		},
	)
	want := "insufficient_disk: need 3050, free 100"
	if err == nil || err.Error() != want {
		t.Fatalf("reason %v want %q", err, want)
	}
	if hits != 0 {
		t.Fatalf("fetch calls=%d", hits)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("dest written: %v", statErr)
	}
	if leftovers := partials(t, dir); len(leftovers) != 0 {
		t.Fatalf("partials %v", leftovers)
	}
	if _, statErr := os.Stat(filepath.Dir(dest)); !os.IsNotExist(statErr) {
		t.Fatalf("preflight created %s: %v", filepath.Dir(dest), statErr)
	}
}

func TestPreflightUnknownSizeDownloadsNothing(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "models", "model.gguf")
	var hits int
	err := ToFileWith(context.Background(), dest, "https://huggingface.co/bartowski/x/resolve/main/model.gguf",
		"sha256:"+strings.Repeat("cd", 32),
		func(context.Context, string) (io.ReadCloser, error) {
			hits++
			return io.NopCloser(strings.NewReader("x")), nil
		},
		Limits{
			FloorSet:  true,
			MarginSet: true,
			Stat: func(string) (int64, error) {
				return 9_000_000_000, nil
			},
			Head: func(context.Context, string) (int64, error) {
				return 0, errors.New("no content length")
			},
		},
	)
	if err == nil || err.Error() != "insufficient_disk: size unknown, free 9000000000" {
		t.Fatalf("got %v", err)
	}
	if hits != 0 {
		t.Fatalf("fetch calls=%d", hits)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatal("dest written")
	}
}

func TestToFileRemovesPartialOnCopyError(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "model.gguf")
	err := ToFileWith(context.Background(), dest, "https://huggingface.co/bartowski/x/resolve/main/model.gguf",
		"sha256:"+strings.Repeat("ab", 32),
		func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(errReader{}), nil
		},
		plentyLimits(32),
	)
	if err == nil {
		t.Fatal("expected copy error")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatal("dest written")
	}
	if leftovers := partials(t, dir); len(leftovers) != 0 {
		t.Fatalf("partials %v", leftovers)
	}
}

func TestToFileRefusesRootPath(t *testing.T) {
	err := ToFileWith(context.Background(), "/model.gguf", "https://huggingface.co/bartowski/x/resolve/main/model.gguf",
		"sha256:"+strings.Repeat("ab", 32),
		func(context.Context, string) (io.ReadCloser, error) {
			t.Fatal("fetched")
			return nil, nil
		},
		plentyLimits(1),
	)
	if err == nil || !strings.Contains(err.Error(), "root filesystem") {
		t.Fatalf("got %v", err)
	}
}

func TestFreeFromBlocksOverflow(t *testing.T) {
	_, err := freeFromBlocks(uint64(math.MaxInt64)/4096+1, 4096)
	if err == nil || err.Error() != "hypermesh: statfs free bytes overflow" {
		t.Fatalf("got %v", err)
	}
	_, err = freeFromBlocks(1, 0)
	if err == nil || err.Error() != "hypermesh: statfs block size" {
		t.Fatalf("got %v", err)
	}
	got, err := freeFromBlocks(3, 4096)
	if err != nil || got != 12288 {
		t.Fatalf("got %d %v", got, err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}
