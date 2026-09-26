package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/pull"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

func TestModelCacheEvictsLeastRecentlyUsed(t *testing.T) {
	a, cache := newCacheAgent(t)
	a.cfg.ModelCacheMax = "200"
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	idA := seedCachedModel(t, cache, bytes.Repeat([]byte{'a'}, 100), old)
	idB := seedCachedModel(t, cache, bytes.Repeat([]byte{'b'}, 100), newer)
	writeSeedIndex(t, cache, cacheIndex{Models: []cacheModel{
		{ID: idA, Bytes: 100, LastUsed: old},
		{ID: idB, Bytes: 100, LastUsed: newer},
	}})
	now := time.Date(2026, 9, 26, 18, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	body := bytes.Repeat([]byte{'n'}, 100)
	pin := shaPin(body)
	var fetches int
	a.fetchWeights = func(_ context.Context, dest, _, _ string) error {
		fetches++
		if _, err := os.Stat(filepath.Join(cache, fileName(idA))); !os.IsNotExist(err) {
			t.Fatalf("oldest model still present at fetch: %v", err)
		}
		return os.WriteFile(dest, body, 0o600)
	}
	res := pullModel(t, a, pin, int64(len(body)))
	if !res.Passed || res.ImageHash != pin {
		t.Fatalf("pull: %+v", res.Result)
	}
	if fetches != 1 {
		t.Fatalf("fetches=%d", fetches)
	}
	if _, err := os.Stat(filepath.Join(cache, fileName(idA))); !os.IsNotExist(err) {
		t.Fatalf("oldest model not evicted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, fileName(idB))); err != nil {
		t.Fatalf("newer model missing: %v", err)
	}
	idx := readIndex(t, cache)
	if idx.Loaded != pin {
		t.Fatalf("loaded %q", idx.Loaded)
	}
	if findModel(idx, idA) != nil || findModel(idx, idB) == nil || findModel(idx, pin) == nil {
		t.Fatalf("index %+v", idx.Models)
	}
	if got := findModel(idx, pin).LastUsed; !got.Equal(now) {
		t.Fatalf("last_used %s", got)
	}
	used, evictable, ok := a.modelCacheUsage()
	if !ok || used != 200 || evictable != 100 {
		t.Fatalf("usage used=%d evictable=%d ok=%v", used, evictable, ok)
	}
}

func TestModelCacheNeverEvictsLoadedOrLeased(t *testing.T) {
	a, cache := newCacheAgent(t)
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	idA := seedCachedModel(t, cache, bytes.Repeat([]byte{'a'}, 100), base)
	idB := seedCachedModel(t, cache, bytes.Repeat([]byte{'b'}, 100), base.Add(time.Hour))
	idC := seedCachedModel(t, cache, bytes.Repeat([]byte{'c'}, 100), base.Add(2*time.Hour))
	idD := seedCachedModel(t, cache, bytes.Repeat([]byte{'d'}, 100), base.Add(3*time.Hour))
	writeSeedIndex(t, cache, cacheIndex{
		Loaded: idC,
		Models: []cacheModel{
			{ID: idA, Bytes: 100, LastUsed: base},
			{ID: idB, Bytes: 100, LastUsed: base.Add(time.Hour)},
			{ID: idC, Bytes: 100, LastUsed: base.Add(2 * time.Hour)},
			{ID: idD, Bytes: 100, LastUsed: base.Add(3 * time.Hour)},
		},
	})
	a.setLease(&runtime.Handle{LeaseID: "lease-1", FileHash: idD})
	body := bytes.Repeat([]byte{'n'}, 100)
	pin := shaPin(body)
	a.fetchWeights = func(_ context.Context, dest, _, _ string) error {
		return os.WriteFile(dest, body, 0o600)
	}
	res := pullModel(t, a, pin, 100)
	if !res.Passed {
		t.Fatalf("pull: %+v", res.Result)
	}
	for _, id := range []string{idA, idB} {
		if _, err := os.Stat(filepath.Join(cache, fileName(id))); !os.IsNotExist(err) {
			t.Fatalf("%s still on disk: %v", id, err)
		}
	}
	for _, id := range []string{idC, idD} {
		if _, err := os.Stat(filepath.Join(cache, fileName(id))); err != nil {
			t.Fatalf("pinned %s missing: %v", id, err)
		}
	}
	idx := readIndex(t, cache)
	if findModel(idx, idC) == nil || findModel(idx, idD) == nil {
		t.Fatalf("pinned dropped from index: %+v", idx)
	}
	if idx.Loaded != pin {
		t.Fatalf("loaded %q", idx.Loaded)
	}
}

func TestModelCacheRespectsBudget(t *testing.T) {
	a, cache := newCacheAgent(t)
	a.cfg.ModelCacheMax = "250"
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	idA := seedCachedModel(t, cache, bytes.Repeat([]byte{'a'}, 100), base)
	idB := seedCachedModel(t, cache, bytes.Repeat([]byte{'b'}, 100), base.Add(time.Hour))
	writeSeedIndex(t, cache, cacheIndex{Models: []cacheModel{
		{ID: idA, Bytes: 100, LastUsed: base},
		{ID: idB, Bytes: 100, LastUsed: base.Add(time.Hour)},
	}})
	body := bytes.Repeat([]byte{'n'}, 150)
	pin := shaPin(body)
	a.fetchWeights = func(_ context.Context, dest, _, _ string) error {
		return os.WriteFile(dest, body, 0o600)
	}
	res := pullModel(t, a, pin, 150)
	if !res.Passed {
		t.Fatalf("pull: %+v", res.Result)
	}
	idx := readIndex(t, cache)
	var sum int64
	for _, m := range idx.Models {
		sum += m.Bytes
	}
	if sum > 250 {
		t.Fatalf("cache bytes %d over budget", sum)
	}
	if _, err := os.Stat(filepath.Join(cache, fileName(idA))); !os.IsNotExist(err) {
		t.Fatal("oldest should have been evicted to meet the budget")
	}
	if _, err := os.Stat(filepath.Join(cache, fileName(idB))); err != nil {
		t.Fatal(err)
	}
}

func TestModelCacheEvictsToRestoreFreeSpace(t *testing.T) {
	a, cache := newCacheAgent(t)
	a.cfg.ModelCacheMax = "100000"
	a.statFree = func(string) (int64, error) { return 50, nil }
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	idA := seedCachedModel(t, cache, bytes.Repeat([]byte{'a'}, 1000), old)
	writeSeedIndex(t, cache, cacheIndex{Models: []cacheModel{{ID: idA, Bytes: 1000, LastUsed: old}}})
	body := bytes.Repeat([]byte{'n'}, 80)
	pin := shaPin(body)
	a.fetchWeights = func(_ context.Context, dest, _, _ string) error {
		if _, err := os.Stat(filepath.Join(cache, fileName(idA))); !os.IsNotExist(err) {
			t.Fatalf("model still present before download: %v", err)
		}
		return os.WriteFile(dest, body, 0o600)
	}
	res := pullModel(t, a, pin, 80)
	if !res.Passed {
		t.Fatalf("pull: %+v", res.Result)
	}
	if _, err := os.Stat(filepath.Join(cache, fileName(idA))); !os.IsNotExist(err) {
		t.Fatalf("evictable model remains: %v", err)
	}
}

func TestModelCacheDoesNotEvictWhenFreeSpaceStillFails(t *testing.T) {
	a, cache := newCacheAgent(t)
	a.cfg.ModelCacheMax = "100000"
	a.statFree = func(string) (int64, error) { return 10, nil }
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	idA := seedCachedModel(t, cache, bytes.Repeat([]byte{'a'}, 20), old)
	writeSeedIndex(t, cache, cacheIndex{Models: []cacheModel{{ID: idA, Bytes: 20, LastUsed: old}}})
	var fetches int
	a.fetchWeights = func(context.Context, string, string, string) error {
		fetches++
		return nil
	}
	body := bytes.Repeat([]byte{'n'}, 100)
	res := pullModel(t, a, shaPin(body), 100)
	if res.Passed || res.Reason != "insufficient_disk: need 100, free 10, evictable 20" {
		t.Fatalf("pull: %+v", res.Result)
	}
	if fetches != 0 {
		t.Fatalf("fetches=%d", fetches)
	}
	if _, err := os.Stat(filepath.Join(cache, fileName(idA))); err != nil {
		t.Fatalf("deleted before fit was known: %v", err)
	}
}

func TestModelCacheDoesNotEvictWhenPinnedModelsExceedBudget(t *testing.T) {
	a, cache := newCacheAgent(t)
	a.cfg.ModelCacheMax = "200"
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	idA := seedCachedModel(t, cache, bytes.Repeat([]byte{'a'}, 50), base)
	idC := seedCachedModel(t, cache, bytes.Repeat([]byte{'c'}, 100), base.Add(time.Hour))
	idD := seedCachedModel(t, cache, bytes.Repeat([]byte{'d'}, 100), base.Add(2*time.Hour))
	writeSeedIndex(t, cache, cacheIndex{
		Loaded: idC,
		Models: []cacheModel{
			{ID: idA, Bytes: 50, LastUsed: base},
			{ID: idC, Bytes: 100, LastUsed: base.Add(time.Hour)},
			{ID: idD, Bytes: 100, LastUsed: base.Add(2 * time.Hour)},
		},
	})
	a.setLease(&runtime.Handle{LeaseID: "lease-1", FileHash: idD})
	var fetches int
	a.fetchWeights = func(context.Context, string, string, string) error {
		fetches++
		return nil
	}
	a.statFree = func(string) (int64, error) { return 5000, nil }
	body := bytes.Repeat([]byte{'n'}, 150)
	pin := shaPin(body)
	res := pullModel(t, a, pin, 150)
	if res.Passed {
		t.Fatal("pinned models over budget must fail")
	}
	const want = "insufficient_disk: need 150, free 5000, evictable 50"
	if res.Reason != want {
		t.Fatalf("reason %q", res.Reason)
	}
	if fetches != 0 {
		t.Fatalf("fetch calls=%d", fetches)
	}
	for _, id := range []string{idA, idC, idD} {
		if _, err := os.Stat(filepath.Join(cache, fileName(id))); err != nil {
			t.Fatalf("%s deleted despite failed fit: %v", id, err)
		}
	}
}

func TestCacheIndexIgnoresTornTempAndRecovers(t *testing.T) {
	a, cache := newCacheAgent(t)
	body := bytes.Repeat([]byte{'k'}, 40)
	id := seedCachedModel(t, cache, body, time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC))
	kept := time.Date(2024, 5, 2, 3, 4, 5, 0, time.UTC)
	if err := writeCacheIndex(cache, cacheIndex{Models: []cacheModel{{ID: id, Bytes: 40, LastUsed: kept}}}); err != nil {
		t.Fatal(err)
	}
	torn := filepath.Join(cache, "index.json.tmp-partial")
	if err := os.WriteFile(torn, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.reconcileModelCache(); err != nil {
		t.Fatal(err)
	}
	idx := readIndex(t, cache)
	got := findModel(idx, id)
	if got == nil || !got.LastUsed.Equal(kept) {
		t.Fatalf("torn temp changed index: %+v", idx)
	}
	if _, err := os.Stat(torn); !os.IsNotExist(err) {
		t.Fatalf("startup left torn index temp: %v", err)
	}

	if err := os.WriteFile(filepath.Join(cache, cacheIndexName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	orphanBody := bytes.Repeat([]byte{'o'}, 12)
	orphan := seedCachedModel(t, cache, orphanBody, time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC))
	if err := a.reconcileModelCache(); err != nil {
		t.Fatal(err)
	}
	idx = readIndex(t, cache)
	if findModel(idx, id) == nil || findModel(idx, orphan) == nil {
		t.Fatalf("startup did not recover files: %+v", idx)
	}
	if _, err := os.ReadFile(filepath.Join(cache, cacheIndexName)); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(mustRead(t, filepath.Join(cache, cacheIndexName))) {
		t.Fatal("recovered index is not valid json")
	}
}

func TestLeaseStartUpdatesModelLastUsed(t *testing.T) {
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("10.0"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	hash := readyLeaseStart(t, a)
	cache := filepath.Join(filepath.Dir(a.cfg.GGUFPath), cacheDirName)
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(a.cfg.GGUFPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, fileName(hash)), body, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := writeCacheIndex(cache, cacheIndex{Models: []cacheModel{{ID: hash, Bytes: int64(len(body)), LastUsed: old}}}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 26, 18, 30, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("cid\n"), nil
	}}
	res := a.leaseStart(context.Background(), api.Job{
		JobID:       "lease-job",
		Kind:        api.KindLeaseStart,
		DeviceID:    testDevice,
		LeaseID:     "lease-9",
		CatalogID:   "llama-3.1-8b-q4",
		ImageDigest: "example.invalid/hypermesh/workload@sha256:" + stringsRepeat('a', 64),
		FileHash:    hash,
		Runtime:     "nvidia",
	})
	if !res.Passed {
		t.Fatalf("lease_start: %+v", res)
	}
	idx := readIndex(t, cache)
	got := findModel(idx, hash)
	if got == nil || !got.LastUsed.Equal(now) {
		t.Fatalf("last_used %+v", idx)
	}
}

func TestModelCacheUseRacesHeartbeat(t *testing.T) {
	a, cache := newCacheAgent(t)
	id := seedCachedModel(t, cache, []byte("race"), time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	writeSeedIndex(t, cache, cacheIndex{Models: []cacheModel{{ID: id, Bytes: 4, LastUsed: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}}})
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			a.touchModelUse(id)
		}()
		go func() {
			defer wg.Done()
			_, _, _ = a.modelCacheUsage()
		}()
	}
	wg.Wait()
	if _, _, ok := a.modelCacheUsage(); !ok {
		t.Fatal("usage unknown after race")
	}
}

func TestModelCacheBudgetClampsExplicitToDiskMinusFloor(t *testing.T) {
	a, _ := newCacheAgent(t)
	a.cfg.DiskFloor = "100"
	gguf := a.cfg.GGUFHostPath()
	capBytes, err := pull.CapacityBytes(gguf)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	a.log = slog.New(slog.NewTextHandler(&buf, nil))
	a.cfg.ModelCacheMax = strconv.FormatUint(uint64(capBytes)+1000, 10)
	got, err := a.modelCacheBudget(gguf)
	if err != nil {
		t.Fatal(err)
	}
	want := uint64(capBytes) - 100
	if got != want {
		t.Fatalf("budget %d want %d", got, want)
	}
	if !strings.Contains(buf.String(), "model cache budget clamped") {
		t.Fatalf("clamp not logged: %s", buf.String())
	}
	buf.Reset()
	a.cfg.ModelCacheMax = "50"
	got, err = a.modelCacheBudget(gguf)
	if err != nil || got != 50 {
		t.Fatalf("under ceiling %d %v", got, err)
	}
	if strings.Contains(buf.String(), "clamped") {
		t.Fatalf("small budget must not log a clamp: %s", buf.String())
	}
}

func TestPendingModelIsNotEvictedAndLeaseMountsIt(t *testing.T) {
	a, cache := newCacheAgent(t)
	a.cfg.ModelCacheMax = "200"
	a.bindIP = "10.77.0.2/32"
	bodyA := bytes.Repeat([]byte{'a'}, 100)
	bodyB := bytes.Repeat([]byte{'b'}, 100)
	bodyC := bytes.Repeat([]byte{'c'}, 100)
	pinA, pinB, pinC := shaPin(bodyA), shaPin(bodyB), shaPin(bodyC)
	bodies := map[string][]byte{pinA: bodyA, pinB: bodyB, pinC: bodyC}
	a.fetchWeights = func(_ context.Context, dest, _, pin string) error {
		return os.WriteFile(dest, bodies[pin], 0o600)
	}
	if res := pullCatalog(t, a, "cat-a", pinA, 100); !res.Passed {
		t.Fatalf("pull A: %+v", res.Result)
	}
	if res := pullCatalog(t, a, "cat-b", pinB, 100); !res.Passed {
		t.Fatalf("pull B: %+v", res.Result)
	}
	if _, err := os.Stat(filepath.Join(cache, fileName(pinA))); err != nil {
		t.Fatalf("A missing after B: %v", err)
	}
	if res := pullCatalog(t, a, "cat-c", pinC, 100); res.Passed {
		t.Fatal("C fit by evicting the pending model")
	}
	if _, err := os.Stat(filepath.Join(cache, fileName(pinA))); err != nil {
		t.Fatalf("pending A evicted: %v", err)
	}
	used, evictable, ok := a.modelCacheUsage()
	if !ok || used != 200 || evictable != 0 {
		t.Fatalf("usage used=%d evictable=%d ok=%v", used, evictable, ok)
	}
	var mounted string
	var runs int
	a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "run" {
			runs++
			for i, arg := range args {
				if arg == "-v" && i+1 < len(args) && strings.Contains(args[i+1], "/models/model.gguf:ro") {
					mounted = args[i+1]
				}
			}
		}
		return []byte("cid\n"), nil
	}}
	res := a.leaseStart(context.Background(), api.Job{
		JobID:       "lease-a",
		Kind:        api.KindLeaseStart,
		DeviceID:    testDevice,
		LeaseID:     "lease-a",
		CatalogID:   "cat-a",
		ImageDigest: "example.invalid/hypermesh/workload@sha256:" + stringsRepeat('a', 64),
		FileHash:    pinA,
		Runtime:     "nvidia",
	})
	if !res.Passed || res.ImageHash != pinA {
		t.Fatalf("lease_start A: %+v", res)
	}
	if runs != 1 {
		t.Fatalf("docker run calls %d", runs)
	}
	wantMount := filepath.Join(cache, fileName(pinA)) + ":/models/model.gguf:ro"
	if mounted != wantMount {
		t.Fatalf("mounted %q want %q", mounted, wantMount)
	}
	idx := readIndex(t, cache)
	for _, p := range idx.Pending {
		if p.CatalogID == "cat-a" {
			t.Fatalf("pending not cleared: %+v", idx.Pending)
		}
	}
}

func TestLeaseStartRefusesMismatchedPendingModel(t *testing.T) {
	a, cache := newCacheAgent(t)
	a.bindIP = "10.77.0.2/32"
	body := bytes.Repeat([]byte{'a'}, 80)
	pin := shaPin(body)
	a.fetchWeights = func(_ context.Context, dest, _, _ string) error {
		return os.WriteFile(dest, body, 0o600)
	}
	if res := pullCatalog(t, a, "cat-a", pin, int64(len(body))); !res.Passed {
		t.Fatalf("pull: %+v", res.Result)
	}
	if err := os.WriteFile(filepath.Join(cache, fileName(pin)), []byte("not-the-model"), 0o600); err != nil {
		t.Fatal(err)
	}
	var starts int
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		starts++
		return []byte("cid\n"), nil
	}}
	res := a.leaseStart(context.Background(), api.Job{
		JobID:       "lease-a",
		Kind:        api.KindLeaseStart,
		DeviceID:    testDevice,
		LeaseID:     "lease-a",
		CatalogID:   "cat-a",
		ImageDigest: "example.invalid/hypermesh/workload@sha256:" + stringsRepeat('b', 64),
		FileHash:    pin,
		Runtime:     "nvidia",
	})
	if res.Passed || res.Reason != "artifact hash mismatch" {
		t.Fatalf("lease_start: %+v", res)
	}
	if starts != 0 {
		t.Fatalf("container started on mismatch")
	}
}

func TestLeaseStartRefusesMissingPendingModel(t *testing.T) {
	a, cache := newCacheAgent(t)
	a.bindIP = "10.77.0.2/32"
	body := bytes.Repeat([]byte{'a'}, 80)
	pin := shaPin(body)
	a.fetchWeights = func(_ context.Context, dest, _, _ string) error {
		return os.WriteFile(dest, body, 0o600)
	}
	if res := pullCatalog(t, a, "cat-a", pin, int64(len(body))); !res.Passed {
		t.Fatalf("pull: %+v", res.Result)
	}
	if err := os.Remove(filepath.Join(cache, fileName(pin))); err != nil {
		t.Fatal(err)
	}
	var starts int
	a.runtime = &runtime.NVIDIA{Run: func(context.Context, string, ...string) ([]byte, error) {
		starts++
		return []byte("cid\n"), nil
	}}
	res := a.leaseStart(context.Background(), api.Job{
		JobID:       "lease-a",
		Kind:        api.KindLeaseStart,
		DeviceID:    testDevice,
		LeaseID:     "lease-a",
		CatalogID:   "cat-a",
		ImageDigest: "example.invalid/hypermesh/workload@sha256:" + stringsRepeat('c', 64),
		Runtime:     "nvidia",
	})
	if res.Passed || res.Reason != "pending model missing" {
		t.Fatalf("lease_start: %+v", res)
	}
	if starts != 0 {
		t.Fatal("container started without the pending model")
	}
	if pendingCatalog(readIndex(t, cache), "cat-a") {
		t.Fatal("missing file left the pin in place")
	}
	again := a.leaseStart(context.Background(), api.Job{
		JobID:       "lease-a-2",
		Kind:        api.KindLeaseStart,
		DeviceID:    testDevice,
		LeaseID:     "lease-a-2",
		CatalogID:   "cat-a",
		ImageDigest: "example.invalid/hypermesh/workload@sha256:" + stringsRepeat('c', 64),
		FileHash:    pin,
		Runtime:     "nvidia",
	})
	if again.Passed || again.Reason == "pending model missing" {
		t.Fatalf("cleared pin must not block the catalog: %+v", again)
	}
	if starts != 0 {
		t.Fatal("cleared pin started a container")
	}
	if res := pullCatalog(t, a, "cat-a", pin, int64(len(body))); !res.Passed {
		t.Fatalf("replacement pull: %+v", res.Result)
	}
	if !pendingCatalog(readIndex(t, cache), "cat-a") {
		t.Fatal("replacement pull did not record a pin")
	}
	started := a.leaseStart(context.Background(), api.Job{
		JobID:       "lease-a-3",
		Kind:        api.KindLeaseStart,
		DeviceID:    testDevice,
		LeaseID:     "lease-a-3",
		CatalogID:   "cat-a",
		ImageDigest: "example.invalid/hypermesh/workload@sha256:" + stringsRepeat('c', 64),
		FileHash:    pin,
		Runtime:     "nvidia",
	})
	if !started.Passed || started.ImageHash != pin || starts == 0 {
		t.Fatalf("start after replacement %+v runs=%d", started, starts)
	}
}

func TestPendingPinExpiresAndLeaseReleaseClearsIt(t *testing.T) {
	a, cache := newCacheAgent(t)
	a.cfg.ModelCacheMax = "100000"
	a.cfg.PendingTTL = "1m"
	now := time.Date(2026, 9, 26, 18, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	bodies := map[string][]byte{}
	a.fetchWeights = func(_ context.Context, dest, _, pin string) error {
		return os.WriteFile(dest, bodies[pin], 0o600)
	}
	pins := map[string]string{}
	for _, cat := range []string{"cat-a", "cat-b", "cat-c", "cat-d"} {
		body := bytes.Repeat([]byte{cat[len(cat)-1]}, 40)
		pin := shaPin(body)
		bodies[pin] = body
		pins[cat] = pin
		if res := pullCatalog(t, a, cat, pin, int64(len(body))); !res.Passed {
			t.Fatalf("pull %s: %+v", cat, res.Result)
		}
	}
	idx := readIndex(t, cache)
	for _, cat := range []string{"cat-a", "cat-b", "cat-c", "cat-d"} {
		p := findPending(idx, cat)
		if p == nil || p.ID != pins[cat] || !p.Expires.Equal(now.Add(time.Minute)) {
			t.Fatalf("pin %s: %+v", cat, p)
		}
	}
	stop := a.runJob(context.Background(), api.Job{
		JobID: "stop-a", Kind: api.KindLeaseStop, DeviceID: testDevice,
		LeaseID: "lease-none", CatalogID: "cat-a",
	})
	if !stop.Passed || stop.Reason != "lease_stop" {
		t.Fatalf("stop %+v", stop)
	}
	cancel := a.runJob(context.Background(), api.Job{
		JobID: "cancel-b", Kind: api.KindLeaseCancel, DeviceID: testDevice,
		LeaseID: "lease-none", CatalogID: "cat-b",
	})
	if !cancel.Passed || cancel.Reason != "lease_cancel" {
		t.Fatalf("cancel %+v", cancel)
	}
	expiry := a.runJob(context.Background(), api.Job{
		JobID: "expiry-c", Kind: api.KindLeaseExpiry, DeviceID: testDevice,
		LeaseID: "lease-none", CatalogID: "cat-c",
	})
	if !expiry.Passed || expiry.Reason != "lease_expiry" {
		t.Fatalf("expiry %+v", expiry)
	}
	idx = readIndex(t, cache)
	if pendingCatalog(idx, "cat-a") || pendingCatalog(idx, "cat-b") || pendingCatalog(idx, "cat-c") || !pendingCatalog(idx, "cat-d") {
		t.Fatalf("pins after release %+v", idx.Pending)
	}
	a.now = func() time.Time { return now.Add(2 * time.Minute) }
	used, evictable, ok := a.modelCacheUsage()
	if !ok || used != 160 || evictable != 120 {
		t.Fatalf("usage used=%d evictable=%d ok=%v", used, evictable, ok)
	}
	idx = readIndex(t, cache)
	if len(idx.Pending) != 0 {
		t.Fatalf("expired pin remains %+v", idx.Pending)
	}
	if findModel(idx, pins["cat-d"]) == nil {
		t.Fatal("expired pin deleted the file")
	}
}

func TestLeaseStartVerifiesPayloadHashWithoutPendingPin(t *testing.T) {
	a, cache := newCacheAgent(t)
	a.cfg.ModelCacheMax = "100000"
	a.bindIP = "10.77.0.2/32"
	bodyA := bytes.Repeat([]byte{'a'}, 50)
	bodyB := bytes.Repeat([]byte{'b'}, 50)
	pinA, pinB := shaPin(bodyA), shaPin(bodyB)
	bodies := map[string][]byte{pinA: bodyA, pinB: bodyB}
	a.fetchWeights = func(_ context.Context, dest, _, pin string) error {
		return os.WriteFile(dest, bodies[pin], 0o600)
	}
	if res := pullCatalog(t, a, "cat-a", pinA, 50); !res.Passed {
		t.Fatalf("pull A: %+v", res.Result)
	}
	if res := pullCatalog(t, a, "cat-b", pinB, 50); !res.Passed {
		t.Fatalf("pull B: %+v", res.Result)
	}
	cleared := a.runJob(context.Background(), api.Job{
		JobID: "stop-a", Kind: api.KindLeaseStop, DeviceID: testDevice,
		CatalogID: "cat-a",
	})
	if !cleared.Passed {
		t.Fatalf("stop %+v", cleared)
	}
	if pendingCatalog(readIndex(t, cache), "cat-a") {
		t.Fatal("cat-a pin still set")
	}
	var runs int
	a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "run" {
			runs++
		}
		return []byte("cid\n"), nil
	}}
	oci := "example.invalid/hypermesh/workload@sha256:" + stringsRepeat('d', 64)
	mismatch := a.leaseStart(context.Background(), api.Job{
		JobID: "lease-mismatch", Kind: api.KindLeaseStart, DeviceID: testDevice,
		LeaseID: "lease-mismatch", CatalogID: "cat-a", ImageDigest: oci,
		FileHash: pinA, Runtime: "nvidia",
	})
	if mismatch.Passed || mismatch.Reason != "artifact hash mismatch" || runs != 0 {
		t.Fatalf("mismatch %+v runs=%d", mismatch, runs)
	}
	missing := a.leaseStart(context.Background(), api.Job{
		JobID: "lease-missing", Kind: api.KindLeaseStart, DeviceID: testDevice,
		LeaseID: "lease-missing", CatalogID: "cat-a", ImageDigest: oci, Runtime: "nvidia",
	})
	if missing.Passed || missing.Reason != "missing expected hash" || runs != 0 {
		t.Fatalf("missing hash %+v runs=%d", missing, runs)
	}
	okStart := a.leaseStart(context.Background(), api.Job{
		JobID: "lease-b", Kind: api.KindLeaseStart, DeviceID: testDevice,
		LeaseID: "lease-b", CatalogID: "cat-a", ImageDigest: oci,
		FileHash: pinB, Runtime: "nvidia",
	})
	if !okStart.Passed || okStart.ImageHash != pinB || runs != 1 {
		t.Fatalf("matching payload hash %+v runs=%d", okStart, runs)
	}
}

func TestPendingModelTTLDefault(t *testing.T) {
	d, err := (Config{}).PendingModelTTL()
	if err != nil || d != 30*time.Minute {
		t.Fatalf("default %s %v", d, err)
	}
	if _, err := (Config{PendingTTL: "0"}).PendingModelTTL(); err == nil {
		t.Fatal("zero ttl accepted")
	}
	if _, err := (Config{PendingTTL: "nope"}).PendingModelTTL(); err == nil {
		t.Fatal("bad ttl accepted")
	}
}

func TestStartupRemovesCachePartials(t *testing.T) {
	a, cache := newCacheAgent(t)
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	partial := bytes.Repeat([]byte{'p'}, 40)
	if err := os.WriteFile(filepath.Join(cache, "lab-pull-deadbeef.gguf"), partial, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "index.json.tmp-123"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	keep := bytes.Repeat([]byte{'k'}, 10)
	id := seedCachedModel(t, cache, keep, time.Now().UTC())
	var buf bytes.Buffer
	a.log = slog.New(slog.NewTextHandler(&buf, nil))
	if err := a.reconcileModelCache(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cache, "lab-pull-deadbeef.gguf")); !os.IsNotExist(err) {
		t.Fatalf("partial remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, "index.json.tmp-123")); !os.IsNotExist(err) {
		t.Fatalf("index temp remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, fileName(id))); err != nil {
		t.Fatalf("real model removed: %v", err)
	}
	log := buf.String()
	if !strings.Contains(log, "model cache cleared partials") || !strings.Contains(log, "bytes=41") {
		t.Fatalf("log %s", log)
	}
}

func TestModelCacheBudgetDefaultsToCapacityMinusFloor(t *testing.T) {
	a, _ := newCacheAgent(t)
	a.cfg.ModelCacheMax = ""
	a.cfg.DiskFloor = "100"
	gguf := a.cfg.GGUFHostPath()
	capBytes, err := pull.CapacityBytes(gguf)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.modelCacheBudget(gguf)
	if err != nil {
		t.Fatal(err)
	}
	if got != uint64(capBytes)-100 {
		t.Fatalf("budget %d want %d", got, uint64(capBytes)-100)
	}
	a.cfg.ModelCacheMax = "0"
	got, err = a.modelCacheBudget(gguf)
	if err != nil || got != 0 {
		t.Fatalf("explicit zero %d %v", got, err)
	}
}

func TestHeartbeatReportsModelCacheBytes(t *testing.T) {
	dir := t.TempDir()
	gguf := filepath.Join(dir, "models", "model.gguf")
	if err := os.MkdirAll(filepath.Dir(gguf), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, hb, text := heartbeatHealth(t, func(a *Agent) {
		a.cfg.GGUFPath = gguf
		cache := filepath.Join(dir, "models", cacheDirName)
		id := seedCachedModel(t, cache, bytes.Repeat([]byte{'z'}, 30), time.Now().UTC())
		writeSeedIndex(t, cache, cacheIndex{
			Loaded: id,
			Models: []cacheModel{{ID: id, Bytes: 30, LastUsed: time.Now().UTC()}},
		})
	})
	if hb.Health == nil || hb.Health.ModelCacheBytes == nil || hb.Health.ModelCacheEvictableBytes == nil {
		t.Fatalf("cache fields missing: %s", raw)
	}
	if *hb.Health.ModelCacheBytes != 30 || *hb.Health.ModelCacheEvictableBytes != 0 {
		t.Fatalf("bytes %d evictable %d", *hb.Health.ModelCacheBytes, *hb.Health.ModelCacheEvictableBytes)
	}
	if !bytes.Contains(raw, []byte(`"model_cache_bytes":30`)) || !bytes.Contains(raw, []byte(`"model_cache_evictable_bytes":0`)) {
		t.Fatalf("json %s", raw)
	}
	if !bytes.Contains([]byte(text), []byte("hypermesh_model_cache_bytes 30\n")) {
		t.Fatalf("metrics\n%s", text)
	}
	if !bytes.Contains([]byte(text), []byte("hypermesh_model_cache_evictable_bytes 0\n")) {
		t.Fatalf("metrics\n%s", text)
	}
}

func newCacheAgent(t *testing.T) (*Agent, string) {
	t.Helper()
	dir := t.TempDir()
	uptime := filepath.Join(dir, "uptime")
	if err := os.WriteFile(uptime, []byte("10.0"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, "http://127.0.0.1:9", dir, uptime, "", "")
	gguf := filepath.Join(dir, "models", "model.gguf")
	a.cfg.GGUFPath = gguf
	a.cfg.ModelCacheMax = "300"
	a.cfg.DiskFloor = "0"
	a.cfg.DiskMargin = "0"
	a.statFree = func(string) (int64, error) { return 1 << 40, nil }
	return a, filepath.Join(dir, "models", cacheDirName)
}

func pullModel(t *testing.T, a *Agent, pin string, size int64) jobOutcome {
	t.Helper()
	return pullCatalog(t, a, "cache-model", pin, size)
}

func pullCatalog(t *testing.T, a *Agent, catalog, pin string, size int64) jobOutcome {
	t.Helper()
	return a.runJob(context.Background(), api.Job{
		JobID:       "lab-pull-" + catalog,
		Kind:        api.KindLabPull,
		DeviceID:    testDevice,
		CatalogID:   catalog,
		ImageDigest: pin,
		SourceRef:   "bartowski/Llama-3.1-8B-Instruct-GGUF@main/model.gguf",
		SizeBytes:   size,
	})
}

func seedCachedModel(t *testing.T, dir string, body []byte, last time.Time) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := shaPin(body)
	path := filepath.Join(dir, fileName(id))
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, last, last); err != nil {
		t.Fatal(err)
	}
	return id
}

func writeSeedIndex(t *testing.T, dir string, idx cacheIndex) {
	t.Helper()
	if err := writeCacheIndex(dir, idx); err != nil {
		t.Fatal(err)
	}
}

func readIndex(t *testing.T, dir string) cacheIndex {
	t.Helper()
	var idx cacheIndex
	b := mustRead(t, filepath.Join(dir, cacheIndexName))
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	return idx
}

func findPending(idx cacheIndex, catalog string) *cachePending {
	for i := range idx.Pending {
		if idx.Pending[i].CatalogID == catalog {
			return &idx.Pending[i]
		}
	}
	return nil
}

func pendingCatalog(idx cacheIndex, catalog string) bool {
	return findPending(idx, catalog) != nil
}

func findModel(idx cacheIndex, id string) *cacheModel {
	for i := range idx.Models {
		if idx.Models[i].ID == id {
			return &idx.Models[i]
		}
	}
	return nil
}

func fileName(id string) string {
	return id[len("sha256:"):] + ".gguf"
}

func shaPin(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func stringsRepeat(b byte, n int) string {
	return string(bytes.Repeat([]byte{b}, n))
}
