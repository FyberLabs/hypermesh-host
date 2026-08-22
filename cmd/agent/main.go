// Command agent is the Hypermesh host agent (AGX Orin first).
//
// It is not the control plane. It speaks the four Panopticon agent
// routes on :8010 and does not invent tunnel or lease-start HTTP.
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/FyberLabs/hypermesh-host/internal/agent"
	"github.com/FyberLabs/hypermesh-host/internal/logsafe"
)

func main() {
	log := logsafe.New()
	cfg := agent.FromEnv()

	flag.StringVar(&cfg.ControlPlaneURL, "control-plane-url", cfg.ControlPlaneURL, "Panopticon Hypermesh base URL (HYPERMESH_CONTROL_PLANE_URL)")
	flag.StringVar(&cfg.TenantID, "tenant-id", cfg.TenantID, "marketplace tenant UUID for X-Tenant-ID (HYPERMESH_TENANT_ID)")
	flag.StringVar(&cfg.EnrollToken, "enroll-token", cfg.EnrollToken, "one-time enroll token if no device_id on disk (HYPERMESH_ENROLL_TOKEN)")
	flag.StringVar(&cfg.StateDir, "state-dir", cfg.StateDir, "directory for device.json (HYPERMESH_STATE_DIR)")
	flag.DurationVar(&cfg.HeartbeatEvery, "heartbeat-interval", cfg.HeartbeatEvery, "heartbeat interval")
	flag.DurationVar(&cfg.JobEvery, "job-interval", cfg.JobEvery, "job pull interval")
	flag.StringVar(&cfg.DeviceSecretHeader, "device-secret-header", cfg.DeviceSecretHeader, "optional future device-secret header name; empty until the control plane documents one")
	flag.StringVar(&cfg.ImageHash, "image-hash", cfg.ImageHash, "optional host image hash override (HYPERMESH_IMAGE_HASH)")
	flag.Parse()

	a, err := agent.New(cfg, log)
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("hypermesh host agent starting",
		"control_plane", cfg.ControlPlaneURL,
		"state_dir", cfg.StateDir,
	)
	if err := a.Run(ctx); err != nil && err != context.Canceled && err != context.DeadlineExceeded {
		log.Error("agent stopped", "err", err)
		os.Exit(1)
	}
	log.Info("agent stopped")
}
