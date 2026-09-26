package agent

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/pull"
	"github.com/FyberLabs/hypermesh-host/internal/runtime"
	"github.com/FyberLabs/hypermesh-host/internal/tunnel"
	"github.com/FyberLabs/hypermesh-host/internal/update"
)

const (
	defaultControlPlane   = "https://api.test.hyperme.sh"
	defaultStateDir       = "/var/lib/hypermesh"
	defaultHeartbeat      = 30 * time.Second
	defaultJobInterval    = 15 * time.Second
	defaultListenPort     = "8080"
	defaultCompletionsURL = "http://127.0.0.1:8080/v1/chat/completions"
	defaultResultTries    = 3
	defaultResultRetry    = 200 * time.Millisecond
	// DefaultModelDir is persistent variable data for weights (FHS
	// /var/lib). It is not the state dir: that directory is 0700 because
	// it holds device.json. Weights are large and not secret, so they
	// live in a sibling models directory (0755, root:root).
	// An empty HYPERMESH_GGUF_PATH used to download into the process
	// cwd, which systemd sets to /, producing /lab-pull-*.gguf.
	DefaultModelDir = "/var/lib/hypermesh/models"
	// DefaultModelPath is the weights file lease_start bind-mounts when
	// HYPERMESH_GGUF_PATH is unset or empty. An explicit path wins.
	DefaultModelPath = DefaultModelDir + "/model.gguf"
)

var uuidRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Config is env/flag configuration for the host agent.
type Config struct {
	ControlPlaneURL string
	TenantID        string
	EnrollToken     string
	StateDir        string
	HeartbeatEvery  time.Duration
	JobEvery        time.Duration
	// DeviceSecret is an optional recovery override. Prefer the value
	// persisted at 0600 after enroll. Never log it.
	DeviceSecret string
	ImageHash    string
	// ListenPort is the host port published as -p <port>:8080 on lease_start.
	// Empty uses 8080.
	ListenPort string
	// CompletionsURL is the local OpenAI-shaped endpoint thin-v1 probes.
	// Empty uses http://127.0.0.1:<ListenPort>/v1/chat/completions.
	CompletionsURL string
	// GGUFPath is the host file bind-mounted at /models/model.gguf:ro.
	// FromEnv uses DefaultModelPath when HYPERMESH_GGUF_PATH is unset
	// or empty. An explicit path is kept as-is. A Config built in tests
	// with an empty GGUFPath still means "no path" so lease_start refuses
	// rather than inventing a file.
	GGUFPath string
	// DiskFloor is HYPERMESH_DISK_FLOOR_BYTES. Empty means 2 GiB must
	// remain free after a model download. "0" leaves no floor.
	DiskFloor string
	// DiskMargin is HYPERMESH_DISK_MARGIN_BYTES. Empty means 64 MiB on
	// top of the object size. "0" adds no margin.
	DiskMargin string
	// ModelCacheMax is HYPERMESH_MODEL_CACHE_MAX_BYTES. Empty means
	// the filesystem size minus DiskFloor. "0" is an empty budget.
	ModelCacheMax string
	// PendingTTL is HYPERMESH_MODEL_CACHE_PENDING_TTL. Empty means
	// 30m. A pending catalog pin is not evictable until this duration
	// passes, a lease_start consumes it, or a stop/cancel/expiry clears it.
	PendingTTL string
	// LlamaJinja, LlamaCtxSize, and LlamaTimeout are the lease container's
	// llama-server settings (LLAMA_ARG_JINJA, LLAMA_ARG_CTX_SIZE,
	// LLAMA_ARG_TIMEOUT). Empty uses the runtime defaults (1, 16384, 1800).
	LlamaJinja   string
	LlamaCtxSize string
	LlamaTimeout string
	// WGMode selects the WireGuard backend (HYPERMESH_WG_USERSPACE).
	// "auto" uses the kernel module when it is loaded or loadable, and
	// otherwise configures the interface owned by hypermesh-wg.service.
	// "1" / "userspace" forces that unit. "0" / "kernel" forces wg-quick.
	// Empty stays on wg-quick (tests). The agent never spawns wireguard-go.
	WGMode string
	// MetricsAddr is an explicit /metrics bind (HYPERMESH_METRICS_ADDR).
	// Empty derives the hypermesh interface address, or 127.0.0.1:9464
	// when the tunnel is down. Only loopback and that WireGuard address
	// are accepted. Public and unspecified addresses are refused.
	MetricsAddr string
	// MetricsAllowNonLoopback is retained so an old environment file
	// still parses. It does not permit a public, unspecified, or
	// non-WireGuard bind.
	MetricsAllowNonLoopback bool
	// HostType is "model" (default) or "supervisor". A supervisor host
	// enrolls and heartbeats and refuses lease_start for a weight.
	HostType string
	// WGKeyDir is the host WireGuard private key directory.
	// Empty uses /etc/wireguard. The agent still owns the key.
	WGKeyDir string
	// UpdateChannel is HYPERMESH_UPDATE_CHANNEL: "test", "prod", or empty.
	// Empty does not check for updates. test tracks builds of main.
	// prod tracks tagged semver releases. The bytes come from the
	// control plane, not from GitHub.
	UpdateChannel string
	// UpdateEvery is how often to check the channel. Zero uses one hour.
	UpdateEvery time.Duration
	// UpdateRollback is how long a new binary has to heartbeat before
	// the previous binary is restored. Zero uses two minutes.
	UpdateRollback time.Duration
	// UpdatePubKey is the file of trusted armored public keys used to
	// verify checksums. Several keys may be concatenated for rotation.
	// Empty uses /etc/hypermesh/update-pubkey.asc.
	UpdatePubKey string
	// UpdateFingerprints is HYPERMESH_UPDATE_FINGERPRINTS. A signature
	// is accepted only when its fingerprint is in this list. A key that
	// is only present in UpdatePubKey is not enough.
	UpdateFingerprints []string
	// UpdateFingerprintsInvalid is set when the env value is not a list
	// of 40 or 64 hex fingerprints.
	UpdateFingerprintsInvalid bool
	// UpdateBinary is the path replaced on install. Empty uses the
	// running executable.
	UpdateBinary string
}

// FromEnv fills defaults and environment variables. Flags should overwrite
// after this returns.
func FromEnv() Config {
	fps, fpErr := update.ParseFingerprints(os.Getenv("HYPERMESH_UPDATE_FINGERPRINTS"))
	return Config{
		ControlPlaneURL:           envOr("HYPERMESH_CONTROL_PLANE_URL", defaultControlPlane),
		TenantID:                  os.Getenv("HYPERMESH_TENANT_ID"),
		EnrollToken:               os.Getenv("HYPERMESH_ENROLL_TOKEN"),
		StateDir:                  envOr("HYPERMESH_STATE_DIR", defaultStateDir),
		HeartbeatEvery:            defaultHeartbeat,
		JobEvery:                  defaultJobInterval,
		DeviceSecret:              os.Getenv("HYPERMESH_DEVICE_SECRET"),
		ImageHash:                 os.Getenv("HYPERMESH_IMAGE_HASH"),
		ListenPort:                envOr("HYPERMESH_LISTEN_PORT", defaultListenPort),
		CompletionsURL:            os.Getenv("HYPERMESH_COMPLETIONS_URL"),
		GGUFPath:                  envOr("HYPERMESH_GGUF_PATH", DefaultModelPath),
		DiskFloor:                 strings.TrimSpace(os.Getenv("HYPERMESH_DISK_FLOOR_BYTES")),
		DiskMargin:                strings.TrimSpace(os.Getenv("HYPERMESH_DISK_MARGIN_BYTES")),
		ModelCacheMax:             strings.TrimSpace(os.Getenv("HYPERMESH_MODEL_CACHE_MAX_BYTES")),
		PendingTTL:                strings.TrimSpace(os.Getenv("HYPERMESH_MODEL_CACHE_PENDING_TTL")),
		LlamaJinja:                envOr("HYPERMESH_LLAMA_JINJA", runtime.DefaultLlamaJinja),
		LlamaCtxSize:              envOr("HYPERMESH_LLAMA_CTX_SIZE", runtime.DefaultLlamaCtxSize),
		LlamaTimeout:              envOr("HYPERMESH_LLAMA_TIMEOUT", runtime.DefaultLlamaTimeout),
		WGMode:                    wgModeFromEnv(),
		MetricsAddr:               strings.TrimSpace(os.Getenv("HYPERMESH_METRICS_ADDR")),
		MetricsAllowNonLoopback:   envBool("HYPERMESH_METRICS_ALLOW_NON_LOOPBACK"),
		HostType:                  strings.TrimSpace(os.Getenv("HYPERMESH_HOST_TYPE")),
		WGKeyDir:                  strings.TrimSpace(os.Getenv("HYPERMESH_WG_KEY_DIR")),
		UpdateChannel:             strings.TrimSpace(os.Getenv("HYPERMESH_UPDATE_CHANNEL")),
		UpdateEvery:               durationEnv("HYPERMESH_UPDATE_EVERY", time.Hour),
		UpdateRollback:            durationEnv("HYPERMESH_UPDATE_ROLLBACK", 2*time.Minute),
		UpdatePubKey:              envOr("HYPERMESH_UPDATE_PUBKEY", "/etc/hypermesh/update-pubkey.asc"),
		UpdateFingerprints:        fps,
		UpdateFingerprintsInvalid: fpErr != nil,
		UpdateBinary:              strings.TrimSpace(os.Getenv("HYPERMESH_UPDATE_BINARY")),
	}
}

func durationEnv(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return -1
	}
	return d
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func wgModeFromEnv() string {
	v := strings.TrimSpace(os.Getenv("HYPERMESH_WG_USERSPACE"))
	if v == "" {
		return "auto"
	}
	return v
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// Validate checks values we can check without logging secrets.
func (c Config) Validate() error {
	if strings.TrimSpace(c.ControlPlaneURL) == "" {
		return fmt.Errorf("hypermesh: control-plane URL is required")
	}
	if !uuidRE.MatchString(strings.TrimSpace(c.TenantID)) {
		return fmt.Errorf("hypermesh: tenant id must be a UUID (X-Tenant-ID)")
	}
	if strings.TrimSpace(c.StateDir) == "" {
		return fmt.Errorf("hypermesh: state dir is required")
	}
	if c.HeartbeatEvery <= 0 {
		return fmt.Errorf("hypermesh: heartbeat interval must be > 0")
	}
	if c.JobEvery <= 0 {
		return fmt.Errorf("hypermesh: job interval must be > 0")
	}
	if p := strings.TrimSpace(c.ListenPort); p != "" && !validPort(p) {
		return fmt.Errorf("hypermesh: listen port must be 1-65535")
	}
	if u := strings.TrimSpace(c.CompletionsURL); u != "" {
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("hypermesh: completions URL must be http(s)")
		}
	}
	if !tunnel.ValidWGMode(c.WGMode) {
		return fmt.Errorf("hypermesh: HYPERMESH_WG_USERSPACE must be auto, 1, or 0")
	}
	switch strings.ToLower(strings.TrimSpace(c.HostType)) {
	case "", HostTypeModel, HostTypeSupervisor:
	default:
		return fmt.Errorf("hypermesh: host type must be model or supervisor")
	}
	if _, err := c.DiskFloorBytes(); err != nil {
		return err
	}
	if _, err := c.DiskMarginBytes(); err != nil {
		return err
	}
	if raw := strings.TrimSpace(c.ModelCacheMax); raw != "" {
		if _, err := parseByteCount("HYPERMESH_MODEL_CACHE_MAX_BYTES", raw, 0); err != nil {
			return err
		}
	}
	if _, err := c.PendingModelTTL(); err != nil {
		return err
	}
	if _, err := update.ParseChannel(c.UpdateChannel); err != nil {
		return err
	}
	if c.UpdateFingerprintsInvalid {
		return fmt.Errorf("hypermesh: HYPERMESH_UPDATE_FINGERPRINTS must be 40 or 64 hex characters")
	}
	if c.UpdateEvery < 0 || c.UpdateRollback < 0 {
		return fmt.Errorf("hypermesh: update interval must be a duration")
	}
	return nil
}

// defaultPendingTTL is how long a catalog pin survives when the plane
// never starts, stops, cancels, or expires the lease.
const defaultPendingTTL = 30 * time.Minute

// PendingModelTTL is the pending-pin lifetime. Empty config means 30m.
func (c Config) PendingModelTTL() (time.Duration, error) {
	raw := strings.TrimSpace(c.PendingTTL)
	if raw == "" {
		return defaultPendingTTL, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("hypermesh: HYPERMESH_MODEL_CACHE_PENDING_TTL must be a positive duration")
	}
	return d, nil
}

// DiskFloorBytes is the free-space floor. Empty config means 2 GiB.
func (c Config) DiskFloorBytes() (uint64, error) {
	return parseByteCount("HYPERMESH_DISK_FLOOR_BYTES", c.DiskFloor, pull.DefaultFloorBytes)
}

// DiskMarginBytes is extra space required beyond the object size.
// Empty config means 64 MiB.
func (c Config) DiskMarginBytes() (uint64, error) {
	return parseByteCount("HYPERMESH_DISK_MARGIN_BYTES", c.DiskMargin, pull.DefaultMarginBytes)
}

func parseByteCount(name, raw string, def uint64) (uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("hypermesh: %s must be an integer byte count", name)
	}
	return n, nil
}

// SupervisorHost is a non-model host. It enrolls and heartbeats.
// lease_start for a weight is refused.
func (c Config) SupervisorHost() bool {
	return strings.EqualFold(strings.TrimSpace(c.HostType), HostTypeSupervisor)
}

// ReportedHostType is the value sent on enroll and heartbeat.
// Empty means model.
func (c Config) ReportedHostType() string {
	if c.SupervisorHost() {
		return HostTypeSupervisor
	}
	return HostTypeModel
}

func validPort(s string) bool {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
		n = n*10 + int(r-'0')
		if n > 65535 {
			return false
		}
	}
	return n >= 1 && n <= 65535
}

// CompletionsEndpoint is the URL thin-v1 POSTs. Never log the probe body.
func (c Config) CompletionsEndpoint() string {
	if u := strings.TrimSpace(c.CompletionsURL); u != "" {
		return u
	}
	p := strings.TrimSpace(c.ListenPort)
	if p == "" {
		p = defaultListenPort
	}
	if p == defaultListenPort {
		return defaultCompletionsURL
	}
	return "http://127.0.0.1:" + p + "/v1/chat/completions"
}

// PublishPort is the host side of -p <port>:8080.
func (c Config) PublishPort() string {
	if p := strings.TrimSpace(c.ListenPort); p != "" {
		return p
	}
	return defaultListenPort
}

// GGUFHostPath is the host weights file for docker -v ...:/models/model.gguf:ro.
// Empty means lease_start must refuse. FromEnv does not return empty:
// an unset HYPERMESH_GGUF_PATH is DefaultModelPath. An explicit value,
// including a cottage path, is returned unchanged.
func (c Config) GGUFHostPath() string {
	return strings.TrimSpace(c.GGUFPath)
}
