package deploy

import (
	"context"
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
			source := Source{Output: func(context.Context, string, ...string) ([]byte, error) {
				return []byte(`{"nvidia":{"path":"` + tc.path + `"}}`), nil
			}, LookPath: func(name string) (string, error) {
				if name == "nvidia-container-runtime" {
					return runtime, nil
				}
				if name == alias || name == runtime || name == "/bin/sh" {
					return name, nil
				}
				return "", os.ErrNotExist
			}}
			_, got := source.dockerState(context.Background())
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestDockerStateRequiresLiveDaemon(t *testing.T) {
	for _, tc := range []struct {
		name, output    string
		err             error
		docker, runtime bool
	}{
		{"stopped", `{"nvidia":{"path":"nvidia-container-runtime"}}`, os.ErrPermission, false, false},
		{"invalid", `invalid`, nil, false, false},
		{"null", `null`, nil, false, false},
		{"not restarted", `{"runc":{"path":"runc"}}`, nil, true, false},
		{"configured", `{"nvidia":{"path":"nvidia-container-runtime"}}`, nil, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := Source{ReadFile: func(string) ([]byte, error) {
				return []byte(`{"runtimes":{"nvidia":{"path":"nvidia-container-runtime"}}}`), nil
			}, LookPath: func(string) (string, error) { return "/usr/bin/nvidia-container-runtime", nil }, Output: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "docker" || len(args) != 4 || args[0] != "--host=unix:///var/run/docker.sock" || args[1] != "info" {
					t.Fatalf("unexpected query %s %v", name, args)
				}
				return []byte(tc.output), tc.err
			}}
			docker, runtime := source.dockerState(context.Background())
			if docker != tc.docker || runtime != tc.runtime {
				t.Fatalf("got %v/%v want %v/%v", docker, runtime, tc.docker, tc.runtime)
			}
		})
	}
}
