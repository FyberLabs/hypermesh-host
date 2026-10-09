package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/pathb"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
)

const privateRevisionFixture = "1f3f9838-4ae2-49d5-9a44-fbc0fb000cc0"

func privateFixture(t *testing.T, data []byte) (api.Job, api.ModelDownloadGrant) {
	t.Helper()
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	config := &api.ModelConfiguration{ContextTokens: 4096, Jinja: false, TimeoutSeconds: 120}
	configHash, err := modelConfigurationHash(config)
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second).Format(time.RFC3339)
	job := api.Job{JobID: "private-job", Kind: api.KindPathB, CatalogID: "private-scope", SourceRef: privateModelPrefix + privateRevisionFixture,
		FileHash: "sha256:" + hash, SizeBytes: int64(len(data)), ImageDigest: "ghcr.io/fyberlabs/hypermesh-llama@sha256:" + strings.Repeat("a", 64),
		Backend: "cuda-jetson", Runtime: "nvidia", ModelConfiguration: config, ModelConfigurationSHA256: configHash}
	q := url.Values{"versionid": {"sealed-version"}, "sp": {"r"}, "sr": {"bv"}, "spr": {"https"}, "sig": {"secret-sas"}, "se": {expires}}
	grant := api.ModelDownloadGrant{RevisionID: privateRevisionFixture, JobID: job.JobID, VersionID: "sealed-version", ETag: `"sealed-etag"`, SHA256: hash, SizeBytes: job.SizeBytes,
		ExpiresAt: expires, Backend: job.Backend, RuntimeImageDigest: job.ImageDigest, ConfigurationSHA256: configHash,
		DownloadURL: "https://modeltest.blob.core.windows.net/quarantine/uploads/" + testTenant + "/" + privateRevisionFixture + "/0000?" + q.Encode()}
	return job, grant
}

func TestPrivateGrantBindsEveryPlacementInputAndExactAzureVersion(t *testing.T) {
	job, grant := privateFixture(t, []byte("GGUFweights"))
	if err := validatePrivateGrant(grant, job, "modeltest", testTenant, time.Now()); err != nil {
		t.Fatal(err)
	}
	changes := map[string]func(*api.ModelDownloadGrant){
		"revision":      func(g *api.ModelDownloadGrant) { g.RevisionID = testDevice },
		"job":           func(g *api.ModelDownloadGrant) { g.JobID = "other" },
		"hash":          func(g *api.ModelDownloadGrant) { g.SHA256 = strings.Repeat("b", 64) },
		"size":          func(g *api.ModelDownloadGrant) { g.SizeBytes++ },
		"backend":       func(g *api.ModelDownloadGrant) { g.Backend = "hip-rocm" },
		"runtime":       func(g *api.ModelDownloadGrant) { g.RuntimeImageDigest += "changed" },
		"configuration": func(g *api.ModelDownloadGrant) { g.ConfigurationSHA256 = strings.Repeat("b", 64) },
		"version":       func(g *api.ModelDownloadGrant) { g.VersionID = "replaced" },
		"expired":       func(g *api.ModelDownloadGrant) { g.ExpiresAt = time.Now().Add(-time.Minute).Format(time.RFC3339) },
		"long grant":    func(g *api.ModelDownloadGrant) { g.ExpiresAt = time.Now().Add(time.Hour).Format(time.RFC3339) },
		"account": func(g *api.ModelDownloadGrant) {
			g.DownloadURL = strings.Replace(g.DownloadURL, "modeltest.", "attacker.", 1)
		},
		"http":            func(g *api.ModelDownloadGrant) { g.DownloadURL = strings.Replace(g.DownloadURL, "https:", "http:", 1) },
		"path":            func(g *api.ModelDownloadGrant) { g.DownloadURL = strings.Replace(g.DownloadURL, "/0000?", "/0001?", 1) },
		"write grant":     func(g *api.ModelDownloadGrant) { g.DownloadURL = strings.Replace(g.DownloadURL, "sp=r", "sp=rw", 1) },
		"unversioned":     func(g *api.ModelDownloadGrant) { g.DownloadURL = strings.Replace(g.DownloadURL, "sr=bv", "sr=b", 1) },
		"query operation": func(g *api.ModelDownloadGrant) { g.DownloadURL += "&comp=list" },
		"duplicate":       func(g *api.ModelDownloadGrant) { g.DownloadURL += "&versionid=other" },
		"userinfo": func(g *api.ModelDownloadGrant) {
			g.DownloadURL = strings.Replace(g.DownloadURL, "https://", "https://user@", 1)
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			bad := grant
			change(&bad)
			if validatePrivateGrant(bad, job, "modeltest", testTenant, time.Now()) == nil {
				t.Fatal("accepted changed binding")
			}
		})
	}
	job.ModelConfiguration.ContextTokens++
	if validatePrivateGrant(grant, job, "modeltest", testTenant, time.Now()) == nil {
		t.Fatal("accepted changed reviewed settings")
	}
}

func TestPrivateCacheHitStillRequestsFreshDeviceAuthorizedGrant(t *testing.T) {
	dir := t.TempDir()
	data := []byte("GGUFprivate weights")
	job, grant := privateFixture(t, data)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/v1/hypermesh/agent/jobs/private-job/model-grant" || r.Header.Get("Authorization") != "Bearer device-secret" || r.Header.Get("X-Tenant-ID") != testTenant {
			t.Error("grant request was not device-bound")
		}
		if calls == 2 {
			w.WriteHeader(403)
			return
		}
		json.NewEncoder(w).Encode(grant)
	}))
	defer server.Close()
	a := newTestAgent(t, server.URL, dir, filepath.Join(dir, "uptime"), "", "")
	a.cfg.PrivateBlobAccount = "modeltest"
	a.cfg.GGUFPath = filepath.Join(dir, "model.gguf")
	a.cfg.ModelCacheMax = "100000000000"
	a.client.DeviceSecret = "device-secret"
	dest, err := a.cacheDest(job.FileHash)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(dest, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = a.fetchPrivateModel(context.Background(), job, dest); err != nil {
		t.Fatal(err)
	}
	if err = a.fetchPrivateModel(context.Background(), job, dest); err == nil {
		t.Fatal("cache bypassed revoked grant")
	}
	if calls != 2 {
		t.Fatalf("grant calls=%d", calls)
	}
}

type privateTransport func(*http.Request) (*http.Response, error)

func (f privateTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPrivateBlobFetchRequiresVersionETagAndSizeWithoutBearer(t *testing.T) {
	_, grant := privateFixture(t, []byte("GGUFweights"))
	for _, mismatch := range []string{"", "etag", "version", "size", "status"} {
		t.Run(mismatch, func(t *testing.T) {
			client := &http.Client{Transport: privateTransport(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != "" || r.Header.Get("If-Match") != grant.ETag {
					t.Fatal("blob request credential/condition changed")
				}
				response := &http.Response{StatusCode: 200, ContentLength: grant.SizeBytes, Header: http.Header{"Etag": {grant.ETag}, "X-Ms-Version-Id": {grant.VersionID}}, Body: io.NopCloser(strings.NewReader("GGUFweights")), Request: r}
				switch mismatch {
				case "etag":
					response.Header.Set("ETag", "other")
				case "version":
					response.Header.Set("x-ms-version-id", "other")
				case "size":
					response.ContentLength++
				case "status":
					response.StatusCode = 302
				}
				return response, nil
			})}
			body, err := privateBlobFetcher(client, grant)(context.Background(), grant.DownloadURL)
			if mismatch == "" {
				if err != nil {
					t.Fatal(err)
				}
				body.Close()
			} else if err == nil {
				t.Fatal("accepted changed storage response")
			}
		})
	}
}

func TestPrivateSettingsUseReviewedConfigurationInsteadOfHostDefaults(t *testing.T) {
	job, _ := privateFixture(t, []byte("GGUFweights"))
	a := &Agent{cfg: Config{LlamaJinja: "1", LlamaCtxSize: "16384", LlamaTimeout: "1800"}}
	jinja, ctx, timeout, err := a.modelSettings(job)
	if err != nil || jinja != "0" || ctx != "4096" || timeout != "120" {
		t.Fatal("reviewed private configuration not preserved")
	}
	job.ModelConfiguration.TimeoutSeconds++
	if _, _, _, err = a.modelSettings(job); err == nil {
		t.Fatal("accepted modified reviewed config")
	}
}

func TestRunningPrivateLeaseRetainsAuthorizationAcrossRestartAndChangedRetries(t *testing.T) {
	job, grant := privateFixture(t, []byte("GGUFprivate weights"))
	job.LeaseID = "private-lease"
	allowed := true
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !allowed {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		json.NewEncoder(w).Encode(grant)
	}))
	defer server.Close()
	dir := t.TempDir()
	a := newTestAgent(t, server.URL, dir, filepath.Join(dir, "uptime"), "", "")
	a.cfg.PrivateBlobAccount = "modeltest"
	a.client.DeviceSecret = "device-secret"
	handle := &runtime.Handle{LeaseID: job.LeaseID, CatalogID: job.CatalogID, ImageDigest: job.ImageDigest, FileHash: job.FileHash,
		Backend: job.Backend, PrivateRevisionID: privateRevisionFixture, PrivateRuntime: job.Runtime,
		PrivateConfigurationSHA256: job.ModelConfigurationSHA256, PrivateSizeBytes: job.SizeBytes}
	if err := a.saveLease(handle); err != nil {
		t.Fatal(err)
	}
	persisted := a.loadLease()
	if persisted == nil {
		t.Fatal("private provenance lost on restart")
	}
	if err := a.authorizeLeaseRetry(context.Background(), job, persisted); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("same-lease retry skipped authorization")
	}
	mutations := map[string]func(*api.Job){
		"omitted source":   func(j *api.Job) { j.SourceRef = "" },
		"public source":    func(j *api.Job) { j.SourceRef = "https://huggingface.co/public.gguf" },
		"changed revision": func(j *api.Job) { j.SourceRef = privateModelPrefix + testDevice },
		"backend":          func(j *api.Job) { j.Backend = "hip-rocm" },
		"runtime":          func(j *api.Job) { j.Runtime = "other" },
		"configuration": func(j *api.Job) {
			c := *j.ModelConfiguration
			c.ContextTokens++
			j.ModelConfiguration = &c
			j.ModelConfigurationSHA256, _ = modelConfigurationHash(&c)
		},
		"image":   func(j *api.Job) { j.ImageDigest += "changed" },
		"file":    func(j *api.Job) { j.FileHash += "changed" },
		"catalog": func(j *api.Job) { j.CatalogID += "changed" },
		"size":    func(j *api.Job) { j.SizeBytes++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := job
			mutate(&changed)
			if a.authorizeLeaseRetry(context.Background(), changed, persisted) == nil {
				t.Fatal("acknowledged changed running binding")
			}
		})
	}
	allowed = false
	if a.authorizeLeaseRetry(context.Background(), job, persisted) == nil {
		t.Fatal("acknowledged revoked running lease")
	}
	if calls != 2 {
		t.Fatal("revoked retry did not request fresh grant")
	}
	legacy := *persisted
	legacy.PrivateRevisionID = ""
	if a.authorizeLeaseRetry(context.Background(), job, &legacy) == nil {
		t.Fatal("accepted missing provenance")
	}
}

func TestPrivateLabMeasurementsNeverPublishCatalogScorecards(t *testing.T) {
	job, _ := privateFixture(t, []byte("GGUFweights"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("private lab measurement reached public catalog scorecard API")
	}))
	defer server.Close()
	a := newTestAgent(t, server.URL, t.TempDir(), "", "", "")
	result := pathb.Result{Passed: true, Measured: true, WorkloadVerified: true, ImageHash: job.FileHash}
	if err := a.reportScorecard(context.Background(), job, result); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateCacheCannotBecomePublicAfterIndexRebuildOrMissingSource(t *testing.T) {
	data := []byte("GGUFprivate weights")
	job, grant := privateFixture(t, data)
	calls := 0
	allow := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !allow {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		json.NewEncoder(w).Encode(grant)
	}))
	defer server.Close()
	dir := t.TempDir()
	a := newTestAgent(t, server.URL, dir, filepath.Join(dir, "uptime"), "", "")
	a.cfg.PrivateBlobAccount = "modeltest"
	a.cfg.GGUFPath = filepath.Join(dir, "model.gguf")
	a.cfg.ModelCacheMax = "100000000000"
	a.client.DeviceSecret = "device-secret"
	a.bindIP = "10.77.0.2/32"
	dest, err := a.cacheDest(job.FileHash)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(dest), 0700)
	os.WriteFile(dest, data, 0600)
	if err := a.fetchPrivateModel(context.Background(), job, dest); err != nil {
		t.Fatal(err)
	}
	if err := a.admitModel(job.CatalogID, job.FileHash, dest); err != nil {
		t.Fatal(err)
	}
	if err := a.notePendingModel(job.CatalogID, job.FileHash); err != nil {
		t.Fatal(err)
	}
	// The generic index is disposable. Private provenance must survive its rebuild.
	os.Remove(filepath.Join(filepath.Dir(dest), cacheIndexName))
	if err := a.reconcileModelCache(); err != nil {
		t.Fatal(err)
	}
	if err := a.notePendingModel(job.CatalogID, job.FileHash); err != nil {
		t.Fatal(err)
	}
	runs := 0
	a.runtime = &runtime.NVIDIA{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "run" {
			runs++
		}
		return []byte("cid\n"), nil
	}}
	omitted := job
	omitted.Kind, omitted.LeaseID, omitted.SourceRef = api.KindLeaseStart, "new-private-lease", ""
	if result := a.leaseStart(context.Background(), omitted); result.Passed {
		t.Fatal("omitted source mounted private cache")
	}
	if runs != 0 || calls != 1 {
		t.Fatal("private provenance bypassed before startup")
	}
	allow = false
	if err := a.authorizePrivateCacheFile(context.Background(), job, dest); err == nil {
		t.Fatal("revoked grant mounted private cache")
	}
	if calls != 2 {
		t.Fatal("private cache consumer did not request fresh authorization")
	}
	changed := job
	changed.Runtime = "other"
	if _, err := a.checkPrivateCacheBinding(changed, dest); err == nil {
		t.Fatal("changed runtime reused private cache")
	}
	a.cfg.TenantID = testDevice
	if _, err := a.checkPrivateCacheBinding(job, dest); err == nil {
		t.Fatal("another tenant reused private cache")
	}
}

func TestPrivateCacheMarkIsDurableBeforeBytesAndMalformedProvenanceFailsClosed(t *testing.T) {
	a, _ := newCacheAgent(t)
	job, _ := privateFixture(t, []byte("GGUFweights"))
	dest, err := a.cacheDest(job.FileHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.rememberPrivateCache(job, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("provenance test unexpectedly created bytes")
	}
	public := job
	public.SourceRef = ""
	if _, err := a.checkPrivateCacheBinding(public, dest); err == nil {
		t.Fatal("pre-transfer private mark was not durable")
	}
	os.WriteFile(dest+".private.json", []byte("["), 0600)
	if _, err := a.checkPrivateCacheBinding(public, dest); err == nil {
		t.Fatal("malformed provenance became public")
	}
}
