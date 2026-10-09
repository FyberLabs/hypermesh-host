//go:build linux

package installer

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestInstalledHostCLI(t *testing.T) {
	root := t.TempDir()
	path := agentDest(root, installedHostCLI)
	old := bytes.Repeat([]byte("x"), 3<<20)
	next := []byte("verified new")
	if err := installHostCLI(root, old, ""); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("CLI is not executable", err)
	}
	if err := installHostCLI(root, next, ""); err == nil {
		t.Fatal("replaced unapproved existing CLI")
	}
	if err := installHostCLI(root, next, digestSetupBytes(old)); err != nil {
		t.Fatal(err)
	}
	if err := installHostCLI(root, next, digestSetupBytes(old)); err != nil {
		t.Fatal("retry failed", err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != string(next) {
		t.Fatal("wrong installed bytes")
	}
}

func TestInstalledHostCLIRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	path := agentDest(root, installedHostCLI)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "other")
	os.WriteFile(target, []byte("keep"), 0755)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if installHostCLI(root, []byte("replacement"), "") == nil {
		t.Fatal("followed symlink")
	}
	raw, _ := os.ReadFile(target)
	if string(raw) != "keep" {
		t.Fatal("changed target")
	}
}
