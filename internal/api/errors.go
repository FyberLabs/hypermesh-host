package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// PeerPublicKeyNotRegistered is the GET /agent/tunnel 409 code when the
// plane has a session but no host public key yet.
const PeerPublicKeyNotRegistered = "peer_public_key_not_registered"

var (
	// ErrInvalidEnrollToken is a 401 from enroll.
	ErrInvalidEnrollToken = errors.New("hypermesh api: enroll token rejected")
	// ErrEnrollTokenUsed is a 409 from enroll.
	ErrEnrollTokenUsed = errors.New("hypermesh api: enroll token already used")
	// ErrUnauthorized is a 401 on a device-authenticated route: missing or wrong secret.
	ErrUnauthorized = errors.New("hypermesh api: missing or wrong device secret")
	// ErrSecretMismatch is a 403: secret does not match device_id.
	ErrSecretMismatch = errors.New("hypermesh api: device secret does not match device_id")
	// ErrMissingSecret is a local error before a call that requires Bearer.
	ErrMissingSecret = errors.New("hypermesh api: Bearer device secret is required")
	// ErrUnknownDevice is a 404 from heartbeat, job pull, or tunnel GET.
	ErrUnknownDevice = errors.New("hypermesh api: unknown device")
	// ErrNoTunnel is POST /agent/tunnel/peer 404 detail "Tunnel not found".
	// The plane still saved wg_public_key on the device. The agent treats
	// this as success with no session to apply.
	ErrNoTunnel = errors.New("hypermesh api: no tunnel allocated")
	// ErrTunnelRevoked is POST /agent/tunnel/peer 409 detail "Tunnel is revoked".
	// Do not bring the WireGuard interface up.
	ErrTunnelRevoked = errors.New("hypermesh api: tunnel is revoked")
	// ErrPeerPublicKeyNotRegistered is GET /agent/tunnel 409 with code
	// peer_public_key_not_registered. POST the public key, then GET again.
	ErrPeerPublicKeyNotRegistered = errors.New("hypermesh api: peer public key not registered")
	// ErrPeerKeyLength is a local check: the plane accepts 32 to 64 characters.
	ErrPeerKeyLength = errors.New("hypermesh api: peer public key must be 32 to 64 characters")
	// ErrUnknownJob is a 404 from job result.
	ErrUnknownJob = errors.New("hypermesh api: unknown job")
	// ErrMissingTenant is a local client error before any HTTP call.
	ErrMissingTenant = errors.New("hypermesh api: X-Tenant-ID is required")
)

// DecodeError is a JSON body this client could not decode.
// The response body is not included; it may echo tokens or keys.
type DecodeError struct {
	Err error
}

func (e *DecodeError) Error() string {
	if e == nil || e.Err == nil {
		return "hypermesh api: decode"
	}
	return "hypermesh api: decode: " + e.Err.Error()
}

func (e *DecodeError) Unwrap() error { return e.Err }

// StatusError is an unexpected HTTP status. Response bodies are not kept
// (they may echo tokens or keys).
// HasRetryAfter is set only for 503 when the Retry-After header parsed.
// RetryAfter is the delay the header asked for, before the caller clamps it.
type StatusError struct {
	Method        string
	Path          string
	StatusCode    int
	HasRetryAfter bool
	RetryAfter    time.Duration
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("hypermesh api: %s %s: HTTP %d", e.Method, e.Path, e.StatusCode)
}

func mapStatus(method, path string, code int, payload []byte, hdr http.Header) error {
	if path == PathEnroll {
		switch code {
		case http.StatusUnauthorized:
			return ErrInvalidEnrollToken
		case http.StatusConflict:
			return ErrEnrollTokenUsed
		}
	}
	if method == http.MethodGet && path == PathTunnel && code == http.StatusConflict && peerCode(payload) == PeerPublicKeyNotRegistered {
		return ErrPeerPublicKeyNotRegistered
	}
	if method == http.MethodPost && path == PathTunnelPeer && code == http.StatusNotFound {
		return ErrNoTunnel
	}
	if method == http.MethodPost && path == PathTunnelPeer && code == http.StatusConflict && detailString(payload) == "Tunnel is revoked" {
		return ErrTunnelRevoked
	}
	// The updates route is not an unknown-device lookup. A 404 body must
	// not be copied: it can echo a SAS URL.
	if strings.HasPrefix(path, Prefix+"/agent/updates/") && code == http.StatusNotFound {
		return &StatusError{Method: method, Path: path, StatusCode: code}
	}
	switch code {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusForbidden:
		return ErrSecretMismatch
	case http.StatusNotFound:
		if strings.HasSuffix(path, "/result") {
			return ErrUnknownJob
		}
		return ErrUnknownDevice
	default:
		return statusErr(method, path, code, hdr)
	}
}

func statusErr(method, path string, code int, hdr http.Header) *StatusError {
	se := &StatusError{Method: method, Path: path, StatusCode: code}
	if code != http.StatusServiceUnavailable || hdr == nil {
		return se
	}
	d, ok := parseRetryAfter(hdr.Get("Retry-After"), time.Now())
	if !ok {
		return se
	}
	se.HasRetryAfter = true
	se.RetryAfter = d
	return se
}

// parseRetryAfter reads a delta-seconds or HTTP-date Retry-After value.
// ok is false when the header is missing or not a delay. A present value
// of zero or a date in the past returns 0, true so the caller can clamp
// it instead of treating the header as absent.
func parseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs < 0 {
			return 0, true
		}
		return time.Duration(secs) * time.Second, true
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return 0, false
	}
	d := t.Sub(now)
	if d < 0 {
		return 0, true
	}
	return d, true
}

func detailString(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	var body struct {
		Detail json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(payload, &body); err != nil || len(body.Detail) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(body.Detail, &s); err != nil {
		return ""
	}
	return s
}

// peerCode reads the top-level code field. The control plane's
// ApiErrorBody carries code, which is peer_public_key_not_registered and
// detail is a sentence. detail is not the match.
func peerCode(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return ""
	}
	return body.Code
}
