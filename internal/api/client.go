// Package api is the HTTP client for the Hypermesh control-plane agent
// routes (prefix /api/v1/hypermesh). Tests compile against httptest.
// The deployed test base URL is https://api.test.hyperme.sh.
//
// After enroll, device routes send Authorization: Bearer <device_secret>
// and X-Tenant-ID, with device_id in the body or query as a cross-check.
// This package does not invent endpoints beyond the locked agent shapes
// plus the optional platform enroll-token helper.
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

// Client talks to the locked agent routes.
type Client struct {
	BaseURL      string
	TenantID     string
	DeviceSecret string // Authorization: Bearer; never log
	HTTP         *http.Client
}

// NewClient returns a client with a 15s HTTP timeout.
func NewClient(baseURL, tenantID string) *Client {
	return &Client{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		TenantID: tenantID,
		HTTP:     &http.Client{Timeout: 15 * time.Second},
	}
}

// Enroll exchanges a one-time enroll token for device_id + device_secret.
// Do not log req.EnrollToken or the returned DeviceSecret.
func (c *Client) Enroll(ctx context.Context, req EnrollRequest) (EnrollResponse, error) {
	var out EnrollResponse
	err := c.doJSON(ctx, http.MethodPost, PathEnroll, nil, req, http.StatusOK, &out, false)
	return out, err
}

// Heartbeat reports device liveness. Requires Bearer. uptime_s must be >= 0.
func (c *Client) Heartbeat(ctx context.Context, req HeartbeatRequest) (HeartbeatResponse, error) {
	if req.UptimeS < 0 {
		req.UptimeS = 0
	}
	var out HeartbeatResponse
	err := c.doJSON(ctx, http.MethodPost, PathHeartbeat, nil, req, http.StatusOK, &out, true)
	return out, err
}

// PullJob returns the next pending job, or (nil, nil) on 204. Requires Bearer.
func (c *Client) PullJob(ctx context.Context, deviceID string) (*Job, error) {
	q := url.Values{"device_id": {deviceID}}
	var out Job
	err := c.doJSON(ctx, http.MethodGet, PathJobs, q, nil, http.StatusOK, &out, true)
	if err == errNoContent {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// PostScorecard stores a measured pack sample. Requires Bearer.
// A failure here does not change the job result.
func (c *Client) PostScorecard(ctx context.Context, req ScorecardReport) error {
	return c.doJSON(ctx, http.MethodPost, PathScorecard, nil, req, http.StatusOK, nil, true)
}

// PostResult reports a job outcome. Requires Bearer. device_id is a query cross-check.
func (c *Client) PostResult(ctx context.Context, jobID, deviceID string, req JobResultRequest) error {
	q := url.Values{"device_id": {deviceID}}
	path := fmt.Sprintf(PathJobResult, url.PathEscape(jobID))
	return c.doJSON(ctx, http.MethodPost, path, q, req, http.StatusOK, nil, true)
}

// GetTunnel returns the current session, or (nil, nil) on 204. Requires Bearer.
func (c *Client) GetTunnel(ctx context.Context, deviceID string) (*TunnelSession, error) {
	q := url.Values{"device_id": {deviceID}}
	var out TunnelSession
	err := c.doJSON(ctx, http.MethodGet, PathTunnel, q, nil, http.StatusOK, &out, true)
	if err == errNoContent {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ProposePeer posts the host WireGuard public key. The private key stays here.
func (c *Client) ProposePeer(ctx context.Context, deviceID string, req TunnelPeerRequest) error {
	q := url.Values{"device_id": {deviceID}}
	return c.doJSON(ctx, http.MethodPost, PathTunnelPeer, q, req, http.StatusOK, nil, true)
}

// AckTunnel tells the control plane wg-quick (or equivalent) applied.
func (c *Client) AckTunnel(ctx context.Context, deviceID string, req TunnelAckRequest) error {
	q := url.Values{"device_id": {deviceID}}
	return c.doJSON(ctx, http.MethodPost, PathTunnelAck, q, req, http.StatusOK, nil, true)
}

// IssueEnrollToken is a platform helper: POST /devices/{id}/enroll-token.
// Tenant header only. Not used by the agent loop. Do not log the token.
func (c *Client) IssueEnrollToken(ctx context.Context, deviceID string) (EnrollTokenResponse, error) {
	var out EnrollTokenResponse
	path := fmt.Sprintf(PathDeviceEnrollToken, url.PathEscape(deviceID))
	err := c.doJSON(ctx, http.MethodPost, path, nil, struct{}{}, http.StatusOK, &out, false)
	return out, err
}

var errNoContent = fmt.Errorf("no content")

func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, body any, want int, dest any, bearer bool) error {
	if strings.TrimSpace(c.TenantID) == "" {
		return ErrMissingTenant
	}
	if bearer && strings.TrimSpace(c.DeviceSecret) == "" {
		return ErrMissingSecret
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
	if bearer {
		req.Header.Set(HeaderAuthorization, "Bearer "+c.DeviceSecret)
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
