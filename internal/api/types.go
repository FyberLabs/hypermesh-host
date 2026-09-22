package api

import "time"

// Paths under the Hypermesh control-plane prefix.
const (
	Prefix                = "/api/v1/hypermesh"
	PathEnroll            = Prefix + "/agent/enroll"
	PathHeartbeat         = Prefix + "/agent/heartbeat"
	PathJobs              = Prefix + "/agent/jobs"
	PathJobResult         = Prefix + "/agent/jobs/%s/result"
	PathScorecard         = Prefix + "/agent/scorecard"
	PathTunnel            = Prefix + "/agent/tunnel"
	PathTunnelAck         = Prefix + "/agent/tunnel/ack"
	PathTunnelPeer        = Prefix + "/agent/tunnel/peer"
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
	DeviceID        string    `json:"device_id"`
	ImageHash       string    `json:"image_hash,omitempty"`
	LoadedModelHash string    `json:"loaded_model_hash,omitempty"`
	UptimeS         int64     `json:"uptime_s"`
	ThermalC        *float64  `json:"thermal_c,omitempty"`
	SellState       SellState `json:"sell_state,omitempty"`
}

// PathBState is the control-plane view of Path B.
type PathBState string

const (
	PathBNeverRun  PathBState = "never-run"
	PathBGreen     PathBState = "green"
	PathBSuspended PathBState = "suspended"
)

// SellState is Host-reported sell posture. Not Path B.
// Do not copy path_b_state into sell_state.
type SellState string

const (
	SellSelling  SellState = "selling"
	SellPaused   SellState = "paused"
	SellDraining SellState = "draining"
	SellOff      SellState = "off"
	SellNA       SellState = "n/a"
)

// HeartbeatResponse is the 200 body from heartbeat.
type HeartbeatResponse struct {
	DeviceID   string     `json:"device_id"`
	PathBState PathBState `json:"path_b_state"`
	SellState  SellState  `json:"sell_state,omitempty"`
	AcceptedAt time.Time  `json:"accepted_at"`
}

// JobKind is a pulled job kind.
type JobKind string

const (
	KindPathB         JobKind = "path_b"
	KindInstanceCheck JobKind = "instance_check"
	KindLeaseStart    JobKind = "lease_start"
	KindLeaseStop     JobKind = "lease_stop"
	KindSellPause     JobKind = "sell_pause"
)

// Job is GET /agent/jobs. One pending job or 204. No suite payload.
// Runtime stays "nvidia" for oci cuda-jetson. Backend is optional
// (cuda-jetson | cuda-sm87 | llama-cpp-cpu | llama-cpp-vulkan | hip-rocm).
// Omitted = cuda-jetson. Do not overload Runtime. disk-io is not in the enum.
// Default AGX image stub-fails hip-rocm / llama-cpp-cpu / llama-cpp-vulkan.
// hip-rocm is a real path only on a -tags rocm image; llama-cpp-cpu is
// Metal only on darwin. llama-cpp-metal is not in the enum.
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
	Until       string  `json:"until,omitempty"`
}

// ScorecardReport is POST /agent/scorecard.
// DecodeTokS is omitted unless the probe returned completion_tokens.
// Sustained stays false for one sample. Prompt text is not a field.
type ScorecardReport struct {
	DeviceID    string  `json:"device_id"`
	CatalogID   string  `json:"catalog_id"`
	ImageHash   string  `json:"image_hash"`
	Runtime     string  `json:"runtime"`
	PromptPack  string  `json:"prompt_pack"`
	ImageDigest string  `json:"image_digest,omitempty"`
	SampleMs    int     `json:"sample_ms,omitempty"`
	DecodeTokS  float64 `json:"decode_tok_s,omitempty"`
	Sustained   bool    `json:"sustained"`
}

// JobResultRequest is POST /agent/jobs/{job_id}/result.
// Sell fields are omitempty so lease_start / lease_stop / path_b
// do not leak them. sell_pause must set SellState. passed=true
// with sell_state=n/a is invalid on the control plane.
type JobResultRequest struct {
	Passed    bool      `json:"passed"`
	ImageHash string    `json:"image_hash,omitempty"`
	SellState SellState `json:"sell_state,omitempty"`
	Until     string    `json:"until,omitempty"`
	DeviceID  string    `json:"device_id,omitempty"`
}

// TunnelStatus is the control-plane session state.
type TunnelStatus string

const (
	TunnelAllocated TunnelStatus = "allocated"
	TunnelRevoked   TunnelStatus = "revoked"
)

// TunnelSession is GET /agent/tunnel. The host private key is never on this GET.
type TunnelSession struct {
	TunnelID        string       `json:"tunnel_id"`
	LeaseID         string       `json:"lease_id"`
	Status          TunnelStatus `json:"status"`
	Endpoint        string       `json:"endpoint"`
	ServerPublicKey string       `json:"server_public_key"`
	PeerPublicKey   string       `json:"peer_public_key,omitempty"`
	AllowedIPs      string       `json:"allowed_ips"`
	ClientAddress   string       `json:"client_address"`
}

// TunnelPeerRequest is POST /agent/tunnel/peer. Public key only.
type TunnelPeerRequest struct {
	PeerPublicKey string `json:"peer_public_key"`
}

// TunnelAckRequest is POST /agent/tunnel/ack after wg-quick (or equivalent).
type TunnelAckRequest struct {
	Applied bool `json:"applied"`
}

// EnrollTokenResponse is POST /devices/{device_id}/enroll-token (platform).
type EnrollTokenResponse struct {
	EnrollToken string `json:"enroll_token"`
}
