//go:build linux || darwin

package installer

import (
	"context"
	"errors"
	"fmt"
	"github.com/FyberLabs/hypermesh-host/internal/hostregistration"
	"io"
	"time"
)

var errRegistrationExpired = errors.New("registration approval expired")

// This private journal is the recovery authority. Never encode it to diagnostics.
type registrationJournal struct {
	Schema       int                      `json:"schema"`
	PlanSHA256   string                   `json:"plan_sha256"`
	Issuer       string                   `json:"issuer"`
	Dashboard    string                   `json:"dashboard"`
	Owner        string                   `json:"owner"`
	Request      hostregistration.Request `json:"request"`
	PrivateKey   string                   `json:"private_key"`
	DeviceSecret string                   `json:"device_secret"`
	DeviceID     string                   `json:"device_id"`
	State        string                   `json:"state"`
	ExpiresAt    time.Time                `json:"expires_at,omitempty"`
}

func (registrationJournal) String() string   { return "[redacted private registration journal]" }
func (registrationJournal) GoString() string { return "[redacted private registration journal]" }

type registrationStatus struct {
	PlanSHA256 string `json:"plan_sha256"`
	RequestID  string `json:"request_id"`
	HostName   string `json:"host_name"`
	DeviceID   string `json:"device_id,omitempty"`
	State      string `json:"state"`
	NextAction string `json:"next_action"`
}

func publicRegistration(j registrationJournal) registrationStatus {
	next := "Rerun register-agent with the same plan and login to recover; do not delete the private journal."
	switch j.State {
	case "expired":
		next = "Rerun the same register-agent command to authenticate again and renew browser approval automatically; previous request history is preserved. If using guided setup, rerun the same installer."
	case "denied":
		next = "Approval ended. Rerun with --restart-approval to archive this request and require new browser approval with fresh keys."
	case "retired":
		next = "Host was retired. Do not restore its identity; use a fresh reinstall workflow."
	case "registered-inactive":
		next = "Host registered and paused. Compatibility, certification and service activation remain pending."
	}
	return registrationStatus{j.PlanSHA256, j.Request.RequestID, j.Request.HostName, j.DeviceID, j.State, next}
}
func waitRegistration(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("approval interrupted; rerun to recover")
	case <-timer.C:
		return nil
	}
}
func finishRegistration(ctx context.Context, j *registrationJournal, remote registrationRemote, save func(registrationJournal) error, publish func(registrationJournal) error, display io.Writer, wait func(context.Context, time.Duration) error) error {
	// Retrying Create is idempotent, including after a lost completion response.
	s, err := remote.Create(ctx, j.Request)
	if err != nil {
		return err
	}
	printed := false
	for {
		if j.DeviceID != "" && (s.DeviceID == nil || *s.DeviceID != j.DeviceID) {
			return fmt.Errorf("server device identity changed; preserve local state")
		}
		j.State = s.State
		if expiry, e := time.Parse(time.RFC3339Nano, s.ExpiresAt); e == nil {
			j.ExpiresAt = expiry
		}
		if s.DeviceID != nil {
			j.DeviceID = *s.DeviceID
		}
		if err = save(*j); err != nil {
			return err
		}
		switch s.State {
		case "completed":
			if err = publish(*j); err != nil {
				return err
			}
			j.State = "registered-inactive"
			return save(*j)
		case "expired":
			return errRegistrationExpired
		case "denied", "retired":
			return fmt.Errorf("registration %s; identity retained and service inactive", s.State)
		case "approved":
			s, err = remote.Complete(ctx, j.Request)
		case "pending":
			if !printed {
				if err = displayHostApproval(display, j.Dashboard+s.ApprovalPath); err != nil {
					return err
				}
				printed = true
			}
			if err = wait(ctx, 5*time.Second); err != nil {
				return err
			}
			s, err = remote.Get(ctx, j.Request)
		default:
			return fmt.Errorf("unknown registration state")
		}
		if err != nil {
			return err
		}
	}
}

type registrationRemote interface {
	Create(context.Context, hostregistration.Request) (hostregistration.Status, error)
	Get(context.Context, hostregistration.Request) (hostregistration.Status, error)
	Complete(context.Context, hostregistration.Request) (hostregistration.Status, error)
}
