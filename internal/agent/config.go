package agent

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	defaultControlPlane   = "https://api.test.hyperme.sh"
	defaultStateDir       = "/var/lib/hypermesh"
	defaultHeartbeat      = 30 * time.Second
	defaultJobInterval    = 15 * time.Second
	defaultListenPort     = "8080"
	defaultCompletionsURL = "http://127.0.0.1:8080/v1/chat/completions"
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
		ListenPort:      envOr("HYPERMESH_LISTEN_PORT", defaultListenPort),
		CompletionsURL:  os.Getenv("HYPERMESH_COMPLETIONS_URL"),
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
	if p := strings.TrimSpace(c.ListenPort); p != "" && !validPort(p) {
		return fmt.Errorf("hypermesh: listen port must be 1-65535")
	}
	if u := strings.TrimSpace(c.CompletionsURL); u != "" {
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("hypermesh: completions URL must be http(s)")
		}
	}
	return nil
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
