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
	"strings"
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
	flag.StringVar(&cfg.GGUFPath, "gguf-path", cfg.GGUFPath, "host GGUF bind-mounted at /models/model.gguf:ro (HYPERMESH_GGUF_PATH; default /var/lib/hypermesh/models/model.gguf)")
	flag.StringVar(&cfg.DiskFloor, "disk-floor-bytes", cfg.DiskFloor, "bytes that must remain free after a model download (HYPERMESH_DISK_FLOOR_BYTES, default 2 GiB)")
	flag.StringVar(&cfg.DiskMargin, "disk-margin-bytes", cfg.DiskMargin, "extra bytes required beyond the object size (HYPERMESH_DISK_MARGIN_BYTES, default 64 MiB)")
	flag.StringVar(&cfg.ModelCacheMax, "model-cache-max-bytes", cfg.ModelCacheMax, "model cache byte budget (HYPERMESH_MODEL_CACHE_MAX_BYTES; default is filesystem size minus the disk floor)")
	flag.StringVar(&cfg.PendingTTL, "model-cache-pending-ttl", cfg.PendingTTL, "how long a catalog pin stays unevictable (HYPERMESH_MODEL_CACHE_PENDING_TTL; default 30m)")
	flag.StringVar(&cfg.LlamaJinja, "llama-jinja", cfg.LlamaJinja, "lease llama-server LLAMA_ARG_JINJA (HYPERMESH_LLAMA_JINJA)")
	flag.StringVar(&cfg.LlamaCtxSize, "llama-ctx-size", cfg.LlamaCtxSize, "lease llama-server LLAMA_ARG_CTX_SIZE (HYPERMESH_LLAMA_CTX_SIZE)")
	flag.StringVar(&cfg.LlamaTimeout, "llama-timeout", cfg.LlamaTimeout, "lease llama-server LLAMA_ARG_TIMEOUT seconds (HYPERMESH_LLAMA_TIMEOUT)")
	flag.StringVar(&cfg.WGMode, "wg-userspace", cfg.WGMode, "WireGuard backend: auto, 1 (hypermesh-wg.service), or 0 (kernel wg-quick) (HYPERMESH_WG_USERSPACE)")
	flag.StringVar(&cfg.MetricsAddr, "metrics-addr", cfg.MetricsAddr, "explicit Prometheus /metrics bind (HYPERMESH_METRICS_ADDR); empty follows the hypermesh address or 127.0.0.1:9464. Public and unspecified addresses are refused")
	flag.BoolVar(&cfg.MetricsAllowNonLoopback, "metrics-allow-non-loopback", cfg.MetricsAllowNonLoopback, "ignored; /metrics binds only loopback or the WireGuard address (HYPERMESH_METRICS_ALLOW_NON_LOOPBACK)")
	flag.StringVar(&cfg.HostType, "host-type", cfg.HostType, "model (default) or supervisor (enroll and heartbeat, refuse lease_start) (HYPERMESH_HOST_TYPE)")
	flag.StringVar(&cfg.WGKeyDir, "wg-key-dir", cfg.WGKeyDir, "host WireGuard key directory (HYPERMESH_WG_KEY_DIR)")

	if pre, ownerArgs, ok := splitOwnerArgs(os.Args[1:]); ok {
		if err := flag.CommandLine.Parse(pre); err != nil {
			os.Exit(2)
		}
		if err := agent.RunOwnerCLI(cfg.StateDir, ownerArgs); err != nil {
			log.Error("owner command failed", "err", err)
			os.Exit(1)
		}
		return
	}
	flag.Parse()

	a, err := agent.New(cfg, log)
	if err != nil {
		log.Error("could not start on this box", "err", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a.StartMetrics(ctx)

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

// splitOwnerArgs finds a bare "owner" subcommand so
// `hypermesh-agent -state-dir=DIR owner sell-pause` works.
func splitOwnerArgs(args []string) (pre, rest []string, ok bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return nil, nil, false
		}
		if a == "owner" {
			return args[:i], args[i+1:], true
		}
		if strings.HasPrefix(a, "-") {
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && args[i+1] != "owner" {
				i++
			}
			continue
		}
		return nil, nil, false
	}
	return nil, nil, false
}
