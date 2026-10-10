package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExplicitGGUFPathWins(t *testing.T) {
	const homeModel = "/home/operator/hypermesh/workload/model.gguf"
	t.Setenv("HYPERMESH_GGUF_PATH", homeModel)
	cfg := FromEnv()
	if cfg.GGUFHostPath() != homeModel {
		t.Fatalf("explicit path %q", cfg.GGUFHostPath())
	}
	t.Setenv("HYPERMESH_GGUF_PATH", "")
	cfg = FromEnv()
	if cfg.GGUFHostPath() != DefaultModelPath {
		t.Fatalf("default path %q", cfg.GGUFHostPath())
	}
}

func TestEnsureModelDirPermissions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "var", "lib", "hypermesh", "models", "model.gguf")
	if err := ensureModelDir(path); err != nil {
		t.Fatal(err)
	}
	models := filepath.Dir(path)
	fi, err := os.Stat(models)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o755 {
		t.Fatalf("models mode %o", fi.Mode().Perm())
	}
	parent, err := os.Stat(filepath.Dir(models))
	if err != nil {
		t.Fatal(err)
	}
	if parent.Mode().Perm() != 0o700 {
		t.Fatalf("state dir mode %o", parent.Mode().Perm())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("ensure must not create the weights file")
	}
}

func TestDiskFloorExplicitZero(t *testing.T) {
	cfg := FromEnv()
	cfg.TenantID = testTenant
	cfg.DiskFloor = "0"
	cfg.DiskMargin = "0"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	floor, err := cfg.DiskFloorBytes()
	if err != nil || floor != 0 {
		t.Fatalf("floor %d err=%v", floor, err)
	}
	cfg.DiskFloor = "nope"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid floor")
	}
}
