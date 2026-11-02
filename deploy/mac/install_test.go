package mac_test

import (
	"os"
	"strings"
	"testing"
)

func TestAgentPlistRunsBinary(t *testing.T) {
	body := string(mustRead(t, "com.fyberlabs.hypermesh-agent.plist"))
	for _, needle := range []string{
		"<string>com.fyberlabs.hypermesh-agent</string>",
		"<string>/usr/local/bin/hypermesh-agent</string>",
		"<key>WorkingDirectory</key>",
		"<string>/var/lib/hypermesh</string>",
		"<key>KeepAlive</key>",
		"<true/>",
		"/opt/homebrew/bin",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("agent plist missing %s", needle)
		}
	}
	if strings.Contains(body, "/bin/sh") {
		t.Fatal("agent plist must run hypermesh-agent, which reads agent.env itself")
	}
}

func TestWireGuardPlistOwnsUtun(t *testing.T) {
	body := string(mustRead(t, "com.fyberlabs.hypermesh-wg.plist"))
	for _, needle := range []string{
		"<string>com.fyberlabs.hypermesh-wg</string>",
		"<string>/opt/homebrew/bin/wireguard-go</string>",
		"<string>-f</string>",
		"<string>utun8</string>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("wg plist missing %s", needle)
		}
	}
}

func TestEnvExampleKeepsLinuxPathsAndDisablesUpdate(t *testing.T) {
	body := string(mustRead(t, "agent.env.example"))
	for _, needle := range []string{
		"HYPERMESH_CONTROL_PLANE_URL=https://api.test.hyperme.sh",
		"HYPERMESH_TENANT_ID=",
		"HYPERMESH_ENROLL_TOKEN=",
		"HYPERMESH_HOST_NAME=",
		"HYPERMESH_STATE_DIR=/var/lib/hypermesh",
		"HYPERMESH_GGUF_PATH=/var/lib/hypermesh/models/model.gguf",
		"HYPERMESH_WG_USERSPACE=1",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("env example missing %s", needle)
		}
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "HYPERMESH_UPDATE_CHANNEL=") {
			t.Fatalf("darwin example must not set an update channel: %s", line)
		}
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
