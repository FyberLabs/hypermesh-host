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
	// PathUpdates is GET /api/v1/hypermesh/agent/updates/{channel}.
	// channel is "test" or "prod". This is the host proposal for the
	// plane (Panopticon builds the route). One constant, one path.
	PathUpdates         = Prefix + "/agent/updates/%s"
	HeaderTenantID      = "X-Tenant-ID"
	HeaderAuthorization = "Authorization"
)

// UpdateArtifact is one blob the plane will let this host read.
// URL is a short-lived (~10 min) read-only SAS URL. It is a credential.
// Do not log UpdateArtifact or UpdateManifest.
type UpdateArtifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	URL    string `json:"url"`
}

// UpdateManifest is the 200 body of GET /agent/updates/{channel}.
// 204 means that channel has no published build.
type UpdateManifest struct {
	Channel   string           `json:"channel"`
	Version   string           `json:"version"`
	Commit    string           `json:"commit"`
	ReleaseID string           `json:"release_id,omitempty"`
	Build     int              `json:"build"`
	Artifacts []UpdateArtifact `json:"artifacts"`
}

// EnrollRequest is POST /agent/enroll.
type EnrollRequest struct {
	HostName    string `json:"host_name,omitempty"`
	WGPublicKey string `json:"wg_public_key,omitempty"`
	EnrollToken string `json:"enroll_token"`
	ImageHash   string `json:"image_hash,omitempty"`
	// HostType is model or supervisor. Empty is not sent. The agent
	// always sends one of those two.
	HostType string `json:"host_type,omitempty"`
}

// EnrollResponse is the 200 body from enroll.
// DeviceSecret (hm_dev_…) is shown once. Persist it; never log it.
type EnrollResponse struct {
	DeviceID         string `json:"device_id"`
	CredentialPrefix string `json:"credential_prefix"`
	DeviceSecret     string `json:"device_secret"`
}

// HeartbeatRequest is POST /agent/heartbeat.
// Health is additive. Older planes ignore it (pydantic extra='ignore').
// Existing fields stay. A missing Health is the pre-health body.
type HeartbeatRequest struct {
	HostName        string      `json:"host_name,omitempty"`
	DeviceID        string      `json:"device_id"`
	ImageHash       string      `json:"image_hash,omitempty"`
	LoadedModelHash string      `json:"loaded_model_hash,omitempty"`
	UptimeS         int64       `json:"uptime_s"`
	ThermalC        *float64    `json:"thermal_c,omitempty"`
	SellState       SellState   `json:"sell_state,omitempty"`
	Health          *HostHealth `json:"health,omitempty"`
}

// Sync error classes for HostHealth.LastSyncErrorClass. Empty means none
// or a failure that is not one of these classes. Do not put response bodies here.
const (
	ErrClassHTTP5xx = "http_5xx"
	ErrClassHTTP4xx = "http_4xx"
	ErrClassNetwork = "network"
	ErrClassTimeout = "timeout"
	ErrClassDecode  = "decode"
)

// HealthErrorClasses is the fixed class set, including empty.
func HealthErrorClasses() []string {
	return []string{ErrClassHTTP5xx, ErrClassHTTP4xx, ErrClassNetwork, ErrClassTimeout, ErrClassDecode, ""}
}

// TunnelHealthStates is the tunnel_state enum.
func TunnelHealthStates() []string {
	return []string{"up", "handshaking", "down", "disabled"}
}

// HostHealth is optional host and tunnel health on the heartbeat.
// No WireGuard keys, tokens, device secrets, raw HTTP bodies, renter ids,
// or model prompts. Lease containers are a count only.
// disk_free_bytes is free bytes for non-root users (statfs Bavail*Bsize)
// on the filesystem that holds the configured GGUF path. It is omitted
// when that path cannot be resolved or statfs fails.
// Fields this box cannot compute are omitted. last_sync_error_class is
// an empty string when there is no class. last_sync_http_status is 0
// when there is no status. A failed collection must not fail the heartbeat.
type HostHealth struct {
	TunnelState         string `json:"tunnel_state,omitempty"`
	LastSyncAt          string `json:"last_sync_at,omitempty"`
	LastSyncErrorClass  string `json:"last_sync_error_class"`
	LastSyncHTTPStatus  int    `json:"last_sync_http_status"`
	HandshakeAgeS       *int   `json:"handshake_age_s,omitempty"`
	WGPID               *int   `json:"wg_pid,omitempty"`
	WGPort              *int   `json:"wg_port,omitempty"`
	LeaseContainerCount *int   `json:"lease_container_count,omitempty"`
	DiskFreeBytes       *int64 `json:"disk_free_bytes,omitempty"`
	// ModelCacheBytes is the sum of GGUF files in the model cache.
	// ModelCacheEvictableBytes excludes the loaded model and any model
	// backing an active lease. Both are omitted when the cache cannot
	// be read. Zero is sent when the cache is empty.
	ModelCacheHashes         []string `json:"model_cache_hashes,omitempty"`
	ModelCacheAvailableBytes *int64   `json:"model_cache_available_bytes,omitempty"`
	ModelCacheBytes          *int64   `json:"model_cache_bytes,omitempty"`
	ModelCacheEvictableBytes *int64   `json:"model_cache_evictable_bytes,omitempty"`
	BuildCommit              string   `json:"build_commit,omitempty"`
	// HostType is model or supervisor. The agent always sets it so a
	// supervisor is not offered as a normal device.
	HostType string `json:"host_type,omitempty"`
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
	KindCompactCert   JobKind = "compact_cert"
	KindLabPull       JobKind = "lab_pull"
	KindInstanceCheck JobKind = "instance_check"
	KindLeaseStart    JobKind = "lease_start"
	KindLeaseStop     JobKind = "lease_stop"
	KindLeaseCancel   JobKind = "lease_cancel"
	KindLeaseExpiry   JobKind = "lease_expiry"
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
	// SourceRef is the trusted fetch pin for lab_pull (repo@rev/file or
	// allowlisted HTTPS URL). Distinct from ImageDigest (content sha256).
	SourceRef string `json:"source_ref,omitempty"`
	// SizeBytes is the expected artifact size for lab_pull when the plane
	// sends it. Zero means this box learns the size from HTTP HEAD
	// Content-Length before downloading. Older planes omit the field.
	SizeBytes int64 `json:"size_bytes,omitempty"`
	// FileHash is the catalog-approved GGUF sha256 (sha256: plus 64 hex).
	// A non-empty value is checked against the file lease_start mounts.
	// Empty is accepted until the plane sends the field: a pending pin
	// is checked instead, and with no pin the start proceeds unverified.
	// Distinct from ImageDigest (OCI repo@digest).
	FileHash string `json:"file_hash,omitempty"`
	Runtime  string `json:"runtime,omitempty"`
	Backend  string `json:"backend,omitempty"`
	SuiteID  string `json:"suite_id,omitempty"`
	Until    string `json:"until,omitempty"`
}

// ScorecardReport is POST /agent/scorecard.
// DecodeTokS is omitted unless the probe returned completion_tokens.
// Sustained stays false for one sample. Prompt text is not a field.
type ScorecardReport struct {
	DeviceID    string   `json:"device_id"`
	CatalogID   string   `json:"catalog_id"`
	ImageHash   string   `json:"image_hash"`
	Runtime     string   `json:"runtime"`
	PromptPack  string   `json:"prompt_pack"`
	ImageDigest string   `json:"image_digest,omitempty"`
	SampleMs    int      `json:"sample_ms,omitempty"`
	DecodeTokS  float64  `json:"decode_tok_s,omitempty"`
	ThermalC    *float64 `json:"thermal_c,omitempty"`
	Sustained   bool     `json:"sustained"`
}

// JobResultRequest is POST /agent/jobs/{job_id}/result.
// Sell fields are omitempty so lease_start / lease_stop / path_b
// do not leak them. sell_pause must set SellState. passed=true
// with sell_state=n/a is invalid on the control plane.
// EgressOk / EgressReason are host observations for lab quarantine
// soaks; omit means unobserved (plane fails closed for admission).
type JobResultRequest struct {
	Passed       bool      `json:"passed"`
	ImageHash    string    `json:"image_hash,omitempty"`
	SellState    SellState `json:"sell_state,omitempty"`
	Until        string    `json:"until,omitempty"`
	DeviceID     string    `json:"device_id,omitempty"`
	EgressOk     *bool     `json:"egress_ok,omitempty"`
	EgressReason string    `json:"egress_reason,omitempty"`
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
