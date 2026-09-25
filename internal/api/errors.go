package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
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
	// ErrNoTunnel is POST /agent/tunnel/peer 404: nothing allocated yet.
	// The agent retries on the next sync and does not treat this as a failure.
	ErrNoTunnel = errors.New("hypermesh api: no tunnel allocated")
	// ErrPeerPublicKeyNotRegistered is GET /agent/tunnel 409 with code
	// peer_public_key_not_registered. POST the public key, then GET again.
	ErrPeerPublicKeyNotRegistered = errors.New("hypermesh api: peer public key not registered")
	// ErrUnknownJob is a 404 from job result.
	ErrUnknownJob = errors.New("hypermesh api: unknown job")
	// ErrMissingTenant is a local client error before any HTTP call.
	ErrMissingTenant = errors.New("hypermesh api: X-Tenant-ID is required")
)

// StatusError is an unexpected HTTP status. Response bodies are not kept
// (they may echo tokens or keys).
type StatusError struct {
	Method     string
	Path       string
	StatusCode int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("hypermesh api: %s %s: HTTP %d", e.Method, e.Path, e.StatusCode)
}

func mapStatus(method, path string, code int, payload []byte) error {
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
		return &StatusError{Method: method, Path: path, StatusCode: code}
	}
}

// peerCode reads a typed error code. The owner named the code; the JSON
// envelope is code, detail (string or object), or error.code. The body is
// not returned to callers.
func peerCode(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	var body struct {
		Code   string          `json:"code"`
		Detail json.RawMessage `json:"detail"`
		Error  struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return ""
	}
	if body.Code != "" {
		return body.Code
	}
	if body.Error.Code != "" {
		return body.Error.Code
	}
	if len(body.Detail) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(body.Detail, &asString); err == nil {
		return asString
	}
	var asObj struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body.Detail, &asObj); err == nil {
		return asObj.Code
	}
	return ""
}
