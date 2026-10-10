package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAGXMemoryRange(t *testing.T) {
	for _, memory := range []int64{0, 16 << 30, 32 << 30, (56 << 30) - 1, (66 << 30) + 1} {
		f := goodFacts()
		f.MemoryBytes = memory
		if err := CheckAGX(f); err == nil || !strings.Contains(err.Error(), "AGX memory") {
			t.Fatalf("unsupported memory %d: %v", memory, err)
		}
	}
	for _, memory := range []int64{56 << 30, 64 << 30, 66 << 30} {
		f := goodFacts()
		f.MemoryBytes = memory
		if err := CheckAGX(f); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNvidiaRuntimeResolvesConfiguredExecutable(t *testing.T) {
	dir := t.TempDir()
	runtime := filepath.Join(dir, "nvidia-container-runtime")
	if err := os.WriteFile(runtime, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(runtime, alias); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"nvidia-container-runtime", true}, {runtime, true}, {alias, true}, {"runc", false}, {"/stale/nvidia-container-runtime", false}, {"/bin/sh", false}, {"", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			source := Source{ReadFile: func(string) ([]byte, error) { return []byte(`{"runtimes":{"nvidia":{"path":"` + tc.path + `"}}}`), nil }, LookPath: func(name string) (string, error) {
				if name == "nvidia-container-runtime" {
					return runtime, nil
				}
				if name == alias || name == runtime || name == "/bin/sh" {
					return name, nil
				}
				return "", os.ErrNotExist
			}}
			if got := source.nvidiaRuntime(); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
