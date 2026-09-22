package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestLabPullFetchesAllowlistedSourceViaStub(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("10.0"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	dest := filepath.Join(dir, "pulled.gguf")
	a.cfg.GGUFPath = dest
	payload := []byte("pulled-weights")
	tmp := filepath.Join(dir, "tmp.gguf")
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	pin, err := runtime.HashGGUFFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(tmp)

	var fetchCalls int
	a.fetchWeights = func(_ context.Context, destPath, sourceRef, wantPin string) error {
		fetchCalls++
		if !strings.Contains(sourceRef, "bartowski/") {
			t.Fatalf("source_ref %q", sourceRef)
		}
		if wantPin != pin {
			t.Fatalf("pin %q", wantPin)
		}
		return os.WriteFile(destPath, payload, 0o644)
	}
	a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		t.Fatalf("lab_pull must not call docker: %v", args)
		return nil, nil
	}}

	res := a.runJob(context.Background(), api.Job{
		JobID:       "lab-pull-fetch",
		Kind:        api.KindLabPull,
		DeviceID:    testDevice,
		CatalogID:   "hf-bartowski-llama",
		ImageDigest: pin,
		SourceRef:   "bartowski/Llama-3.1-8B-Instruct-GGUF@main/model.gguf",
	})
	if !res.Passed || res.ImageHash != pin {
		t.Fatalf("fetch pass: %+v", res.Result)
	}
	if fetchCalls != 1 {
		t.Fatalf("fetchCalls=%d", fetchCalls)
	}

	a.fetchWeights = nil
	denied := a.runJob(context.Background(), api.Job{
		JobID:       "lab-pull-evil",
		Kind:        api.KindLabPull,
		DeviceID:    testDevice,
		ImageDigest: pin,
		SourceRef:   "evilcorp/secret@main/model.gguf",
	})
	if denied.Passed || denied.Reason != "untrusted source" {
		t.Fatalf("untrusted: %+v", denied.Result)
	}
}
