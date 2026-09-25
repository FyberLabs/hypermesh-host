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
	var tokenFile string
	fs.StringVar(&opt.ControlPlaneURL, "control-plane-url", "https://api.test.hyperme.sh", "control plane base URL")
	fs.StringVar(&opt.TenantID, "tenant-id", os.Getenv("HYPERMESH_TENANT_ID"), "marketplace tenant UUID")
	fs.StringVar(&tokenFile, "enroll-token-file", os.Getenv("HYPERMESH_ENROLL_TOKEN_FILE"), "file containing the one-time enroll token (or HYPERMESH_ENROLL_TOKEN)")
	fs.StringVar(&opt.StateDir, "state-dir", "/var/lib/hypermesh", "agent state directory")
	fs.StringVar(&opt.GGUFPath, "gguf-path", os.Getenv("HYPERMESH_GGUF_PATH"), "host GGUF file for the existing agent")
	fs.StringVar(&opt.EnvFile, "env-file", "/etc/hypermesh/agent.env", "agent env file")
	fs.StringVar(&opt.UnitDir, "unit-dir", "/etc/systemd/system", "systemd unit directory")
	fs.StringVar(&opt.Serial, "serial", "", "known-host serial (required); device id comes from the plane list")
	fs.DurationVar(&opt.HeartbeatWait, "heartbeat-wait", 0, "how long to wait for a heartbeat (default 45s)")
	_ = fs.Parse(os.Args[2:])
	if opt.Serial == "" {
		fmt.Fprintln(os.Stderr, "serial is required")
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

Host classes:
  agx    preflight, configure, and connect an AGX. Stops on the first failure.

-serial is required. The device id is read from GET /api/v1/hypermesh/devices
for that serial. It is not a flag.

The enroll token comes from HYPERMESH_ENROLL_TOKEN or -enroll-token-file.
It is not a command-line flag. A box that already has device.json can rerun
without a fresh token.

Dev install: go install github.com/FyberLabs/hypermesh-host/cmd/hypermesh-deploy@latest
`)
}
