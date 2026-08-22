package api

import (
	"errors"
	"fmt"
	"net/http"
)

var (
	// ErrInvalidEnrollToken is a 401 from enroll.
	ErrInvalidEnrollToken = errors.New("hypermesh api: enroll token rejected")
	// ErrEnrollTokenUsed is a 409 from enroll.
	ErrEnrollTokenUsed = errors.New("hypermesh api: enroll token already used")
	// ErrUnknownDevice is a 404 from heartbeat or job pull.
	ErrUnknownDevice = errors.New("hypermesh api: unknown device")
	// ErrUnknownJob is a 404 from job result.
	ErrUnknownJob = errors.New("hypermesh api: unknown job")
	// ErrMissingTenant is a local client error before any HTTP call.
	ErrMissingTenant = errors.New("hypermesh api: X-Tenant-ID is required")
)

// StatusError is an unexpected HTTP status. Message is a short, non-secret
// description (status text). Response bodies are not kept.
type StatusError struct {
	Method     string
	Path       string
	StatusCode int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("hypermesh api: %s %s: HTTP %d", e.Method, e.Path, e.StatusCode)
}

func mapStatus(method, path string, code int) error {
	switch {
	case method == http.MethodPost && path == PathEnroll && code == http.StatusUnauthorized:
		return ErrInvalidEnrollToken
	case method == http.MethodPost && path == PathEnroll && code == http.StatusConflict:
		return ErrEnrollTokenUsed
	case method == http.MethodPost && path == PathHeartbeat && code == http.StatusNotFound:
		return ErrUnknownDevice
	case method == http.MethodGet && path == PathJobs && code == http.StatusNotFound:
		return ErrUnknownDevice
	case method == http.MethodPost && code == http.StatusNotFound:
		return ErrUnknownJob
	default:
		return &StatusError{Method: method, Path: path, StatusCode: code}
	}
}
