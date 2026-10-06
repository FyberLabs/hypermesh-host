package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/pull"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

// cacheDirName is the model store under the GGUF data directory.
// The data directory is the parent of HYPERMESH_GGUF_PATH
// (/var/lib/hypermesh/models by default), so the cache is
// /var/lib/hypermesh/models/cache.
const cacheDirName = "cache"

const cacheIndexName = "index.json"

// cacheIndex is the durable last-used record. An atime update is not
// used: Jetson and other Linux roots often mount with relatime or
// noatime, so atime does not move on read and cannot order an LRU.
// The index is rewritten with os.CreateTemp in this directory, fsync,
// and rename. A crash leaves the previous index.json and a temp file.
// Startup ignores temps and rebuilds entries from the gguf files that
// are actually present.
type cacheIndex struct {
	Loaded  string         `json:"loaded,omitempty"`
	Models  []cacheModel   `json:"models"`
	Pending []cachePending `json:"pending,omitempty"`
}

// cachePending is a model a later lease_start for this catalog must mount.
// Expires is written with the index. After that instant the pin is dropped
// on the next reconcile, so a pull that never starts does not stay pinned.
// lease_stop, lease_cancel, lease_expiry, and a successful lease_start clear
// it sooner. The plane does not send lease_cancel or lease_expiry yet, so
// a cancelled or expired lease drops its pin only when this expiry passes.
// A newer lab_pull for the same catalog replaces it.
type cachePending struct {
	CatalogID string    `json:"catalog_id"`
	ID        string    `json:"id"`
	Expires   time.Time `json:"expires"`
}

// cacheModel is one GGUF in the cache. CatalogIDs is the catalogs
// lab_pull admitted this file for. It is stored in index.json so a
// restart still knows which catalog the file belongs to. A later pull
// for one of those catalogs moves that id onto the new file. Eviction
// drops the whole entry, catalogs included. LastUsed is the LRU key.
type cacheModel struct {
	ID         string    `json:"id"`
	Bytes      int64     `json:"bytes"`
	LastUsed   time.Time `json:"last_used"`
	CatalogIDs []string  `json:"catalog_ids,omitempty"`
}

type evictionPlan struct {
	Fits      bool
	Victims   []string
	Freed     int64
	Evictable uint64
	Need      uint64
}

// modelCacheDir is the cache directory for a GGUF path. The path is the
// weights file; the cache is a sibling directory of that file named cache.
func modelCacheDir(ggufPath string) (string, error) {
	if err := downloadDestOK(ggufPath); err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(filepath.Clean(ggufPath)), cacheDirName), nil
}

func cacheFilePath(dir, pin string) (string, error) {
	name, err := cacheFileName(pin)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func cacheFileName(pin string) (string, error) {
	id, err := normalizeModelID(pin)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(id, "sha256:") + ".gguf", nil
}

func normalizeModelID(pin string) (string, error) {
	id := strings.ToLower(strings.TrimSpace(pin))
	hexPart := strings.TrimPrefix(id, "sha256:")
	if !strings.HasPrefix(id, "sha256:") || len(hexPart) != 64 || !hexOnly(hexPart) {
		return "", fmt.Errorf("hypermesh: model id must be sha256 and 64 hex chars")
	}
	return "sha256:" + hexPart, nil
}

func hexOnly(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

func (a *Agent) modelCacheBudget(path string) (uint64, error) {
	capBytes, capErr := pull.CapacityBytes(path)
	floor, err := a.cfg.DiskFloorBytes()
	if err != nil {
		return 0, err
	}
	var bound uint64
	if capErr == nil && capBytes >= 0 && uint64(capBytes) >= floor {
		bound = uint64(capBytes) - floor
	}
	raw := strings.TrimSpace(a.cfg.ModelCacheMax)
	policy, err := a.cachePolicy()
	if err != nil {
		return 0, err
	}
	if policy != nil {
		raw = policyBudget(policy)
		if capBytes >= 0 && uint64(capBytes) >= policy.ReserveBytes {
			bound = uint64(capBytes) - policy.ReserveBytes
		} else {
			bound = 0
		}
	}
	if raw == "" {
		if capErr != nil {
			return 0, capErr
		}
		return bound, nil
	}
	explicit, err := parseByteCount("HYPERMESH_MODEL_CACHE_MAX_BYTES", raw, 0)
	if err != nil {
		return 0, err
	}
	if capErr != nil {
		return 0, capErr
	}
	if explicit > bound {
		if a.log != nil {
			a.log.Info("model cache budget clamped", "requested", explicit, "budget", bound)
		}
		return bound, nil
	}
	return explicit, nil
}

func (a *Agent) reconcileModelCache() error {
	gguf := a.cfg.GGUFHostPath()
	if gguf == "" {
		return nil
	}
	dir, err := modelCacheDir(gguf)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Dir(dir)); os.IsNotExist(err) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	unlock, err := LockModelCache(ctx, filepath.Dir(dir))
	if err != nil {
		return err
	}
	defer unlock()
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	if n, freed := removeCachePartials(dir); n > 0 && a.log != nil {
		a.log.Info("model cache cleared partials", "files", n, "bytes", freed)
	}
	_, err = a.reconcileCacheDirLocked(dir)
	return err
}

// removeCachePartials deletes crash leftovers in the cache directory.
// It runs only from startup reconcile, before the job loop, so a live
// download's lab-pull-*.gguf is not removed. Bytes freed are the file sizes.
func removeCachePartials(dir string) (files int, freed int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		partial := (strings.HasPrefix(name, "lab-pull-") && strings.HasSuffix(name, ".gguf")) || strings.HasPrefix(name, ".certification-model-")
		temp := strings.HasPrefix(name, cacheIndexName+".tmp-")
		if !partial && !temp {
			continue
		}
		info, err := ent.Info()
		if err != nil {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			continue
		}
		files++
		if info.Size() > 0 {
			freed += info.Size()
		}
	}
	return files, freed
}

func (a *Agent) reconcileCacheDirLocked(dir string) (cacheIndex, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return cacheIndex{}, nil
	} else if err != nil {
		return cacheIndex{}, err
	}
	idx, corrupt, err := readCacheIndex(dir)
	if err != nil {
		return cacheIndex{}, err
	}
	next, changed, err := reconcileIndex(dir, idx, a.clock())
	if err != nil {
		return cacheIndex{}, err
	}
	if corrupt {
		changed = true
	}
	if changed {
		if err := writeCacheIndex(dir, next); err != nil {
			return cacheIndex{}, err
		}
	}
	return next, nil
}

func readCacheIndex(dir string) (cacheIndex, bool, error) {
	b, err := os.ReadFile(filepath.Join(dir, cacheIndexName))
	if os.IsNotExist(err) {
		return cacheIndex{}, false, nil
	}
	if err != nil {
		return cacheIndex{}, false, err
	}
	var idx cacheIndex
	if err := json.Unmarshal(b, &idx); err != nil {
		return cacheIndex{}, true, nil
	}
	return idx, false, nil
}

func reconcileIndex(dir string, idx cacheIndex, now time.Time) (cacheIndex, bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return cacheIndex{}, false, err
	}
	onDisk := map[string]os.FileInfo{}
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".gguf") {
			continue
		}
		if strings.HasPrefix(name, "lab-pull-") || strings.Contains(name, ".tmp-") {
			continue
		}
		id, err := normalizeModelID("sha256:" + strings.TrimSuffix(name, ".gguf"))
		if err != nil {
			continue
		}
		info, err := ent.Info()
		if err != nil {
			return cacheIndex{}, false, err
		}
		onDisk[id] = info
	}
	changed := false
	var kept []cacheModel
	seen := map[string]struct{}{}
	for _, m := range idx.Models {
		id, err := normalizeModelID(m.ID)
		if err != nil {
			changed = true
			continue
		}
		info, ok := onDisk[id]
		if !ok {
			changed = true
			continue
		}
		if _, dup := seen[id]; dup {
			changed = true
			continue
		}
		seen[id] = struct{}{}
		cats, catsChanged := normalizeCatalogs(m.CatalogIDs)
		if catsChanged || m.Bytes != info.Size() || m.ID != id {
			changed = true
		}
		m.ID = id
		m.Bytes = info.Size()
		m.CatalogIDs = cats
		kept = append(kept, m)
	}
	for id, info := range onDisk {
		if _, ok := seen[id]; ok {
			continue
		}
		changed = true
		kept = append(kept, cacheModel{
			ID:       id,
			Bytes:    info.Size(),
			LastUsed: info.ModTime().UTC(),
		})
	}
	loaded, err := normalizeOptionalID(idx.Loaded)
	if err != nil {
		loaded = ""
		changed = true
	}
	if loaded != "" {
		found := false
		for _, m := range kept {
			if m.ID == loaded {
				found = true
				break
			}
		}
		if !found {
			loaded = ""
			changed = true
		}
	}
	if idx.Loaded != loaded {
		changed = true
	}
	pending, pendChanged := reconcilePending(idx.Pending, now)
	if pendChanged {
		changed = true
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].ID < kept[j].ID })
	return cacheIndex{Loaded: loaded, Models: kept, Pending: pending}, changed, nil
}

func reconcilePending(in []cachePending, now time.Time) ([]cachePending, bool) {
	changed := false
	now = now.UTC()
	var out []cachePending
	seen := map[string]struct{}{}
	for _, p := range in {
		cat := strings.TrimSpace(p.CatalogID)
		id, err := normalizeModelID(p.ID)
		if err != nil || cat == "" {
			changed = true
			continue
		}
		// A zero or past expiry is not a pin. A missing file stays
		// recorded until lease_start clears it or this expiry passes,
		// so a vanished file cannot be replaced by a different model
		// on that one start.
		if p.Expires.IsZero() || !p.Expires.After(now) {
			changed = true
			continue
		}
		if _, dup := seen[cat]; dup {
			changed = true
			continue
		}
		seen[cat] = struct{}{}
		exp := p.Expires.UTC()
		if p.CatalogID != cat || p.ID != id || !p.Expires.Equal(exp) {
			changed = true
		}
		out = append(out, cachePending{CatalogID: cat, ID: id, Expires: exp})
	}
	return out, changed
}

func normalizeOptionalID(pin string) (string, error) {
	if strings.TrimSpace(pin) == "" {
		return "", nil
	}
	return normalizeModelID(pin)
}

// writeCacheIndex replaces index.json atomically. The temp file is in
// the same directory. fsync on the file and the directory makes a crash
// leave either the old index or the new one, never a half-written index.
// A leftover index.json.tmp-* is not read.
func writeCacheIndex(dir string, idx cacheIndex) error {
	if idx.Models == nil {
		idx.Models = []cacheModel{}
	}
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(dir, cacheIndexName+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(dir, cacheIndexName)); err != nil {
		return err
	}
	cleanup = false
	df, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer df.Close()
	return df.Sync()
}

func (a *Agent) planModelEviction(dest, pin string, lim pull.Limits) (pull.Limits, error) {
	free, err := lim.Stat(dest)
	if err != nil || free < 0 {
		return lim, &pull.DiskError{StatErr: true}
	}
	budget, err := a.modelCacheBudget(dest)
	if err != nil {
		return lim, &pull.DiskError{StatErr: true}
	}
	leased := ""
	if h := a.localLease(); h != nil {
		leased = strings.TrimSpace(h.FileHash)
	}
	dir, err := modelCacheDir(a.cfg.GGUFHostPath())
	if err != nil {
		return lim, err
	}
	id, err := normalizeModelID(pin)
	if err != nil {
		return lim, err
	}
	a.cacheMu.Lock()
	idx, recErr := a.reconcileCacheDirLocked(dir)
	a.cacheMu.Unlock()
	if recErr != nil {
		return lim, &pull.DiskError{StatErr: true}
	}
	idx, recErr = a.protectOwnerPins(idx)
	if recErr != nil {
		return lim, recErr
	}
	plan := planEviction(idx.Models, "", leased, pendingIDs(idx), id, lim.Expected, budget, free, limFloor(lim), limMargin(lim))
	lim.Evictable = plan.Evictable
	lim.WithEvictable = true
	lim.Reclaim = 0
	if !plan.Fits {
		if used, evictable, ok := a.modelCacheUsage(); ok && lim.Expected > 0 && uint64(used-evictable)+uint64(lim.Expected) > budget {
			return lim, fmt.Errorf("model_cache_quota_exceeded: model %d bytes, quota %d, protected %d; increase the host quota or remove an owner pin after its workload ends", lim.Expected, budget, used-evictable)
		}
		return lim, &pull.DiskError{
			Need:          plan.Need,
			Free:          free,
			Evictable:     plan.Evictable,
			WithEvictable: true,
		}
	}
	if plan.Freed > 0 {
		lim.Reclaim = uint64(plan.Freed)
	}
	victims := append([]string(nil), plan.Victims...)
	if len(victims) > 0 {
		lim.BeforeWrite = func() error {
			return a.applyEvict(dir, victims)
		}
	}
	return lim, nil
}

func limFloor(lim pull.Limits) uint64 {
	if lim.FloorSet || lim.Floor > 0 {
		return lim.Floor
	}
	return pull.DefaultFloorBytes
}

func limMargin(lim pull.Limits) uint64 {
	if lim.MarginSet || lim.Margin > 0 {
		return lim.Margin
	}
	return pull.DefaultMarginBytes
}

func planEviction(models []cacheModel, loaded, leased string, extra []string, incoming string, incomingSize int64, budget uint64, free int64, floor, margin uint64) evictionPlan {
	loaded, _ = normalizeOptionalID(loaded)
	leased, _ = normalizeOptionalID(leased)
	incoming, _ = normalizeOptionalID(incoming)
	need, needOK := pull.NeedBytes(incomingSize, floor, margin)
	pinned := map[string]struct{}{}
	if loaded != "" {
		pinned[loaded] = struct{}{}
	}
	if leased != "" {
		pinned[leased] = struct{}{}
	}
	for _, id := range extra {
		if id != "" {
			pinned[id] = struct{}{}
		}
	}
	var evictable int64
	for _, m := range models {
		if _, ok := pinned[m.ID]; ok {
			continue
		}
		if m.Bytes > 0 {
			evictable += m.Bytes
		}
	}
	if evictable < 0 {
		evictable = 0
	}
	plan := evictionPlan{Evictable: uint64(evictable), Need: need}
	if !needOK || incomingSize <= 0 || free < 0 {
		return plan
	}
	type cand struct {
		id       string
		bytes    int64
		lastUsed time.Time
	}
	var cands []cand
	for _, m := range models {
		if _, ok := pinned[m.ID]; ok {
			continue
		}
		cands = append(cands, cand{id: m.ID, bytes: m.Bytes, lastUsed: m.LastUsed})
	}
	sort.Slice(cands, func(i, j int) bool {
		if !cands[i].lastUsed.Equal(cands[j].lastUsed) {
			return cands[i].lastUsed.Before(cands[j].lastUsed)
		}
		return cands[i].id < cands[j].id
	})
	evicted := map[string]struct{}{}
	var freed int64
	fits := func() bool {
		var cache int64
		var oldIncoming int64
		for _, m := range models {
			if m.ID == incoming {
				oldIncoming = m.Bytes
			}
			if _, gone := evicted[m.ID]; gone {
				continue
			}
			if m.Bytes < 0 {
				continue
			}
			if cache > (1<<63-1)-m.Bytes {
				return false
			}
			cache += m.Bytes
		}
		final := cache
		if _, gone := evicted[incoming]; !gone {
			if oldIncoming > final {
				final = 0
			} else {
				final -= oldIncoming
			}
		}
		if incomingSize < 0 || final > (1<<63-1)-incomingSize {
			return false
		}
		final += incomingSize
		if final < 0 || uint64(final) > budget {
			return false
		}
		if freed < 0 || free > (1<<63-1)-freed {
			return false
		}
		disk := free + freed
		if disk < 0 {
			return false
		}
		return uint64(disk) >= need
	}
	if fits() {
		plan.Fits = true
		return plan
	}
	for _, c := range cands {
		evicted[c.id] = struct{}{}
		if c.bytes > 0 {
			if freed > (1<<63-1)-c.bytes {
				plan.Fits = false
				plan.Victims = nil
				plan.Freed = 0
				return plan
			}
			freed += c.bytes
		}
		plan.Victims = append(plan.Victims, c.id)
		plan.Freed = freed
		if fits() {
			plan.Fits = true
			return plan
		}
	}
	plan.Fits = false
	plan.Victims = nil
	plan.Freed = 0
	return plan
}

func (a *Agent) applyEvict(dir string, victims []string) error {
	if len(victims) == 0 {
		return nil
	}
	leased := ""
	if h := a.localLease(); h != nil {
		leased, _ = normalizeOptionalID(h.FileHash)
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	idx, err := a.reconcileCacheDirLocked(dir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	protected, err := a.protectOwnerPins(idx)
	if err != nil {
		return err
	}
	drop := map[string]struct{}{}
	for _, id := range victims {
		norm, err := normalizeModelID(id)
		if err != nil {
			continue
		}
		if norm == leased || pendingPinned(protected, norm) {
			continue
		}
		drop[norm] = struct{}{}
	}
	var kept []cacheModel
	for _, m := range idx.Models {
		if _, ok := drop[m.ID]; !ok {
			kept = append(kept, m)
			continue
		}
		path, err := cacheFilePath(dir, m.ID)
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		a.log.Info("model cache evict", "id", m.ID, "bytes", m.Bytes)
	}
	idx.Models = kept
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeCacheIndex(dir, idx)
}

func (a *Agent) admitModel(catalog, pin, path string) error {
	id, err := normalizeModelID(pin)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	dir, err := modelCacheDir(a.cfg.GGUFHostPath())
	if err != nil {
		return err
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	idx, err := a.reconcileCacheDirLocked(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		if _, statErr := os.Stat(dir); statErr != nil {
			return statErr
		}
	}
	now := a.clock().UTC()
	cat := strings.TrimSpace(catalog)
	found := false
	for i := range idx.Models {
		if idx.Models[i].ID == id {
			idx.Models[i].Bytes = info.Size()
			idx.Models[i].LastUsed = now
			if cat != "" {
				idx.Models[i].CatalogIDs = addCatalog(idx.Models[i].CatalogIDs, cat)
			}
			found = true
			continue
		}
		if cat != "" {
			idx.Models[i].CatalogIDs = removeCatalog(idx.Models[i].CatalogIDs, cat)
		}
	}
	if !found {
		var cats []string
		if cat != "" {
			cats = []string{cat}
		}
		idx.Models = append(idx.Models, cacheModel{ID: id, Bytes: info.Size(), LastUsed: now, CatalogIDs: cats})
	}
	idx.Loaded = id
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeCacheIndex(dir, idx)
}

func (a *Agent) notePendingModel(catalog, pin string) error {
	catalog = strings.TrimSpace(catalog)
	id, err := normalizeModelID(pin)
	if err != nil || catalog == "" {
		return err
	}
	ttl, err := a.cfg.PendingModelTTL()
	if err != nil {
		return err
	}
	expires := a.clock().UTC().Add(ttl)
	dir, err := modelCacheDir(a.cfg.GGUFHostPath())
	if err != nil {
		return err
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	idx, err := a.reconcileCacheDirLocked(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		if _, statErr := os.Stat(dir); statErr != nil {
			return statErr
		}
	}
	var next []cachePending
	replaced := false
	for _, p := range idx.Pending {
		if p.CatalogID == catalog {
			next = append(next, cachePending{CatalogID: catalog, ID: id, Expires: expires})
			replaced = true
			continue
		}
		next = append(next, p)
	}
	if !replaced {
		next = append(next, cachePending{CatalogID: catalog, ID: id, Expires: expires})
	}
	idx.Pending = next
	return writeCacheIndex(dir, idx)
}

// clearPendingForLease drops the pin for the job catalog and, when the
// stopped handle names a different catalog, that one too.
func (a *Agent) clearPendingForLease(job api.Job, h *runtime.Handle) {
	cat := strings.TrimSpace(job.CatalogID)
	if cat != "" {
		a.clearPendingModel(cat)
	}
	if h == nil {
		return
	}
	handleCat := strings.TrimSpace(h.CatalogID)
	if handleCat != "" && handleCat != cat {
		a.clearPendingModel(handleCat)
	}
}

func (a *Agent) clearPendingModel(catalog string) {
	catalog = strings.TrimSpace(catalog)
	if catalog == "" {
		return
	}
	dir, err := modelCacheDir(a.cfg.GGUFHostPath())
	if err != nil {
		return
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	idx, err := a.reconcileCacheDirLocked(dir)
	if err != nil {
		return
	}
	var next []cachePending
	for _, p := range idx.Pending {
		if p.CatalogID == catalog {
			continue
		}
		next = append(next, p)
	}
	if len(next) == len(idx.Pending) {
		return
	}
	idx.Pending = next
	if err := writeCacheIndex(dir, idx); err != nil && a.log != nil {
		a.log.Warn("model cache pending not cleared", "err", err)
	}
}

// pendingModelFile is the cache file a lease_start for catalog must mount.
// found is false when this catalog has no pending lab_pull. When found is
// true and err is set, the recorded file is missing and the caller must
// refuse rather than mount a different model.
func (a *Agent) pendingModelFile(catalog string) (path, pin string, found bool, err error) {
	catalog = strings.TrimSpace(catalog)
	if catalog == "" {
		return "", "", false, nil
	}
	dir, dirErr := modelCacheDir(a.cfg.GGUFHostPath())
	if dirErr != nil {
		return "", "", false, dirErr
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	idx, recErr := a.reconcileCacheDirLocked(dir)
	if recErr != nil {
		return "", "", false, recErr
	}
	for _, p := range idx.Pending {
		if p.CatalogID != catalog {
			continue
		}
		file, pathErr := cacheFilePath(dir, p.ID)
		if pathErr != nil {
			return "", p.ID, true, pathErr
		}
		info, statErr := os.Stat(file)
		if statErr != nil || info.IsDir() {
			if statErr == nil {
				statErr = fmt.Errorf("hypermesh: pending model is not a file")
			}
			return "", p.ID, true, statErr
		}
		return file, p.ID, true, nil
	}
	return "", "", false, nil
}

func pendingIDs(idx cacheIndex) []string {
	var ids []string
	for _, p := range idx.Pending {
		id, err := normalizeModelID(p.ID)
		if err != nil || id == "" {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

func pendingPinned(idx cacheIndex, id string) bool {
	for _, p := range idx.Pending {
		pin, err := normalizeModelID(p.ID)
		if err == nil && pin == id {
			return true
		}
	}
	return false
}

func (a *Agent) touchModelUse(pin string) {
	id, err := normalizeModelID(pin)
	if err != nil {
		return
	}
	dir, err := modelCacheDir(a.cfg.GGUFHostPath())
	if err != nil {
		return
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	idx, err := a.reconcileCacheDirLocked(dir)
	if err != nil {
		return
	}
	now := a.clock().UTC()
	found := false
	for i := range idx.Models {
		if idx.Models[i].ID != id {
			continue
		}
		idx.Models[i].LastUsed = now
		found = true
		break
	}
	if !found {
		return
	}
	if err := writeCacheIndex(dir, idx); err != nil {
		a.log.Warn("model cache last-used not saved", "err", err)
	}
}

func normalizeCatalogs(in []string) ([]string, bool) {
	seen := map[string]struct{}{}
	var out []string
	for _, raw := range in {
		c := strings.TrimSpace(raw)
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	sort.Strings(out)
	if len(out) == 0 {
		out = nil
	}
	return out, !sameStrings(in, out)
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func addCatalog(ids []string, catalog string) []string {
	catalog = strings.TrimSpace(catalog)
	if catalog == "" || modelHasCatalog(cacheModel{CatalogIDs: ids}, catalog) {
		out, _ := normalizeCatalogs(ids)
		return out
	}
	out, _ := normalizeCatalogs(append(append([]string{}, ids...), catalog))
	return out
}

func removeCatalog(ids []string, catalog string) []string {
	catalog = strings.TrimSpace(catalog)
	var kept []string
	for _, id := range ids {
		if strings.TrimSpace(id) == catalog {
			continue
		}
		kept = append(kept, id)
	}
	out, _ := normalizeCatalogs(kept)
	return out
}

func modelHasCatalog(m cacheModel, catalog string) bool {
	catalog = strings.TrimSpace(catalog)
	if catalog == "" {
		return false
	}
	for _, id := range m.CatalogIDs {
		if id == catalog {
			return true
		}
	}
	return false
}

// cachedModelForCatalog is the loaded cache file when lab_pull admitted
// it for catalog. empty is true when the cache has no models, so
// lease_start may fall back to HYPERMESH_GGUF_PATH. found is false when
// the cache holds models but the loaded file is not recorded for catalog,
// including when a later pull replaced the loaded model.
func (a *Agent) cachedModelForCatalog(catalog string) (path string, found, empty bool) {
	dir, err := modelCacheDir(a.cfg.GGUFHostPath())
	if err != nil {
		// No cache directory can be named. The caller falls back to
		// HYPERMESH_GGUF_PATH, which fails closed when that path is unset.
		return "", false, true
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	idx, err := a.reconcileCacheDirLocked(dir)
	if err != nil {
		return "", false, false
	}
	if len(idx.Models) == 0 {
		return "", false, true
	}
	loaded, _ := normalizeOptionalID(idx.Loaded)
	if loaded == "" {
		return "", false, false
	}
	var match *cacheModel
	for i := range idx.Models {
		if idx.Models[i].ID != loaded || !modelHasCatalog(idx.Models[i], catalog) {
			continue
		}
		match = &idx.Models[i]
		break
	}
	if match == nil {
		return "", false, false
	}
	file, err := cacheFilePath(dir, match.ID)
	if err != nil {
		return "", false, false
	}
	info, err := os.Stat(file)
	if err != nil || info.IsDir() {
		return "", false, false
	}
	return file, true, false
}

func (a *Agent) loadedModelFile() (string, bool) {
	gguf := a.cfg.GGUFHostPath()
	dir, err := modelCacheDir(gguf)
	if err != nil {
		return "", false
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	idx, err := a.reconcileCacheDirLocked(dir)
	if err != nil || idx.Loaded == "" {
		return "", false
	}
	path, err := cacheFilePath(dir, idx.Loaded)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	return path, true
}

func (a *Agent) modelForLease() string {
	if path, ok := a.loadedModelFile(); ok {
		return path
	}
	return a.cfg.GGUFHostPath()
}

// modelCacheUsage reports cached bytes and bytes outside active leases,
// pending reservations, and owner pins. Loaded is a selection hint, not a pin. ok is false when the cache
// directory cannot be read. A missing directory is an empty cache.
func (a *Agent) modelCacheUsage() (used, evictable int64, ok bool) {
	gguf := a.cfg.GGUFHostPath()
	if strings.TrimSpace(gguf) == "" {
		return 0, 0, false
	}
	dir, err := modelCacheDir(gguf)
	if err != nil {
		return 0, 0, false
	}
	leased := ""
	if h := a.localLease(); h != nil {
		leased, _ = normalizeOptionalID(h.FileHash)
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return 0, 0, true
	}
	idx, err := a.reconcileCacheDirLocked(dir)
	if err != nil {
		return 0, 0, false
	}
	idx, err = a.protectOwnerPins(idx)
	if err != nil {
		return 0, 0, false
	}
	for _, m := range idx.Models {
		if m.Bytes < 0 {
			continue
		}
		if used > (1<<63-1)-m.Bytes {
			return 0, 0, false
		}
		used += m.Bytes
		if m.ID == leased || pendingPinned(idx, m.ID) {
			continue
		}
		evictable += m.Bytes
	}
	return used, evictable, true
}
