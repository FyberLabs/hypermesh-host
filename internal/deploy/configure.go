package deploy

import (
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
// The device secret is not written here.
func RenderAgentEnv(controlPlane, tenant, token, stateDir, gguf string) (string, error) {
	controlPlane = strings.TrimSpace(controlPlane)
	if controlPlane == "" {
		controlPlane = defaultControlPlane
	}
	tenant = strings.TrimSpace(tenant)
	if !uuidRE.MatchString(tenant) {
		return "", fmt.Errorf("tenant id must be a UUID")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf("enroll token is required")
	}
	if strings.Contains(token, "\n") || strings.Contains(token, "\x00") {
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
	fmt.Fprintf(&b, "HYPERMESH_ENROLL_TOKEN=%s\n", token)
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

func underRoot(root, abs string) string {
	abs = filepath.Clean(abs)
	if root == "" || root == "/" {
		return abs
	}
	rel := strings.TrimPrefix(abs, string(os.PathSeparator))
	return filepath.Join(root, rel)
}
