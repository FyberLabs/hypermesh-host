package hostregistration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

type QualificationRun struct {
	CertificationRun
	Purpose     string `json:"purpose"`
	Repetitions int    `json:"repetitions"`
}
type QualificationSoftware struct {
	Kernel          string `json:"kernel"`
	OSVersion       string `json:"os_version"`
	InstallerCommit string `json:"installer_commit"`
}
type QualificationSample struct {
	CertificationEvidence
	MeasurementID string `json:"measurement_id"`
}
type QualificationReport struct {
	Samples     []QualificationSample `json:"samples"`
	FailureCode string                `json:"failure_code"`
	Software    QualificationSoftware `json:"software"`
}

func (c *Client) qualification(ctx context.Context, r Request, device, secret, id string, previous *QualificationRun, report *QualificationReport) (QualificationRun, error) {
	var out QualificationRun
	if r.Validate() != nil || !UUID.MatchString(device) || !UUID.MatchString(id) || !onboardingSecretRE.MatchString(secret) {
		return out, fmt.Errorf("invalid qualification identity")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return out, err
	}
	nonce := hex.EncodeToString(b)
	body := map[string]any{"protocol_version": 1, "request_id": r.RequestID, "device_id": device, "run_id": id, "nonce": nonce}
	op := "plan"
	if previous != nil {
		body["manifest_sha256"] = previous.ManifestSHA256
	}
	if report != nil {
		op = "result"
		body["samples"] = report.Samples
		body["failure_code"] = report.FailureCode
		body["software"] = report.Software
	}
	if err := c.request(ctx, "POST", "/api/v1/hypermesh/agent/onboarding/qualification/"+op, secret, body, &out); err != nil {
		return out, err
	}
	if out.Purpose != "lab-qualification-v1" || out.Repetitions != 3 || out.ProtocolVersion != 1 || out.Nonce != nonce || out.RunID != id || out.TenantID != c.tenant || out.RequestID != r.RequestID || out.DeviceID != device || out.Manifest.ClassID != r.ClassID || out.Manifest.ValidateQualification() != nil || !hashRE.MatchString(out.ManifestSHA256) || !oneOf(out.State, "planned", "passed", "failed") || out.ExpiresAt.IsZero() || out.SellingAllowed == nil || *out.SellingAllowed || out.ActivationAllowed != nil {
		return QualificationRun{}, fmt.Errorf("qualification response does not match this installation")
	}
	if previous != nil && (out.ManifestSHA256 != previous.ManifestSHA256 || !SameManifest(out.Manifest, previous.Manifest)) {
		return QualificationRun{}, fmt.Errorf("qualification manifest changed")
	}
	if report != nil {
		expected := "passed"
		if report.FailureCode != "" {
			expected = "failed"
		}
		if out.State != expected {
			return QualificationRun{}, fmt.Errorf("qualification result not acknowledged in expected terminal state")
		}
	}
	return out, nil
}
func (c *Client) PlanQualification(ctx context.Context, r Request, device, secret, id string) (QualificationRun, error) {
	return c.qualification(ctx, r, device, secret, id, nil, nil)
}
func (c *Client) ReportQualification(ctx context.Context, r Request, device, secret string, run QualificationRun, report QualificationReport) (QualificationRun, error) {
	return c.qualification(ctx, r, device, secret, run.RunID, &run, &report)
}

// QualificationNext is a device-bound decision, never authority to activate.
type QualificationNext struct {
	ProtocolVersion   int     `json:"protocol_version"`
	Nonce             string  `json:"nonce"`
	TenantID          string  `json:"tenant_id"`
	DeviceID          string  `json:"device_id"`
	RequestID         string  `json:"request_id"`
	Action            string  `json:"action"`
	RunID             *string `json:"run_id"`
	State             *string `json:"state"`
	SellingAllowed    *bool   `json:"selling_allowed"`
	ActivationAllowed *bool   `json:"activation_allowed"`
}

func (c *Client) NextQualification(ctx context.Context, r Request, device, secret string) (QualificationNext, error) {
	var out QualificationNext
	if r.Validate() != nil || !UUID.MatchString(device) || !onboardingSecretRE.MatchString(secret) {
		return out, fmt.Errorf("invalid qualification identity")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return out, err
	}
	nonce := hex.EncodeToString(b)
	id, err := NewRunID()
	if err != nil {
		return out, err
	}
	body := map[string]any{"protocol_version": 1, "nonce": nonce, "request_id": r.RequestID, "device_id": device, "run_id": id}
	if err = c.request(ctx, "POST", "/api/v1/hypermesh/agent/onboarding/qualification/next", secret, body, &out); err != nil {
		return out, err
	}
	if out.ProtocolVersion != 1 || out.Nonce != nonce || out.TenantID != c.tenant || out.DeviceID != device || out.RequestID != r.RequestID || out.SellingAllowed == nil || *out.SellingAllowed || out.ActivationAllowed == nil || *out.ActivationAllowed {
		return QualificationNext{}, fmt.Errorf("qualification decision is not bound to this inactive host")
	}
	if out.Action == "certification" {
		if out.RunID != nil || out.State != nil {
			return QualificationNext{}, fmt.Errorf("invalid certification decision")
		}
		return out, nil
	}
	if !oneOf(out.Action, "qualification", "review", "attention") || out.RunID == nil || !UUID.MatchString(*out.RunID) || out.State == nil || !oneOf(*out.State, "planned", "passed", "failed") {
		return QualificationNext{}, fmt.Errorf("invalid qualification decision")
	}
	if (out.Action == "qualification" && *out.State != "planned") || (out.Action == "review" && *out.State != "passed") {
		return QualificationNext{}, fmt.Errorf("invalid qualification phase")
	}
	return out, nil
}
