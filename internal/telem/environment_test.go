package telem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvironmentTracksSoftwareAndPower(t *testing.T) {
	dir := t.TempDir()
	cuda := filepath.Join(dir, "cuda.json")
	packages := filepath.Join(dir, "packages")
	os.WriteFile(cuda, []byte(`{"cuda":{"version":"13.2.1"}}`), 0600)
	os.WriteFile(packages, []byte("nvidia-container-toolkit 1.0"), 0600)
	s := Source{EnvironmentFiles: []string{cuda, packages}}
	first := s.environmentHash("30W")
	if len(first) != 71 || first != s.environmentHash("30W") {
		t.Fatal("unstable inventory")
	}
	if first == s.environmentHash("MAXN") {
		t.Fatal("power change ignored")
	}
	os.WriteFile(cuda, []byte(`{"cuda":{"version":"13.2.2"}}`), 0600)
	second := s.environmentHash("30W")
	if first == second {
		t.Fatal("CUDA patch change ignored")
	}
	os.WriteFile(packages, []byte("nvidia-container-toolkit 1.1"), 0600)
	if second == s.environmentHash("30W") {
		t.Fatal("package update ignored")
	}
	s.EnvironmentFiles = []string{packages, cuda}
	ordered := s.environmentHash("30W")
	s.EnvironmentFiles = []string{cuda, packages}
	if ordered != s.environmentHash("30W") {
		t.Fatal("ordering changed inventory")
	}
}

func TestEnvironmentUnknownInventoryFailsClosed(t *testing.T) {
	dir := t.TempDir()
	s := Source{EnvironmentFiles: []string{filepath.Join(dir, "optional")}}
	missing := s.environmentHash("")
	if missing == "" {
		t.Fatal("missing optional file should be stable")
	}
	// A directory cannot be inventoried as a version file.
	s.EnvironmentFiles = []string{dir}
	if s.environmentHash("") != "" {
		t.Fatal("unreadable inventory must be unknown")
	}
}
