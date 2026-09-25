package deploy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/FyberLabs/hypermesh-host/deploy/agx"
)

const (
	defaultControlPlane = "https://api.test.hyperme.sh"
	defaultStateDir     = "/var/lib/hypermesh"
	defaultEnvFile      = "/etc/hypermesh/agent.env"
	defaultUnitDir      = "/etc/systemd/system"
	agentUnitName       = "hypermesh-agent.service"
	wgUnitName          = "hypermesh-wg.service"
)

var uuidRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// RenderAgentEnv is the env file the existing enroll path reads.
// The device secret is not written here. enrolled means device.json
// already has a device id and secret, so a fresh enroll token is optional.
func RenderAgentEnv(controlPlane, tenant, token, stateDir, gguf string, enrolled bool) (string, error) {
	controlPlane = strings.TrimSpace(controlPlane)
	if controlPlane == "" {
		controlPlane = defaultControlPlane
	}
	tenant = strings.TrimSpace(tenant)
	if !uuidRE.MatchString(tenant) {
		return "", fmt.Errorf("tenant id must be a UUID")
	}
	token = strings.TrimSpace(token)
	if token == "" && !enrolled {
		return "", fmt.Errorf("enroll token is required")
	}
	if token != "" && (strings.Contains(token, "\n") || strings.Contains(token, "\x00")) {
		return "", fmt.Errorf("enroll token is not a single line")
	}
	gguf = strings.TrimSpace(gguf)
	if gguf == "" {
		return "", fmt.Errorf("GGUF path is required")
	}
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		stateDir = defaultStateDir
	}
	var b strings.Builder
	b.WriteString("# Written by hypermesh-deploy. Mode 0600.\n")
	b.WriteString("# The device secret stays in the state dir device.json after enroll.\n")
	fmt.Fprintf(&b, "HYPERMESH_CONTROL_PLANE_URL=%s\n", controlPlane)
	fmt.Fprintf(&b, "HYPERMESH_TENANT_ID=%s\n", tenant)
	if token != "" {
		fmt.Fprintf(&b, "HYPERMESH_ENROLL_TOKEN=%s\n", token)
	}
	fmt.Fprintf(&b, "HYPERMESH_STATE_DIR=%s\n", stateDir)
	fmt.Fprintf(&b, "HYPERMESH_GGUF_PATH=%s\n", gguf)
	b.WriteString("HYPERMESH_WG_USERSPACE=1\n")
	return b.String(), nil
}

func writeAgentEnv(root, envFile, body string) (string, error) {
	if strings.TrimSpace(envFile) == "" {
		envFile = defaultEnvFile
	}
	dest := underRoot(root, envFile)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := writeFileMode(dest, []byte(body), 0o600); err != nil {
		return "", err
	}
	return dest, nil
}

func writeUnits(root, unitDir string) error {
	if strings.TrimSpace(unitDir) == "" {
		unitDir = defaultUnitDir
	}
	dir := underRoot(root, unitDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeFileMode(filepath.Join(dir, agentUnitName), agx.AgentUnit, 0o644); err != nil {
		return err
	}
	return writeFileMode(filepath.Join(dir, wgUnitName), agx.WGUnit, 0o644)
}

func writeFileMode(path string, body []byte, mode os.FileMode) error {
	if err := os.WriteFile(path, body, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// LoadEnrollToken reads the one-time token from the environment, a
// file, or both when they match. It is not taken from argv.
func LoadEnrollToken(envValue, filePath string) (string, error) {
	envValue = strings.TrimSpace(envValue)
	filePath = strings.TrimSpace(filePath)
	var fromFile string
	if filePath != "" {
		b, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("enroll token file: %w", err)
		}
		fromFile = strings.TrimSpace(string(b))
		if strings.Contains(fromFile, "\n") || strings.Contains(fromFile, "\x00") {
			return "", fmt.Errorf("enroll token is not a single line")
		}
	}
	if envValue != "" && fromFile != "" && envValue != fromFile {
		return "", fmt.Errorf("enroll token from the environment and the file differ")
	}
	if envValue != "" {
		return envValue, nil
	}
	return fromFile, nil
}

// AlreadyEnrolled is true when device.json has both a device id and a secret.
func AlreadyEnrolled(root, stateDir string) bool {
	if strings.TrimSpace(stateDir) == "" {
		stateDir = defaultStateDir
	}
	b, err := os.ReadFile(filepath.Join(underRoot(root, stateDir), "device.json"))
	if err != nil {
		return false
	}
	var st struct {
		DeviceID     string `json:"device_id"`
		DeviceSecret string `json:"device_secret"`
	}
	if json.Unmarshal(b, &st) != nil {
		return false
	}
	return strings.TrimSpace(st.DeviceID) != "" && strings.TrimSpace(st.DeviceSecret) != ""
}

func underRoot(root, abs string) string {
	abs = filepath.Clean(abs)
	if root == "" || root == "/" {
		return abs
	}
	rel := strings.TrimPrefix(abs, string(os.PathSeparator))
	return filepath.Join(root, rel)
}
