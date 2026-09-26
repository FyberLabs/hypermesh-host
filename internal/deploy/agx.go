package deploy

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/agent"
)

// AGX is the first host class. Another class implements HostClass and is
// added in Class next to this one.
type AGX struct{}

func (AGX) Name() string { return "agx" }

// Options are the inputs for one AGX command. Paths are absolute on the
// box. Root prefixes them in tests.
type Options struct {
	ControlPlaneURL string
	TenantID        string
	EnrollToken     string
	StateDir        string
	GGUFPath        string
	Root            string
	EnvFile         string
	UnitDir         string
	// Serial is the known host. Required. The device id is not an
	// option. It comes from the device list.
	Serial string
	// HTTP is used for catalog and the device list. Nil uses a 15s client.
	HTTP *http.Client

	Facts   *Facts
	Collect func(context.Context) (Facts, error)
	// Run executes systemctl. Nil uses exec.Command.
	Run func(context.Context, string, ...string) error

	PollEvery     time.Duration
	HeartbeatWait time.Duration
	// Now is the clock used to require a heartbeat newer than connect.
	Now func() time.Time
}

// Normalize fills defaults that do not hide a missing tenant or token.
func (o Options) withDefaults() Options {
	if o.ControlPlaneURL == "" {
		o.ControlPlaneURL = defaultControlPlane
	}
	if o.StateDir == "" {
		o.StateDir = defaultStateDir
	}
	if o.EnvFile == "" {
		o.EnvFile = defaultEnvFile
	}
	if o.UnitDir == "" {
		o.UnitDir = defaultUnitDir
	}
	if o.Run == nil {
		o.Run = func(ctx context.Context, name string, args ...string) error {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// Pipeline is preflight, configure, connect.
func (AGX) Pipeline(opt Options) Pipeline {
	opt = opt.withDefaults()
	return Pipeline{Steps: []Step{
		{Name: StepPreflight, Run: func(ctx context.Context) error { return preflight(ctx, opt) }},
		{Name: StepConfigure, Run: func(ctx context.Context) error { return configure(ctx, opt) }},
		{Name: StepConnect, Run: func(ctx context.Context) error { return connect(ctx, opt) }},
	}}
}

func preflight(ctx context.Context, opt Options) error {
	facts, err := loadFacts(ctx, opt)
	if err != nil {
		return err
	}
	return CheckAGX(facts)
}

func loadFacts(ctx context.Context, opt Options) (Facts, error) {
	if opt.Facts != nil {
		return *opt.Facts, nil
	}
	if opt.Collect != nil {
		return opt.Collect(ctx)
	}
	return CollectHost(ctx), nil
}

func configure(_ context.Context, opt Options) error {
	if strings.TrimSpace(opt.Serial) == "" {
		return fmt.Errorf("known host serial is required")
	}
	enrolled := AlreadyEnrolled(opt.Root, opt.StateDir)
	body, err := RenderAgentEnv(opt.ControlPlaneURL, opt.TenantID, opt.EnrollToken, opt.StateDir, opt.GGUFPath, enrolled)
	if err != nil {
		return err
	}
	if _, err := writeAgentEnv(opt.Root, opt.EnvFile, body); err != nil {
		return err
	}
	if err := writeUnits(opt.Root, opt.UnitDir); err != nil {
		return err
	}
	state := underRoot(opt.Root, opt.StateDir)
	if err := os.MkdirAll(state, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(state, 0o700); err != nil {
		return err
	}
	return ensureDefaultModelDir(opt.Root)
}

// ensureDefaultModelDir creates /var/lib/hypermesh/models at 0755.
// The state directory stays 0700 (device.json). Weight files written later
// are 0600 root. The 0700 parent keeps them unreadable to other users.
// Rootful Docker bind-mounts them as root. Root owns the directory when
// this process is root.
func ensureDefaultModelDir(root string) error {
	dir := underRoot(root, agent.DefaultModelDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(dir, 0, 0); err != nil {
			return err
		}
	}
	return nil
}

func connect(ctx context.Context, opt Options) error {
	p := plane{base: opt.ControlPlaneURL, tenant: opt.TenantID, http: opt.HTTP}
	if err := p.catalog(ctx); err != nil {
		return err
	}
	rows, err := p.devices(ctx)
	if err != nil {
		return err
	}
	deviceID, err := DeviceIDFromList(rows, opt.Serial)
	if err != nil {
		return err
	}
	// Heartbeats at or before this instant do not count. Two minutes of
	// skew covers a clock that is slightly behind the plane.
	since := opt.Now().Add(-2 * time.Minute)
	if err := startExistingAgent(ctx, opt.Run); err != nil {
		return err
	}
	return waitHeartbeat(ctx, p, deviceID, since, opt.PollEvery, opt.HeartbeatWait)
}
