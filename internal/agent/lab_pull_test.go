package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

func TestLabPullAcceptsPinnedLocalHashWithoutDocker(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("10.0"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	path, pin := writeTestGGUF(t, dir)
	a.cfg.GGUFPath = path

	var dockerCalls int
	a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		dockerCalls++
		t.Fatalf("lab_pull must not call docker: %v", args)
		return nil, nil
	}}

	res := a.runJob(context.Background(), api.Job{
		JobID:       "lab-pull-1",
		Kind:        api.KindLabPull,
		DeviceID:    testDevice,
		CatalogID:   "hf-test-artifact",
		ImageDigest: pin,
	})
	if !res.Passed || res.Reason != "" {
		t.Fatalf("expected pass, got %+v", res.Result)
	}
	if res.ImageHash != pin {
		t.Fatalf("image_hash=%q want %q", res.ImageHash, pin)
	}
	if dockerCalls != 0 {
		t.Fatalf("docker calls=%d", dockerCalls)
	}
}

func TestLabPullFailsClosedOnHashMismatchAndMissingPin(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("10.0"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	path, _ := writeTestGGUF(t, dir)
	a.cfg.GGUFPath = path
	a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		t.Fatalf("lab_pull must not call docker: %v", args)
		return nil, nil
	}}

	wrong := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	mismatch := a.runJob(context.Background(), api.Job{
		JobID:       "lab-pull-bad",
		Kind:        api.KindLabPull,
		DeviceID:    testDevice,
		ImageDigest: wrong,
	})
	if mismatch.Passed || mismatch.Reason != "artifact hash mismatch" {
		t.Fatalf("mismatch: %+v", mismatch.Result)
	}

	missing := a.runJob(context.Background(), api.Job{
		JobID:       "lab-pull-nopin",
		Kind:        api.KindLabPull,
		DeviceID:    testDevice,
		ImageDigest: "bartowski/Llama@main/file.gguf",
	})
	if missing.Passed || missing.Reason != "pinned hash required" {
		t.Fatalf("missing pin: %+v", missing.Result)
	}
}
