// Command hypermesh-deploy is the operator path for a host class.
// It is not the renter CLI. The AGX class preflights, writes the agent
// env and units, then starts the existing hypermesh-wg and hypermesh-agent
// units. The first failure stops the command.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/FyberLabs/hypermesh-host/internal/deploy"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		usage()
		os.Exit(2)
	}
	name := os.Args[1]
	class, err := deploy.Class(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		usage()
		os.Exit(2)
	}
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	var opt deploy.Options
	fs.StringVar(&opt.ControlPlaneURL, "control-plane-url", "https://api.test.hyperme.sh", "control plane base URL")
	fs.StringVar(&opt.TenantID, "tenant-id", os.Getenv("HYPERMESH_TENANT_ID"), "marketplace tenant UUID")
	fs.StringVar(&opt.EnrollToken, "enroll-token", os.Getenv("HYPERMESH_ENROLL_TOKEN"), "one-time enroll token")
	fs.StringVar(&opt.StateDir, "state-dir", "/var/lib/hypermesh", "agent state directory")
	fs.StringVar(&opt.GGUFPath, "gguf-path", os.Getenv("HYPERMESH_GGUF_PATH"), "host GGUF file for the existing agent")
	fs.StringVar(&opt.EnvFile, "env-file", "/etc/hypermesh/agent.env", "agent env file")
	fs.StringVar(&opt.UnitDir, "unit-dir", "/etc/systemd/system", "systemd unit directory")
	fs.StringVar(&opt.Serial, "serial", "FYBER-AGX64-1", "known-host serial; device id comes from the plane list")
	fs.DurationVar(&opt.HeartbeatWait, "heartbeat-wait", 0, "how long to wait for a heartbeat (default 45s)")
	_ = fs.Parse(os.Args[2:])

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := class.Pipeline(opt).Run(ctx, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `hypermesh-deploy <class>

Host classes:
  agx    preflight, configure, and connect an AGX. Stops on the first failure.

The device id is read from GET /api/v1/hypermesh/devices for the known-host
serial. It is not a flag.

Dev install: go install github.com/FyberLabs/hypermesh-host/cmd/hypermesh-deploy@latest
`)
}
