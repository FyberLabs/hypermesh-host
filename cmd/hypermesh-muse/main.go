// Command hypermesh-muse is the supervisor: one binary, three placements.
// outside is off the network, host enrolls as a supervisor host, and
// renter posts chat to the gateway.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/FyberLabs/hypermesh-host/internal/agent"
	"github.com/FyberLabs/hypermesh-host/internal/muse"
)

func main() {
	fs := flag.NewFlagSet("hypermesh-muse", flag.ExitOnError)
	placement := fs.String("placement", envOr("HYPERMESH_MUSE_PLACEMENT", muse.PlacementOutside), "outside, host, or renter")
	stateDir := fs.String("state-dir", envOr("HYPERMESH_STATE_DIR", "/var/lib/hypermesh"), "credential and agent state directory")
	runtimeCmd := fs.String("runtime", os.Getenv("HYPERMESH_MUSE_RUNTIME"), "local AI process to restart")
	control := fs.String("control-plane-url", envOr("HYPERMESH_CONTROL_PLANE_URL", "https://api.test.hyperme.sh"), "control plane base URL (host placement)")
	tenant := fs.String("tenant-id", os.Getenv("HYPERMESH_TENANT_ID"), "tenant UUID")
	enroll := fs.String("enroll-token", os.Getenv("HYPERMESH_ENROLL_TOKEN"), "one-time host enroll token")
	chat := fs.String("chat-base", envOr("HYPERMESH_CHAT_BASE", muse.DefaultChatBase), "chat gateway base URL")
	apiKey := fs.String("api-key", os.Getenv("HYPERMESH_API_KEY"), "renter API key")
	lease := fs.String("lease-id", os.Getenv("HYPERMESH_LEASE_ID"), "renter lease id")
	listen := fs.String("listen", os.Getenv("HYPERMESH_MUSE_LISTEN"), "local chat listen address (renter)")
	_ = fs.Parse(os.Args[1:])

	var run muse.Runtime
	if strings.TrimSpace(*runtimeCmd) != "" {
		fields := strings.Fields(*runtimeCmd)
		run = func(ctx context.Context) error {
			cmd := exec.CommandContext(ctx, fields[0], fields[1:]...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		}
	}

	s := &muse.Supervisor{
		Placement:  *placement,
		StateDir:   *stateDir,
		ListenAddr: *listen,
		Runtime:    run,
		Creds: muse.Credentials{
			TenantID:    *tenant,
			EnrollToken: *enroll,
			APIKey:      *apiKey,
			LeaseID:     *lease,
			ChatBase:    *chat,
		},
		Agent: agent.FromEnv(),
	}
	s.Agent.ControlPlaneURL = *control
	s.Agent.TenantID = *tenant
	s.Agent.EnrollToken = *enroll
	s.Agent.StateDir = *stateDir
	s.Agent.HostType = agent.HostTypeSupervisor

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := s.Run(ctx); err != nil && err != context.Canceled {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
