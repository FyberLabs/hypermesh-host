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
		"<string>/usr/local/libexec/hypermesh:/usr/bin:/bin:/usr/sbin:/sbin</string>",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("agent plist missing %s", needle)
		}
	}
	if strings.Contains(body, "/bin/sh") {
		t.Fatal("agent plist must run hypermesh-agent, which reads agent.env itself")
	}
	assertNoHomebrew(t, body)
	assertPathDirs(t, body, "/usr/local/libexec/hypermesh", "/usr/bin", "/bin", "/usr/sbin", "/sbin")
}

func TestWireGuardPlistOwnsUtun(t *testing.T) {
	body := string(mustRead(t, "com.fyberlabs.hypermesh-wg.plist"))
	for _, needle := range []string{
		"<string>com.fyberlabs.hypermesh-wg</string>",
		"<string>/usr/local/libexec/hypermesh/wireguard-go</string>",
		"<string>-f</string>",
		"<string>utun8</string>",
		"<key>KeepAlive</key>",
		"<string>/usr/bin:/bin:/usr/sbin:/sbin</string>",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("wg plist missing %s", needle)
		}
	}
	assertNoHomebrew(t, body)
	assertPathDirs(t, body, "/usr/bin", "/bin", "/usr/sbin", "/sbin")
}

func TestInstallCopiesRootOwnedBinaries(t *testing.T) {
	body := string(mustRead(t, "README.md"))
	const rootBin = "/usr/local/libexec/hypermesh"
	firstInstall := strings.Index(body, "sudo install ")
	if firstInstall < 0 {
		t.Fatal("README missing binary installation")
	}
	// Accept combined or separate commands, but require both destinations to
	// be prepared before the first binary copy on a fresh Mac.
	for _, dir := range []string{"/usr/local/bin", rootBin} {
		for _, command := range []string{"sudo mkdir -p", "sudo chown root:wheel", "sudo chmod 0755"} {
			found := false
			for _, line := range strings.Split(body[:firstInstall], "\n") {
				if !strings.HasPrefix(line, command+" ") {
					continue
				}
				for _, arg := range strings.Fields(strings.TrimPrefix(line, command)) {
					if arg == dir {
						found = true
					}
				}
			}
			if !found {
				t.Fatalf("README must prepare %s with %s before installing binaries", dir, command)
			}
		}
	}
	for _, needle := range []string{
		"sudo install -o root -g wheel -m 0755 \"$prefix/bin/wireguard-go\" " + rootBin + "/wireguard-go",
		"sudo install -o root -g wheel -m 0755 \"$prefix/bin/wg\" " + rootBin + "/wg",
		"sudo install -o root -g wheel -m 0755 \"$prefix/bin/llama-server\" " + rootBin + "/llama-server",
		"Do not point a plist or `PATH` at",
		"/opt/homebrew",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("README missing %s", needle)
		}
	}
	agent := string(mustRead(t, "com.fyberlabs.hypermesh-agent.plist"))
	wg := string(mustRead(t, "com.fyberlabs.hypermesh-wg.plist"))
	if strings.Contains(agent, "/opt/homebrew") || strings.Contains(wg, "/opt/homebrew") {
		t.Fatal("launchd files still reference /opt/homebrew")
	}
}

func assertNoHomebrew(t *testing.T, body string) {
	t.Helper()
	if strings.Contains(body, "homebrew") || strings.Contains(body, "/opt/") {
		t.Fatalf("launchd file references a user-writable prefix:\n%s", body)
	}
}

func assertPathDirs(t *testing.T, body string, want ...string) {
	t.Helper()
	const key = "<key>PATH</key>\n\t\t<string>"
	i := strings.Index(body, key)
	if i < 0 {
		t.Fatal("PATH missing")
	}
	rest := body[i+len(key):]
	end := strings.Index(rest, "</string>")
	if end < 0 {
		t.Fatal("PATH string not closed")
	}
	got := strings.Split(rest[:end], ":")
	if strings.Join(got, ":") != strings.Join(want, ":") {
		t.Fatalf("PATH %q want %q", got, want)
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
