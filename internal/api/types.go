package api

import "time"

// Paths under the Hypermesh control-plane prefix.
const (
	Prefix         = "/api/v1/hypermesh"
	PathEnroll     = Prefix + "/agent/enroll"
	PathHeartbeat  = Prefix + "/agent/heartbeat"
	PathJobs       = Prefix + "/agent/jobs"
	PathJobResult  = Prefix + "/agent/jobs/%s/result"
	HeaderTenantID = "X-Tenant-ID"
)

// EnrollRequest is POST /agent/enroll.
type EnrollRequest struct {
	EnrollToken string `json:"enroll_token"`
	ImageHash   string `json:"image_hash,omitempty"`
}

// EnrollResponse is the 200 body from enroll.
//
// The control plane currently returns credential_prefix only. It does not
// issue a device secret and does not check Bearer on agent routes. Do not
// treat CredentialPrefix as a credential to send.
type EnrollResponse struct {
	DeviceID         string `json:"device_id"`
	CredentialPrefix string `json:"credential_prefix"`
}

// HeartbeatRequest is POST /agent/heartbeat.
type HeartbeatRequest struct {
	DeviceID        string   `json:"device_id"`
	ImageHash       string   `json:"image_hash,omitempty"`
	LoadedModelHash string   `json:"loaded_model_hash,omitempty"`
	UptimeS         int64    `json:"uptime_s"`
	ThermalC        *float64 `json:"thermal_c,omitempty"`
}

// PathBState is the control-plane view of Path B.
type PathBState string

const (
	PathBNeverRun  PathBState = "never-run"
	PathBGreen     PathBState = "green"
	PathBSuspended PathBState = "suspended"
)

// HeartbeatResponse is the 200 body from heartbeat.
// The control plane currently persists last_heartbeat_at and last_image_hash.
type HeartbeatResponse struct {
	DeviceID   string     `json:"device_id"`
	PathBState PathBState `json:"path_b_state"`
	AcceptedAt time.Time  `json:"accepted_at"`
}

// CertJobKind is the job kind scheduled by the platform.
type CertJobKind string

const (
	KindPathB         CertJobKind = "path_b"
	KindInstanceCheck CertJobKind = "instance_check"
)

// CertJobStatus is the job status.
type CertJobStatus string

const (
	StatusPending CertJobStatus = "pending"
	StatusPassed  CertJobStatus = "passed"
	StatusFailed  CertJobStatus = "failed"
)

// CertJob is a Path B or instance-check job. The control plane does not
// send a suite payload or commands. suite_id defaults to stub-suite-v1.
type CertJob struct {
	ID          string        `json:"id"`
	TenantID    string        `json:"tenant_id"`
	DeviceID    string        `json:"device_id"`
	SuiteID     string        `json:"suite_id"`
	Kind        CertJobKind   `json:"kind"`
	Status      CertJobStatus `json:"status"`
	CreatedAt   time.Time     `json:"created_at"`
	CompletedAt *time.Time    `json:"completed_at"`
	ImageHash   string        `json:"image_hash"`
}

// JobResultRequest is POST /agent/jobs/{job_id}/result.
type JobResultRequest struct {
	Passed    bool   `json:"passed"`
	ImageHash string `json:"image_hash,omitempty"`
}
