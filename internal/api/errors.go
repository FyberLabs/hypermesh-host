package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

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
	// ErrUnknownDevice is a 404 from heartbeat, job pull, or tunnel.
	ErrUnknownDevice = errors.New("hypermesh api: unknown device")
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

func mapStatus(method, path string, code int) error {
	if path == PathEnroll {
		switch code {
		case http.StatusUnauthorized:
			return ErrInvalidEnrollToken
		case http.StatusConflict:
			return ErrEnrollTokenUsed
		}
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
