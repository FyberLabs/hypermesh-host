package api

import "time"

// Paths under the Hypermesh control-plane prefix.
const (
	Prefix                = "/api/v1/hypermesh"
	PathEnroll            = Prefix + "/agent/enroll"
	PathHeartbeat         = Prefix + "/agent/heartbeat"
	PathJobs              = Prefix + "/agent/jobs"
	PathJobResult         = Prefix + "/agent/jobs/%s/result"
	PathTunnel            = Prefix + "/agent/tunnel"
	PathTunnelAck         = Prefix + "/agent/tunnel/ack"
	PathDeviceEnrollToken = Prefix + "/devices/%s/enroll-token"
	HeaderTenantID        = "X-Tenant-ID"
	HeaderAuthorization   = "Authorization"
)

// EnrollRequest is POST /agent/enroll.
type EnrollRequest struct {
	EnrollToken string `json:"enroll_token"`
	ImageHash   string `json:"image_hash,omitempty"`
}

// EnrollResponse is the 200 body from enroll.
// DeviceSecret (hm_dev_…) is shown once. Persist it; never log it.
type EnrollResponse struct {
	DeviceID         string `json:"device_id"`
	CredentialPrefix string `json:"credential_prefix"`
	DeviceSecret     string `json:"device_secret"`
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
type HeartbeatResponse struct {
	DeviceID   string     `json:"device_id"`
	PathBState PathBState `json:"path_b_state"`
	AcceptedAt time.Time  `json:"accepted_at"`
}

// JobKind is a pulled job kind.
type JobKind string

const (
	KindPathB         JobKind = "path_b"
	KindInstanceCheck JobKind = "instance_check"
	KindLeaseStart    JobKind = "lease_start"
	KindLeaseStop     JobKind = "lease_stop"
)

// Job is GET /agent/jobs. One pending job or 204. No suite payload.
// Runtime stays "nvidia" for oci cuda-jetson. Backend is optional
// (cuda-jetson | cuda-sm87 | llama-cpp-cpu | llama-cpp-vulkan | hip-rocm).
// Omitted = cuda-jetson. Do not overload Runtime. disk-io is not in the enum.
type Job struct {
	JobID       string  `json:"job_id"`
	Kind        JobKind `json:"kind"`
	DeviceID    string  `json:"device_id"`
	LeaseID     string  `json:"lease_id,omitempty"`
	CatalogID   string  `json:"catalog_id,omitempty"`
	ImageDigest string  `json:"image_digest,omitempty"`
	Runtime     string  `json:"runtime,omitempty"`
	Backend     string  `json:"backend,omitempty"`
	SuiteID     string  `json:"suite_id,omitempty"`
}

// JobResultRequest is POST /agent/jobs/{job_id}/result.
type JobResultRequest struct {
	Passed    bool   `json:"passed"`
	ImageHash string `json:"image_hash,omitempty"`
}

// TunnelStatus is the control-plane session state.
type TunnelStatus string

const (
	TunnelAllocated TunnelStatus = "allocated"
	TunnelRevoked   TunnelStatus = "revoked"
)

// TunnelSession is GET /agent/tunnel.
// PeerPrivateKey is present only while status=allocated, and only on this GET.
// Never log it.
type TunnelSession struct {
	TunnelID        string       `json:"tunnel_id"`
	LeaseID         string       `json:"lease_id"`
	Status          TunnelStatus `json:"status"`
	Endpoint        string       `json:"endpoint"`
	ServerPublicKey string       `json:"server_public_key"`
	PeerPrivateKey  string       `json:"peer_private_key,omitempty"`
	AllowedIPs      string       `json:"allowed_ips"`
	ClientAddress   string       `json:"client_address"`
}

// TunnelAckRequest is POST /agent/tunnel/ack after wg-quick (or equivalent).
type TunnelAckRequest struct {
	Applied bool `json:"applied"`
}

// EnrollTokenResponse is POST /devices/{device_id}/enroll-token (platform).
type EnrollTokenResponse struct {
	EnrollToken string `json:"enroll_token"`
}
