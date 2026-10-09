package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// ModelDownloadGrant is ephemeral. Never serialize it into host state or logs.
type ModelDownloadGrant struct {
	RevisionID          string `json:"revision_id"`
	JobID               string `json:"job_id"`
	DownloadURL         string `json:"download_url"`
	VersionID           string `json:"version_id"`
	ETag                string `json:"etag"`
	SHA256              string `json:"sha256"`
	SizeBytes           int64  `json:"size_bytes"`
	ExpiresAt           string `json:"expires_at"`
	Backend             string `json:"backend"`
	RuntimeImageDigest  string `json:"runtime_image_digest"`
	ConfigurationSHA256 string `json:"configuration_sha256"`
}

func (c *Client) PrivateModelGrant(ctx context.Context, jobID, revisionID string) (ModelDownloadGrant, error) {
	var out ModelDownloadGrant
	if jobID == "" || revisionID == "" {
		return out, fmt.Errorf("private model job identity required")
	}
	path := "/api/v1/hypermesh/agent/jobs/" + url.PathEscape(jobID) + "/model-grant"
	err := c.doJSON(ctx, http.MethodPost, path, nil, map[string]string{"revision_id": revisionID}, http.StatusOK, &out, true)
	if err != nil {
		return ModelDownloadGrant{}, fmt.Errorf("private model grant unavailable")
	}
	return out, nil
}
