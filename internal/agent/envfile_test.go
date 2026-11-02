package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyEnvFileFillsUnsetAndKeepsExisting(t *testing.T) {
	t.Setenv("HYPERMESH_ENVFILE_TEST_KEEP", "kept")
	t.Setenv("HYPERMESH_ENVFILE_TEST_FILL", "")
	t.Setenv("HYPERMESH_ENVFILE_TEST_PLAIN", "")
	path := filepath.Join(t.TempDir(), "agent.env")
	body := "" +
		"# comment\n" +
		"\n" +
		"; also a comment\n" +
		"export HYPERMESH_ENVFILE_TEST_FILL=\"from-file\"\n" +
		"HYPERMESH_ENVFILE_TEST_KEEP=replaced\n" +
		"not a line\n" +
		"HYPERMESH_ENVFILE_TEST_PLAIN=plain\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("HYPERMESH_ENVFILE_TEST_KEEP"); got != "kept" {
		t.Fatalf("existing env changed to %q", got)
	}
	if got := os.Getenv("HYPERMESH_ENVFILE_TEST_FILL"); got != "from-file" {
		t.Fatalf("quoted value %q", got)
	}
	if got := os.Getenv("HYPERMESH_ENVFILE_TEST_PLAIN"); got != "plain" {
		t.Fatalf("plain value %q", got)
	}
}

func TestApplyEnvFileMissingIsOK(t *testing.T) {
	if err := ApplyEnvFile(filepath.Join(t.TempDir(), "missing.env")); err != nil {
		t.Fatal(err)
	}
}

func TestFromEnvReadsWGIface(t *testing.T) {
	t.Setenv("HYPERMESH_WG_IFACE", "utun3")
	if got := FromEnv().WGIface; got != "utun3" {
		t.Fatalf("iface %q", got)
	}
}

func TestEnrollEnvKeysMatchLinux(t *testing.T) {
	for _, key := range []string{
		"HYPERMESH_CONTROL_PLANE_URL", "HYPERMESH_STATE_DIR", "HYPERMESH_LISTEN_PORT",
		"HYPERMESH_LEASE_START_READY_TIMEOUT_S", "HYPERMESH_COMPLETIONS_URL",
		"HYPERMESH_DISK_FLOOR_BYTES", "HYPERMESH_DISK_MARGIN_BYTES",
		"HYPERMESH_MODEL_CACHE_MAX_BYTES", "HYPERMESH_MODEL_CACHE_PENDING_TTL",
		"HYPERMESH_HOST_TYPE", "HYPERMESH_UPDATE_CHANNEL", "HYPERMESH_UPDATE_FINGERPRINTS",
		"HYPERMESH_UPDATE_EVERY", "HYPERMESH_UPDATE_ROLLBACK",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("HYPERMESH_TENANT_ID", "11111111-1111-1111-1111-111111111111")
	t.Setenv("HYPERMESH_ENROLL_TOKEN", "one-time")
	t.Setenv("HYPERMESH_HOST_NAME", "mac-mini")
	t.Setenv("HYPERMESH_WG_USERSPACE", "1")
	cfg := FromEnv()
	if cfg.ControlPlaneURL != defaultControlPlane {
		t.Fatalf("control plane %q", cfg.ControlPlaneURL)
	}
	if cfg.TenantID != "11111111-1111-1111-1111-111111111111" || cfg.EnrollToken != "one-time" {
		t.Fatalf("tenant %q token %q", cfg.TenantID, cfg.EnrollToken)
	}
	if cfg.HostName != "mac-mini" || cfg.StateDir != defaultStateDir || cfg.WGMode != "1" {
		t.Fatalf("name %q state %q wg %q", cfg.HostName, cfg.StateDir, cfg.WGMode)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
