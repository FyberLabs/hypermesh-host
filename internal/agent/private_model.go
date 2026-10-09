package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/api"
	"github.com/FyberLabs/hypermesh-host/internal/pull"
)

var modelRevisionID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var blobAccountName = regexp.MustCompile(`^[a-z0-9]{3,24}$`)
var modelSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

const privateModelPrefix = "hmblob:"

func privateRevision(source string) (string, error) {
	revision := strings.TrimPrefix(source, privateModelPrefix)
	if source == revision || !modelRevisionID.MatchString(revision) {
		return "", fmt.Errorf("invalid private model reference")
	}
	return revision, nil
}

func modelConfigurationHash(c *api.ModelConfiguration) (string, error) {
	if c == nil || c.ContextTokens < 1 || c.ContextTokens > 16777216 || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 1800 {
		return "", fmt.Errorf("private model configuration required")
	}
	canonical := fmt.Sprintf(`{"context_tokens":%d,"jinja":%t,"timeout_seconds":%d}`, c.ContextTokens, c.Jinja, c.TimeoutSeconds)
	hash := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(hash[:]), nil
}

func validatePrivateGrant(grant api.ModelDownloadGrant, job api.Job, account, tenant string, now time.Time) error {
	fail := func() error { return fmt.Errorf("private model grant binding invalid") }
	revision, err := privateRevision(job.SourceRef)
	if err != nil || !blobAccountName.MatchString(account) || !modelRevisionID.MatchString(tenant) {
		return fail()
	}
	configHash, err := modelConfigurationHash(job.ModelConfiguration)
	if err != nil || configHash != job.ModelConfigurationSHA256 || configHash != grant.ConfigurationSHA256 {
		return fail()
	}
	if grant.JobID != job.JobID || grant.RevisionID != revision || grant.RuntimeImageDigest != job.ImageDigest || grant.Backend != job.Backend ||
		!modelSHA256.MatchString(grant.SHA256) || "sha256:"+grant.SHA256 != job.FileHash ||
		grant.SizeBytes != job.SizeBytes || grant.SizeBytes <= 0 || grant.SizeBytes > 50000000000 || grant.ETag == "" || grant.VersionID == "" {
		return fail()
	}
	expires, err := time.Parse(time.RFC3339Nano, grant.ExpiresAt)
	if err != nil || !expires.After(now) || expires.After(now.Add(15*time.Minute)) {
		return fail()
	}
	u, err := url.Parse(grant.DownloadURL)
	if err != nil || u.Scheme != "https" || u.Host != account+".blob.core.windows.net" || u.User != nil || u.Fragment != "" || u.RawPath != "" ||
		u.Path != "/quarantine/uploads/"+tenant+"/"+revision+"/0000" {
		return fail()
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || q.Get("versionid") != grant.VersionID || q.Get("sp") != "r" || q.Get("sr") != "bv" || q.Get("spr") != "https" || q.Get("sig") == "" {
		return fail()
	}
	allowed := map[string]bool{"versionid": true, "skoid": true, "sktid": true, "skt": true, "ske": true, "sks": true, "skv": true, "st": true, "se": true, "sp": true, "spr": true, "sv": true, "sr": true, "sig": true}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			return fail()
		}
	}
	signedExpiry, err := time.Parse(time.RFC3339Nano, q.Get("se"))
	if err != nil || !signedExpiry.After(now) || signedExpiry.After(expires) {
		return fail()
	}
	return nil
}

func privateBlobFetcher(client *http.Client, grant api.ModelDownloadGrant) pull.Fetcher {
	return func(ctx context.Context, address string) (io.ReadCloser, error) {
		if address != grant.DownloadURL {
			return nil, fmt.Errorf("private model source changed")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return nil, fmt.Errorf("private model request invalid")
		}
		req.Header.Set("If-Match", grant.ETag)
		response, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("private model transfer failed")
		}
		if response.StatusCode != http.StatusOK || response.ContentLength != grant.SizeBytes ||
			strings.Trim(response.Header.Get("ETag"), `"`) != strings.Trim(grant.ETag, `"`) || response.Header.Get("x-ms-version-id") != grant.VersionID {
			response.Body.Close()
			return nil, fmt.Errorf("private model response binding invalid")
		}
		return response.Body, nil
	}
}

func (a *Agent) authorizedPrivateGrant(ctx context.Context, job api.Job) (api.ModelDownloadGrant, error) {
	revision, err := privateRevision(job.SourceRef)
	if err != nil || a.cfg.PrivateBlobAccount == "" {
		return api.ModelDownloadGrant{}, fmt.Errorf("private model delivery is disabled")
	}
	grant, err := a.client.PrivateModelGrant(ctx, job.JobID, revision)
	if err != nil {
		return api.ModelDownloadGrant{}, fmt.Errorf("private model grant unavailable")
	}
	if err = validatePrivateGrant(grant, job, a.cfg.PrivateBlobAccount, a.cfg.TenantID, time.Now()); err != nil {
		return api.ModelDownloadGrant{}, err
	}
	return grant, nil
}

func (a *Agent) fetchPrivateModel(ctx context.Context, job api.Job, dest string) error {
	grant, err := a.authorizedPrivateGrant(ctx, job)
	if err != nil {
		return err
	}
	// Reauthorize every use, including verified cache hits. Never persist the SAS.
	lim, err := a.policyLimits(a.pullLimits(grant.SizeBytes))
	if err != nil {
		return err
	}
	if hit, err := a.verifyCachedArtifact(dest, job.FileHash, grant.SizeBytes); err != nil {
		return err
	} else if hit {
		return nil
	}
	lim, err = a.planModelEviction(dest, job.FileHash, lim)
	if err != nil {
		return err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 80 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("private model redirects denied") }}
	return pull.ToFileWith(ctx, dest, grant.DownloadURL, job.FileHash, privateBlobFetcher(client, grant), lim)
}

func artifactPullJob(job api.Job) api.Job {
	if !strings.HasPrefix(job.SourceRef, privateModelPrefix) {
		job.ImageDigest = job.FileHash
	}
	return job
}

func (a *Agent) modelSettings(job api.Job) (jinja, contextTokens, timeout string, err error) {
	if !strings.HasPrefix(job.SourceRef, privateModelPrefix) {
		return a.cfg.LlamaJinja, a.cfg.LlamaCtxSize, a.cfg.LlamaTimeout, nil
	}
	hash, err := modelConfigurationHash(job.ModelConfiguration)
	if err != nil || hash != job.ModelConfigurationSHA256 {
		return "", "", "", fmt.Errorf("private model configuration binding invalid")
	}
	jinja = "0"
	if job.ModelConfiguration.Jinja {
		jinja = "1"
	}
	return jinja, strconv.Itoa(job.ModelConfiguration.ContextTokens), strconv.Itoa(job.ModelConfiguration.TimeoutSeconds), nil
}
