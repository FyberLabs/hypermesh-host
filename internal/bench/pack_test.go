package bench

import (
	"context"
	"strings"
	"testing"
)

func TestFirstRepoDigestIgnoresBareHashes(t *testing.T) {
	raw := []byte(`["sha256:` + strings.Repeat("ab", 32) + `"]`)
	if got := FirstRepoDigest(raw); got != "" {
		t.Fatalf("bare hash must not tag: %q", got)
	}
}

func TestFirstRepoDigestKeepsRepoPin(t *testing.T) {
	want := "ghcr.io/fyberlabs/hypermesh-test@sha256:" + strings.Repeat("ab", 32)
	raw := []byte(`["` + want + `"]`)
	if got := FirstRepoDigest(raw); got != want {
		t.Fatalf("got %q", got)
	}
}

func TestPullRefusesAPin(t *testing.T) {
	err := Pull(context.Background(), "ghcr.io/fyberlabs/hypermesh-test@sha256:"+strings.Repeat("ab", 32))
	if err == nil {
		t.Fatal("pull must not accept a repo pin")
	}
}

func TestLlamaImageIsTheOnlyLocalPin(t *testing.T) {
	if LocalImage("llama-3.1-8b-q4") == "" {
		t.Fatal("8B workload image should be inspectable")
	}
	if LocalImage("whisper-small") != "" || LocalImage("qwen-2.5-32b-q4") != "" {
		t.Fatal("unexpected image for a row this box does not build")
	}
}
