package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/pull"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

// CachePolicy is host-owned storage policy, separate from model admission.
// Pins are content hashes, never catalog aliases or mutable download locations.
type CachePolicy struct {
	Schema       int      `json:"schema"`
	MaxBytes     uint64   `json:"max_bytes"`
	ReserveBytes uint64   `json:"reserve_bytes"`
	Pins         []string `json:"pinned_hashes,omitempty"`
	TokenFile    string   `json:"huggingface_token_file,omitempty"`
}

const cachePolicyName = ".hypermesh-cache.json"

func ReadCachePolicy(directory string) (*CachePolicy, error) {
	path := filepath.Join(directory, cachePolicyName)
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read host cache policy")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 16384 || st.Mode().Perm()&0022 != 0 {
		return nil, fmt.Errorf("unsafe host cache policy")
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("host cache policy owner differs")
	}
	var p CachePolicy
	if json.NewDecoder(f).Decode(&p) != nil {
		return nil, fmt.Errorf("invalid host cache policy")
	}
	if err = p.validate(); err != nil {
		return nil, err
	}
	return &p, nil
}
func (p CachePolicy) validate() error {
	if p.TokenFile != "" && !filepath.IsAbs(p.TokenFile) {
		return fmt.Errorf("model token file must be absolute")
	}
	if p.Schema != 1 || p.MaxBytes > 1<<63-1 || p.ReserveBytes > 1<<63-1 || len(p.Pins) > 128 {
		return fmt.Errorf("invalid host cache policy")
	}
	for _, pin := range p.Pins {
		if id, err := normalizeModelID(pin); err != nil || id != pin {
			return fmt.Errorf("cache pins require canonical SHA-256 identities")
		}
	}
	return nil
}
func WriteCachePolicy(directory string, p CachePolicy) error {
	if err := p.validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	unlock, err := LockModelCache(ctx, directory)
	if err != nil {
		return err
	}
	defer unlock()
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(directory, ".cache-policy-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(directory, cachePolicyName)); err != nil {
		return err
	}
	d, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// LockModelCache serializes transfers across installer and agent processes.
func LockModelCache(ctx context.Context, directory string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(directory, ".hypermesh-cache.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("unsafe cache lock")
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
func (a *Agent) cachePolicy() (*CachePolicy, error) {
	return ReadCachePolicy(filepath.Dir(a.cfg.GGUFHostPath()))
}
func (a *Agent) protectOwnerPins(idx cacheIndex) (cacheIndex, error) {
	p, err := a.cachePolicy()
	if err != nil {
		return idx, err
	}
	if p != nil {
		for _, id := range p.Pins {
			idx.Pending = append(idx.Pending, cachePending{ID: id})
		}
	}
	return idx, nil
}

// AcquireCachedModel lets approved installer runs share the agent's LRU, quota,
// reserve and content identity. The callback retains authenticated transfer policy.
func AcquireCachedModel(ctx context.Context, path, pin string, size int64, fetch func() error) error {
	directory := filepath.Dir(filepath.Dir(path))
	expected, err := cacheFilePath(filepath.Join(directory, cacheDirName), pin)
	if err != nil || expected != path || size <= 0 {
		return fmt.Errorf("invalid cache artifact")
	}
	unlock, err := LockModelCache(ctx, directory)
	if err != nil {
		return err
	}
	defer unlock()
	a := &Agent{cfg: Config{GGUFPath: filepath.Join(directory, "model.gguf")}, log: slog.Default()}
	if _, err = a.cachePolicy(); err != nil {
		return err
	}
	if st, e := os.Lstat(path); e == nil {
		if !st.Mode().IsRegular() || st.Size() != size {
			return fmt.Errorf("cached model size or type differs")
		}
		if err = fetch(); err != nil {
			return err
		} // Existing file is verified by the installer.
	} else {
		if !os.IsNotExist(e) {
			return e
		}
		limits, err := a.policyLimits(a.pullLimits(size))
		if err != nil {
			return err
		}
		lim, err := a.planModelEviction(path, pin, limits)
		if err != nil {
			return err
		}
		if lim.BeforeWrite != nil {
			if err = lim.BeforeWrite(); err != nil {
				return err
			}
		}
		lim.Reclaim = 0
		if err = pull.Preflight(path, size, lim); err != nil {
			return err
		}
		if err = fetch(); err != nil {
			return err
		}
	}
	return a.admitModel("", pin, path)
}
func (a *Agent) verifyCachedArtifact(path, pin string, size int64) (bool, error) {
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !st.Mode().IsRegular() || size <= 0 || st.Size() != size {
		return false, fmt.Errorf("cached artifact size or type differs")
	}
	actual, err := runtime.HashGGUFFile(path)
	if err != nil || actual != pin {
		return false, fmt.Errorf("cached artifact hash mismatch")
	}
	return true, nil
}
func (a *Agent) policyLimits(lim pull.Limits) (pull.Limits, error) {
	p, err := a.cachePolicy()
	if err != nil {
		return lim, err
	}
	if p != nil {
		lim.Floor = p.ReserveBytes
		lim.FloorSet = true
	}
	return lim, nil
}
func policyBudget(p *CachePolicy) string { return strconv.FormatUint(p.MaxBytes, 10) }

func (a *Agent) cachedHashes() []string {
	dir, err := modelCacheDir(a.cfg.GGUFHostPath())
	if err != nil {
		return nil
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	idx, err := a.reconcileCacheDirLocked(dir)
	if err != nil {
		return nil
	}
	ids := []string{}
	for _, m := range idx.Models {
		if len(ids) >= 1024 {
			break
		}
		ids = append(ids, m.ID)
	}
	return ids
}
