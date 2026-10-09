package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/pathb"
)

// Authenticated plane cleanup needs no read grant for revoked bytes. Match the
// durable private provenance first; never turn retained private bytes public.
func (a *Agent) evictPrivateModel(ctx context.Context, job api.Job) pathb.Result {
	fail := func(reason string) pathb.Result { return pathb.Result{Reason: reason} }
	revision, err := privateRevision(job.SourceRef)
	if err != nil {
		return fail("private eviction reference invalid")
	}
	dest, err := a.cacheDest(job.FileHash)
	if err != nil {
		return fail("private eviction hash invalid")
	}
	unlock, err := LockModelCache(ctx, filepath.Dir(filepath.Dir(dest)))
	if err != nil {
		return fail("private eviction cache lock unavailable")
	}
	defer unlock()
	bindings, err := readPrivateBindings(dest)
	if err != nil {
		return fail("private eviction provenance unavailable")
	}
	matching := false
	kept := []privateCacheBinding{}
	for _, binding := range bindings {
		if binding.Tenant == strings.ToLower(strings.TrimSpace(a.cfg.TenantID)) && binding.Revision == revision {
			matching = true
		} else {
			kept = append(kept, binding)
		}
	}
	if !matching {
		return pathb.Result{Passed: true, ImageHash: job.FileHash, Reason: "private model already evicted"}
	}
	if handle := a.localLease(); handle != nil && handle.FileHash == job.FileHash {
		if handle.PrivateRevisionID != revision || job.LeaseID == "" || handle.LeaseID != job.LeaseID {
			return fail("private eviction active lease differs")
		}
		if result := a.leaseStop(ctx, job); !result.Passed {
			return result
		}
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	if len(kept) > 0 {
		if err := writePrivateBindings(dest, kept); err != nil {
			return fail("private eviction provenance unavailable")
		}
	} else {
		// Delete bytes before provenance. A crash may leave a closed marker but
		// cannot leave private bytes without their authorization requirement.
		if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
			return fail("private eviction file unavailable")
		}
		if err := os.Remove(dest + ".private.json"); err != nil && !os.IsNotExist(err) {
			return fail("private eviction provenance unavailable")
		}
	}
	if _, err := a.reconcileCacheDirLocked(filepath.Dir(dest)); err != nil {
		return fail("private eviction cache index unavailable")
	}
	return pathb.Result{Passed: true, ImageHash: job.FileHash, Reason: "private model evicted"}
}
