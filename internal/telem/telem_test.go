package telem

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCollectFromFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "uptime"), []byte("12.7 99.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "image"), []byte("pinned-hash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	zone := filepath.Join(dir, "thermal_zone0")
	if err := os.Mkdir(zone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zone, "temp"), []byte("41500\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := Source{
		ImageHashFile: filepath.Join(dir, "image"),
		UptimePath:    filepath.Join(dir, "uptime"),
		ThermalRoot:   dir,
		LookPath:      func(string) (string, error) { return "/bin/nvpmodel", nil },
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte("NV Power Mode: MODE_30W\n2\n"), nil
		},
	}
	snap, err := s.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.ImageHash != "pinned-hash" {
		t.Fatalf("hash %q", snap.ImageHash)
	}
	if snap.UptimeS != 12 {
		t.Fatalf("uptime %d", snap.UptimeS)
	}
	if snap.ThermalC == nil || *snap.ThermalC != 41.5 {
		t.Fatalf("thermal %+v", snap.ThermalC)
	}
	if snap.Nvpmodel == "" || snap.Nvpmodel[:2] != "NV" {
		t.Fatalf("nvpmodel %q", snap.Nvpmodel)
	}
}

func TestImageHashFromTegraRelease(t *testing.T) {
	dir := t.TempDir()
	tegra := filepath.Join(dir, "nv_tegra_release")
	if err := os.WriteFile(tegra, []byte("# R39 (release), REVISION: 2.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "uptime"), []byte("1.0 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Source{
		TegraRelease: tegra,
		UptimePath:   filepath.Join(dir, "uptime"),
		LookPath:     func(string) (string, error) { return "", os.ErrNotExist },
	}
	snap, err := s.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.ImageHash == "" || snap.ImageHash[:7] != "sha256:" {
		t.Fatalf("expected sha256 image hash, got %q", snap.ImageHash)
	}
}

func TestBootIDFromProcFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "uptime"), []byte("4.2 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	boot := filepath.Join(dir, "boot_id")
	if err := os.WriteFile(boot, []byte("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Source{
		UptimePath: filepath.Join(dir, "uptime"),
		BootIDPath: boot,
		LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
	}
	snap, err := s.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.BootID != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Fatalf("boot_id %q", snap.BootID)
	}
}

func TestMissingBootIDFileOK(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "uptime"), []byte("1.0 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Source{
		UptimePath: filepath.Join(dir, "uptime"),
		BootIDPath: filepath.Join(dir, "missing-boot-id"),
		LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
	}
	snap, err := s.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.BootID != "" {
		t.Fatalf("boot_id should be absent, got %q", snap.BootID)
	}
}

func TestMissingThermalAndNvpmodelOK(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "uptime"), []byte("0.0 0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Source{
		UptimePath:  filepath.Join(dir, "uptime"),
		ThermalRoot: filepath.Join(dir, "missing"),
		LookPath:    func(string) (string, error) { return "", os.ErrNotExist },
	}
	snap, err := s.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.ThermalC != nil {
		t.Fatalf("thermal should be absent")
	}
	if snap.Nvpmodel != "" {
		t.Fatalf("nvpmodel should be absent")
	}
	if snap.UptimeS != 0 {
		t.Fatalf("uptime %d", snap.UptimeS)
	}
}
