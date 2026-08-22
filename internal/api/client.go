// Package api is the HTTP client for the Hypermesh control-plane agent
// routes on Panopticon (:8010, prefix /api/v1/hypermesh).
//
// There is no tunnel allocate HTTP and no lease-start / workload pull.
// This package does not invent those endpoints.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DeviceAuth is a hook for a future control-plane device secret header.
//
// Docs say the agent uses a device credential after enroll. The control
// plane currently returns only credential_prefix and does not check Bearer
// on agent routes. Leave Header empty until the control plane documents
// a header name. Do not send Bearer on a guess.
type DeviceAuth struct {
	Header string
	Value  string
}

// Client talks to the four agent routes.
type Client struct {
	BaseURL    string
	TenantID   string
	HTTP       *http.Client
	DeviceAuth DeviceAuth
}

// NewClient returns a client with a 15s HTTP timeout.
func NewClient(baseURL, tenantID string) *Client {
	return &Client{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		TenantID: tenantID,
		HTTP:     &http.Client{Timeout: 15 * time.Second},
	}
}

// Enroll exchanges a one-time enroll token for a device_id.
// The caller must not log req.EnrollToken.
func (c *Client) Enroll(ctx context.Context, req EnrollRequest) (EnrollResponse, error) {
	var out EnrollResponse
	err := c.doJSON(ctx, http.MethodPost, PathEnroll, nil, req, http.StatusOK, &out)
	return out, err
}

// Heartbeat reports device liveness. uptime_s must be >= 0.
func (c *Client) Heartbeat(ctx context.Context, req HeartbeatRequest) (HeartbeatResponse, error) {
	if req.UptimeS < 0 {
		req.UptimeS = 0
	}
	var out HeartbeatResponse
	err := c.doJSON(ctx, http.MethodPost, PathHeartbeat, nil, req, http.StatusOK, &out)
	return out, err
}

// PullJob returns the next pending job, or (nil, nil) on 204.
func (c *Client) PullJob(ctx context.Context, deviceID string) (*CertJob, error) {
	q := url.Values{"device_id": {deviceID}}
	var out CertJob
	err := c.doJSON(ctx, http.MethodGet, PathJobs, q, nil, http.StatusOK, &out)
	if err == errNoContent {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// PostResult reports a stub Path B (or instance-check) outcome.
func (c *Client) PostResult(ctx context.Context, jobID string, req JobResultRequest) (CertJob, error) {
	var out CertJob
	path := fmt.Sprintf(PathJobResult, url.PathEscape(jobID))
	err := c.doJSON(ctx, http.MethodPost, path, nil, req, http.StatusOK, &out)
	return out, err
}

var errNoContent = fmt.Errorf("no content")

func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, body any, want int, dest any) error {
	if strings.TrimSpace(c.TenantID) == "" {
		return ErrMissingTenant
	}

	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return fmt.Errorf("hypermesh api: base url: %w", err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	if query != nil {
		u.RawQuery = query.Encode()
	}

	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("hypermesh api: encode: %w", err)
		}
		rdr = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), rdr)
	if err != nil {
		return err
	}
	req.Header.Set(HeaderTenantID, c.TenantID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if h := strings.TrimSpace(c.DeviceAuth.Header); h != "" && c.DeviceAuth.Value != "" {
		req.Header.Set(h, c.DeviceAuth.Value)
	}

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusNoContent {
		return errNoContent
	}
	if resp.StatusCode != want {
		return mapStatus(method, path, resp.StatusCode)
	}
	if dest == nil {
		return nil
	}
	if len(payload) == 0 {
		return fmt.Errorf("hypermesh api: empty %d body", resp.StatusCode)
	}
	if err := json.Unmarshal(payload, dest); err != nil {
		return fmt.Errorf("hypermesh api: decode: %w", err)
	}
	return nil
}
