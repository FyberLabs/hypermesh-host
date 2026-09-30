//go:build linux

package installer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxProbeUsesExistingFilesystem(t *testing.T) {
	directory := t.TempDir()
	r, err := probeResources(context.Background(), directory, "uma", 12*GiB)
	if err != nil { t.Fatal(err) }
	if r.SystemBytes <= 0 || r.AvailableSystemBytes <= 0 || r.DiskFreeBytes < 0 || !r.UMA {
		t.Fatalf("invalid local measurements: %+v", r)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 { t.Fatal("probe modified directory") }
	missing := filepath.Join(directory, "missing")
	if _, err := probeResources(context.Background(), missing, "uma", 12*GiB); err == nil {
		t.Fatal("missing filesystem path accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) { t.Fatal("probe created directory") }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := probeResources(ctx, directory, "uma", 12*GiB); err == nil { t.Fatal("ignored cancellation") }
}
