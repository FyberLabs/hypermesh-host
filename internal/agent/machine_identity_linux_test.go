//go:build linux

package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRelativeStateDirStillReturnsBearerCredential(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	if err := os.Mkdir("state", 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{ControlPlaneURL: "https://cp.example", TenantID: "t1", StateDir: "./state"}
	if path := machineIdentityPath(cfg); !filepath.IsAbs(path) {
		t.Fatalf("path %q is not absolute", path)
	}
	c := machineAPIClient(cfg)
	c.DeviceID = "device-1"
	c.DeviceSecret = "bootstrap-secret"
	token, err := c.Credential(context.Background())
	if err != nil || token != "bootstrap-secret" {
		t.Fatalf("credential = %q, %v", token, err)
	}
}
