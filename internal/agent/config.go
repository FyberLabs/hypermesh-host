package agent

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	defaultControlPlane = "http://127.0.0.1:8010"
	defaultStateDir     = "/var/lib/hypermesh"
	defaultHeartbeat    = 30 * time.Second
	defaultJobInterval  = 15 * time.Second
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
}

// FromEnv fills defaults and environment variables. Flags should overwrite
// after this returns.
func FromEnv() Config {
	return Config{
		ControlPlaneURL: envOr("HYPERMESH_CONTROL_PLANE_URL", defaultControlPlane),
		TenantID:        os.Getenv("HYPERMESH_TENANT_ID"),
		EnrollToken:     os.Getenv("HYPERMESH_ENROLL_TOKEN"),
		StateDir:        envOr("HYPERMESH_STATE_DIR", defaultStateDir),
		HeartbeatEvery:  defaultHeartbeat,
		JobEvery:        defaultJobInterval,
		DeviceSecret:    os.Getenv("HYPERMESH_DEVICE_SECRET"),
		ImageHash:       os.Getenv("HYPERMESH_IMAGE_HASH"),
	}
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
	return nil
}
