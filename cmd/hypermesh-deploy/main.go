// Command hypermesh-deploy is the operator path for a host class.
// It is not the renter CLI. The AGX class preflights, writes the agent
// env and units, then starts the existing hypermesh-wg and hypermesh-agent
// units. The first failure stops the command.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/FyberLabs/hypermesh-host/internal/deploy"
	"github.com/FyberLabs/hypermesh-host/internal/hardware"
	"github.com/FyberLabs/hypermesh-host/internal/installer"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		usage()
		os.Exit(2)
	}
	name := os.Args[1]
	if name == "plan-setup" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		code := installer.RunSetupPlan(ctx, os.Args[2:], os.Stdout, os.Stderr)
		stop()
		os.Exit(code)
	}
	if name == "download" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		code := installer.RunDownload(ctx, os.Args[2:], os.Stdout, os.Stderr)
		stop()
		os.Exit(code)
	}
	if name == "plan" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		code := installer.RunPlan(ctx, os.Args[2:], os.Stdout, os.Stderr)
		stop()
		os.Exit(code)
	}
	if name == "check-resources" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		code := installer.RunResourceCheck(ctx, os.Args[2:], os.Stdout, os.Stderr)
		stop()
		os.Exit(code)
	}
    if name == "inspect" {
        flags := flag.NewFlagSet("inspect",flag.ExitOnError)
        model := flags.String("model","","explicit local GGUF for a measured benchmark")
        benchmark := flags.Bool("benchmark",false,"run installed llama-bench after memory preflight")
        _ = flags.Parse(os.Args[2:])
        ctx,stop:=signal.NotifyContext(context.Background(),syscall.SIGINT,syscall.SIGTERM)
        defer stop()
        profile:=hardware.Detect(ctx)
        _ = json.NewEncoder(os.Stdout).Encode(profile)
        if *benchmark {
            if err:=hardware.Benchmark(ctx,profile,*model);err!=nil {
                fmt.Fprintln(os.Stderr,err);os.Exit(1)
            }
        }
        return
    }
	class, err := deploy.Class(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		usage()
		os.Exit(2)
	}
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	var opt deploy.Options
	var tokenFile string
	fs.StringVar(&opt.ControlPlaneURL, "control-plane-url", "https://api.test.hyperme.sh", "control plane base URL")
	fs.StringVar(&opt.TenantID, "tenant-id", os.Getenv("HYPERMESH_TENANT_ID"), "marketplace tenant UUID")
	fs.StringVar(&tokenFile, "enroll-token-file", os.Getenv("HYPERMESH_ENROLL_TOKEN_FILE"), "file containing the one-time enroll token (or HYPERMESH_ENROLL_TOKEN)")
	fs.StringVar(&opt.StateDir, "state-dir", "/var/lib/hypermesh", "agent state directory")
	fs.StringVar(&opt.GGUFPath, "gguf-path", os.Getenv("HYPERMESH_GGUF_PATH"), "host GGUF file (default /var/lib/hypermesh/models/model.gguf when empty)")
	fs.StringVar(&opt.ModelCacheMax, "model-cache-max-bytes", os.Getenv("HYPERMESH_MODEL_CACHE_MAX_BYTES"), "model cache byte budget (default: filesystem size minus the 2 GiB disk floor)")
	fs.StringVar(&opt.EnvFile, "env-file", "/etc/hypermesh/agent.env", "agent env file")
	fs.StringVar(&opt.UnitDir, "unit-dir", "/etc/systemd/system", "systemd unit directory")
	fs.StringVar(&opt.Serial, "host-name", os.Getenv("HYPERMESH_HOST_NAME"), "reported enrollment host name; device id comes from the plane list")
	fs.DurationVar(&opt.HeartbeatWait, "heartbeat-wait", 0, "how long to wait for a heartbeat (default 45s)")
	_ = fs.Parse(os.Args[2:])
	if opt.Serial == "" {
		fmt.Fprintln(os.Stderr, "host-name is required")
		os.Exit(2)
	}
	token, err := deploy.LoadEnrollToken(os.Getenv("HYPERMESH_ENROLL_TOKEN"), tokenFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	opt.EnrollToken = token

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := class.Pipeline(opt).Run(ctx, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `hypermesh-deploy <class>

Commands:
  plan-setup  review versioned Linux prerequisite package and headless changes.
              Resource/readiness flags plus --profile auto and optional --headless.
              --wireguard-mode kernel (default) or userspace; only userspace needs wireguard-go.
              --list-profiles lists candidate policies; this command applies nothing.
  download  fetch a signed deploy CLI using independently trusted key material.
            Use download --help for required release, channel and build constraints.
  plan      read-only resource, hardware and HTTPS reachability plan.
            Resource flags plus --aiuditor-check-url https://HOST/READINESS_PATH
  check-resources  read-only Linux RAM, accelerator capacity and model-filesystem check.
                   --model-dir PATH --memory-mode uma --uma-gib N
                   --model-dir PATH --memory-mode discrete
  inspect   report hardware fingerprint, memory budget and preflight recommendations.
            --benchmark --model /path/model.gguf runs an installed llama-bench.

Host classes:
  agx    preflight, configure, and connect an AGX. Stops on the first failure.

-host-name is required. The device id is read from GET /api/v1/hypermesh/devices
for that name. It is not a flag.

The enroll token comes from HYPERMESH_ENROLL_TOKEN or -enroll-token-file.
It is not a command-line flag. A box that already has device.json can rerun
without a fresh token.

Dev install: go install github.com/FyberLabs/hypermesh-host/cmd/hypermesh-deploy@latest
`)
}
