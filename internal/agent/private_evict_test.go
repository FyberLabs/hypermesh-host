package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

func TestPrivateEvictionRemovesBytesBeforeProvenanceAndSurvivesRetry(t *testing.T) {
	job, _ := privateFixture(t, []byte("GGUFprivate"))
	a := &Agent{cfg: Config{TenantID: "  " + strings.ToUpper(testTenant) + "  ", GGUFPath: filepath.Join(t.TempDir(), "weights.gguf")}, log: slog.Default()}
	dest, err := a.cacheDest(job.FileHash)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.rememberPrivateCache(job, dest); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(dest, []byte("GGUFprivate"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if result := a.evictPrivateModel(context.Background(), job); !result.Passed {
			t.Fatal(result.Reason)
		}
	}
	if _, err = os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("private bytes remained")
	}
	if _, err = os.Stat(dest + ".private.json"); !os.IsNotExist(err) {
		t.Fatal("private marker remained")
	}
}

func TestPrivateEvictionPreservesOtherRevisionSharingTheHash(t *testing.T) {
	job, _ := privateFixture(t, []byte("GGUFprivate"))
	a := &Agent{cfg: Config{TenantID: testTenant, GGUFPath: filepath.Join(t.TempDir(), "weights.gguf")}, log: slog.Default()}
	dest, _ := a.cacheDest(job.FileHash)
	if err := a.rememberPrivateCache(job, dest); err != nil {
		t.Fatal(err)
	}
	other := job
	other.SourceRef = privateModelPrefix + testDevice
	if err := a.rememberPrivateCache(other, dest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("GGUFprivate"), 0600); err != nil {
		t.Fatal(err)
	}
	if result := a.evictPrivateModel(context.Background(), job); !result.Passed {
		t.Fatal(result.Reason)
	}
	if marked, err := a.checkPrivateCacheBinding(other, dest); err != nil || !marked {
		t.Fatal("other authorized revision lost", err)
	}
	if _, err := a.checkPrivateCacheBinding(job, dest); err == nil {
		t.Fatal("revoked revision still bound")
	}
}

func TestPrivateEvictionCannotStopAnotherRunningLease(t *testing.T) {
	job, _ := privateFixture(t, []byte("GGUFprivate"))
	a := &Agent{cfg: Config{TenantID: testTenant, GGUFPath: filepath.Join(t.TempDir(), "weights.gguf")}, log: slog.Default()}
	dest, _ := a.cacheDest(job.FileHash)
	if err := a.rememberPrivateCache(job, dest); err != nil {
		t.Fatal(err)
	}
	a.setLease(&runtime.Handle{LeaseID: "other-lease", FileHash: job.FileHash, PrivateRevisionID: testDevice})
	if result := a.evictPrivateModel(context.Background(), job); result.Passed {
		t.Fatal("eviction stopped a different workload")
	}
	if a.currentLease() == nil {
		t.Fatal("other workload cleared")
	}
}
