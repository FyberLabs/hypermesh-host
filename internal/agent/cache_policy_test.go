package agent

import (
	"bytes"
	"context"
	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestQuotaPolicyEvictsIdleLastDownloadedAndProtectsOwnerPin(t *testing.T) {
	a, cache := newCacheAgent(t)
	old := seedCachedModel(t, cache, bytes.Repeat([]byte{'o'}, 100), time.Now().Add(-time.Hour))
	writeSeedIndex(t, cache, cacheIndex{Loaded: old, Models: []cacheModel{{ID: old, Bytes: 100, LastUsed: time.Now().Add(-time.Hour)}}})
	dir := filepath.Dir(cache)
	if err := WriteCachePolicy(dir, CachePolicy{Schema: 1, MaxBytes: 100}); err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte{'n'}, 100)
	pin := shaPin(body)
	a.fetchWeights = func(_ context.Context, dest, _, _ string) error { return os.WriteFile(dest, body, 0600) }
	if res := pullModel(t, a, pin, 100); !res.Passed {
		t.Fatal(res)
	}
	if _, err := os.Stat(filepath.Join(cache, fileName(old))); !os.IsNotExist(err) {
		t.Fatal("idle last-used file not evicted")
	}
	a.clearPendingModel("cache-model")
	if err := WriteCachePolicy(dir, CachePolicy{Schema: 1, MaxBytes: 100, Pins: []string{pin}}); err != nil {
		t.Fatal(err)
	}
	if res := pullModel(t, a, old, 100); res.Passed {
		t.Fatal("evicted owner pin")
	}
}
func TestConcurrentDemandReusesVerifiedArtifact(t *testing.T) {
	a, _ := newCacheAgent(t)
	body := bytes.Repeat([]byte{'n'}, 100)
	pin := shaPin(body)
	var downloads atomic.Int32
	a.fetchWeights = func(_ context.Context, dest, _, _ string) error {
		downloads.Add(1)
		time.Sleep(10 * time.Millisecond)
		return os.WriteFile(dest, body, 0600)
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if res := pullModel(t, a, pin, 100); !res.Passed {
				t.Error(res)
			}
		}()
	}
	wg.Wait()
	if downloads.Load() != 1 {
		t.Fatalf("downloads=%d", downloads.Load())
	}
}
func TestCachePolicyAndLockFailSafely(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(t.TempDir(), "policy")
	os.WriteFile(other, []byte(`{}`), 0600)
	os.Symlink(other, filepath.Join(dir, cachePolicyName))
	if _, err := ReadCachePolicy(dir); err == nil {
		t.Fatal("symlink policy accepted")
	}
	unlock, err := LockModelCache(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if release, err := LockModelCache(ctx, dir); err == nil {
		release()
		t.Fatal("overlapping transfer lock")
	}
}

func TestLeaseStartDownloadsSelectedModelOnDemand(t *testing.T) {
	a, cache := newCacheAgent(t)
	a.bindIP = "10.77.0.2/32"
	a.modelFit = func(context.Context, int64) error { return nil }
	body := bytes.Repeat([]byte{'m'}, 100)
	pin := shaPin(body)
	var fetches int
	a.fetchWeights = func(_ context.Context, dest, _, _ string) error { fetches++; return os.WriteFile(dest, body, 0600) }
	var mounted string
	a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		for i, arg := range args {
			if arg == "-v" && i+1 < len(args) {
				mounted = args[i+1]
			}
		}
		return []byte("cid"), nil
	}}
	result := a.leaseStart(context.Background(), api.Job{JobID: "demand", LeaseID: "demand", CatalogID: "selected", ImageDigest: "example.invalid/model@sha256:" + strings.Repeat("a", 64), FileHash: pin, SourceRef: "bartowski/model@" + strings.Repeat("b", 40) + "/model.gguf", SizeBytes: 100, Runtime: "nvidia"})
	if !result.Passed || fetches != 1 || !strings.HasPrefix(mounted, filepath.Join(cache, fileName(pin))) {
		t.Fatalf("result=%+v fetches=%d mount=%s", result, fetches, mounted)
	}
}

func TestPrivateModelCredentialValidation(t *testing.T) {
	a, cache := newCacheAgent(t)
	dir := filepath.Dir(cache)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(dir, "hf-token")
	os.WriteFile(tokenFile, []byte("hf_private_test_credential"), 0644)
	if err := WriteCachePolicy(dir, CachePolicy{Schema: 1, MaxBytes: 100, TokenFile: tokenFile}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.modelFetcher(); err == nil || strings.Contains(err.Error(), "hf_private") {
		t.Fatal("unsafe credential accepted or exposed", err)
	}
	os.Chmod(tokenFile, 0600)
	if _, err := a.modelFetcher(); err != nil {
		t.Fatal(err)
	}
}
