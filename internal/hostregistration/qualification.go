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
type QualificationReport struct {
	Samples     []CertificationEvidence `json:"samples"`
	FailureCode string                  `json:"failure_code"`
	Software    QualificationSoftware   `json:"software"`
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
	return out, nil
}
func (c *Client) PlanQualification(ctx context.Context, r Request, device, secret, id string) (QualificationRun, error) {
	return c.qualification(ctx, r, device, secret, id, nil, nil)
}
func (c *Client) ReportQualification(ctx context.Context, r Request, device, secret string, run QualificationRun, report QualificationReport) (QualificationRun, error) {
	return c.qualification(ctx, r, device, secret, run.RunID, &run, &report)
}
