// Command agent is the Hypermesh host agent (AGX Orin first).
//
// It is not the control plane. It speaks the locked Panopticon agent
// routes under /api/v1/hypermesh (enroll, heartbeat, jobs, tunnel get/ack).
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

	flag.StringVar(&cfg.ControlPlaneURL, "control-plane-url", cfg.ControlPlaneURL, "Hypermesh control-plane base URL (HYPERMESH_CONTROL_PLANE_URL)")
	flag.StringVar(&cfg.TenantID, "tenant-id", cfg.TenantID, "marketplace tenant UUID for X-Tenant-ID (HYPERMESH_TENANT_ID)")
	flag.StringVar(&cfg.EnrollToken, "enroll-token", cfg.EnrollToken, "one-time enroll token if no device_id+secret on disk (HYPERMESH_ENROLL_TOKEN)")
	flag.StringVar(&cfg.StateDir, "state-dir", cfg.StateDir, "directory for device.json (HYPERMESH_STATE_DIR)")
	flag.DurationVar(&cfg.HeartbeatEvery, "heartbeat-interval", cfg.HeartbeatEvery, "heartbeat interval")
	flag.DurationVar(&cfg.JobEvery, "job-interval", cfg.JobEvery, "job pull and tunnel sync interval")
	flag.StringVar(&cfg.ImageHash, "image-hash", cfg.ImageHash, "optional host image hash override (HYPERMESH_IMAGE_HASH)")
	flag.StringVar(&cfg.ListenPort, "listen-port", cfg.ListenPort, "host port published to the workload :8080 (HYPERMESH_LISTEN_PORT)")
	flag.StringVar(&cfg.CompletionsURL, "completions-url", cfg.CompletionsURL, "local OpenAI-shaped completions URL (HYPERMESH_COMPLETIONS_URL)")
	flag.Parse()

	a, err := agent.New(cfg, log)
	if err != nil {
		log.Error("could not start on this box", "err", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("hypermesh starting",
		"control_plane", cfg.ControlPlaneURL,
		"state_dir", cfg.StateDir,
		"listen_port", cfg.PublishPort(),
	)
	if err := a.Run(ctx); err != nil && err != context.Canceled && err != context.DeadlineExceeded {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
	log.Info("stopped")
}
