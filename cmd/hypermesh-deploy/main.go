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
 if name=="plan-installer-update"||name=="update-installer"||name=="rollback-installer"{ctx,stop:=signal.NotifyContext(context.Background(),syscall.SIGINT,syscall.SIGTERM);run:=installer.RunPlanInstallerUpdate;if name=="update-installer"{run=installer.RunUpdateInstaller};if name=="rollback-installer"{run=installer.RunRollbackInstaller};code:=run(ctx,os.Args[2:],os.Stdout,os.Stderr);stop();os.Exit(code)}
 if name=="plan-maintenance"||name=="maintain-agent"||name=="resume-agent"{ctx,stop:=signal.NotifyContext(context.Background(),syscall.SIGINT,syscall.SIGTERM);run:=installer.RunPlanMaintenance;if name=="maintain-agent"{run=installer.RunMaintainAgent};if name=="resume-agent"{run=installer.RunResumeAgent};code:=run(ctx,os.Args[2:],os.Stdout,os.Stderr);stop();os.Exit(code)}
 if name=="plan-recovery"||name=="recover-agent"{ctx,stop:=signal.NotifyContext(context.Background(),syscall.SIGINT,syscall.SIGTERM);run:=installer.RunPlanRecovery;if name=="recover-agent"{run=installer.RunRecoverAgent};code:=run(ctx,os.Args[2:],os.Stdout,os.Stderr);stop();os.Exit(code)}
 if name=="plan-reboot"||name=="reboot-setup"||name=="resume-setup"||name=="reboot-status" {
  ctx,stop:=signal.NotifyContext(context.Background(),syscall.SIGINT,syscall.SIGTERM)
  run:=installer.RunPlanReboot
  switch name{case "reboot-setup":run=installer.RunRebootSetup;case "resume-setup":run=installer.RunResumeSetup;case "reboot-status":run=installer.RunRebootStatus}
  code:=run(ctx,os.Args[2:],os.Stdout,os.Stderr);stop();os.Exit(code)
 }
	if name == "plan-certification" || name == "certify-agent" || name == "activate-agent" || name == "certification-status" || name == "host-status" {
  ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
  code := 2
  switch name {
  case "host-status": code = installer.RunHostStatus(ctx,os.Args[2:],os.Stdout,os.Stderr)
  case "certification-status": code = installer.RunCertificationStatus(ctx,os.Args[2:],os.Stdout,os.Stderr)
  case "plan-certification": code = installer.RunPlanCertification(ctx,os.Args[2:],os.Stdout,os.Stderr)
  case "certify-agent": code = installer.RunCertifyAgent(ctx,os.Args[2:],os.Stdout,os.Stderr)
  case "activate-agent": code = installer.RunActivateAgent(ctx,os.Args[2:],os.Stdout,os.Stderr)
  }
  stop();os.Exit(code)
 }
 if name == "plan-workload" || name == "check-workload" || name == "workload-status" {
		ctx,stop:=signal.NotifyContext(context.Background(),syscall.SIGINT,syscall.SIGTERM)
		run:=installer.RunPlanWorkload
		if name=="check-workload"{run=installer.RunCheckWorkload}
		if name=="workload-status"{run=installer.RunWorkloadStatus}
		code:=run(ctx,os.Args[2:],os.Stdout,os.Stderr);stop();os.Exit(code)
	}
 if name == "check-runtime-memory" || name == "runtime-memory-status" {
  ctx,stop:=signal.NotifyContext(context.Background(),syscall.SIGINT,syscall.SIGTERM)
  run:=installer.RunRuntimeMemory
  if name=="runtime-memory-status"{run=installer.RunRuntimeMemoryStatus}
  code:=run(ctx,os.Args[2:],os.Stdout,os.Stderr);stop();os.Exit(code)
 }
	if name == "support-bundle" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		code := installer.RunSupportBundle(ctx, os.Args[2:], os.Stdout, os.Stderr)
		stop()
		os.Exit(code)
	}
 if name == "check-agent" || name == "onboarding-status" {
  ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
  run := installer.RunCheckAgent
  if name == "onboarding-status" { run = installer.RunOnboardingStatus }
  code := run(ctx, os.Args[2:], os.Stdout, os.Stderr)
  stop()
  os.Exit(code)
 }
 if name == "register-agent" || name == "registration-status" {
  ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
  run := installer.RunRegisterAgent
  if name == "registration-status" { run = installer.RunRegistrationStatus }
  code := run(ctx, os.Args[2:], os.Stdout, os.Stderr)
  stop()
  os.Exit(code)
 }
	if name == "plan-agent" || name == "install-agent" || name == "agent-install-status" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		run := installer.RunPlanAgent
		if name == "install-agent" { run = installer.RunInstallAgent }
		if name == "agent-install-status" { run = installer.RunAgentInstallStatus }
		code := run(ctx, os.Args[2:], os.Stdout, os.Stderr)
		stop()
		os.Exit(code)
	}
	if name == "apply-setup" || name == "setup-status" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		run := installer.RunApplySetup
		if name == "setup-status" { run = installer.RunSetupStatus }
		code := run(ctx, os.Args[2:], os.Stdout, os.Stderr)
		stop()
		os.Exit(code)
	}
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
  plan-installer-update  review signed CLI replacement; --release-url URL --channel test|prod --trusted-key FILE --fingerprints LIST
  update-installer  replace reviewed CLI with retained backup; --plan FILE --approve-plan SHA256 --apply
  rollback-installer  explicitly restore that backup; --plan FILE --approve-plan SHA256 --apply
  plan-maintenance  review enrolled-host stop/reboot; --plan CERTIFICATION_PLAN --operation stop|reboot
  maintain-agent  drain and stop; --plan FILE --approve-plan SHA256 --apply [--reboot]
  resume-agent  verify and resume paused; --plan FILE --approve-plan SHA256 --start
  plan-recovery  review inactive host repair/uninstall; --agent-plan FILE --operation repair|uninstall
  recover-agent  apply recovery while preserving identity/models; --plan FILE --approve-plan SHA256 --apply
  plan-reboot  review a pre-enrollment prerequisite reboot; --setup-plan FILE
  reboot-setup  save a checkpoint and request reboot; --plan FILE --approve-plan SHA256 --reboot
                An unconfirmed request requires explicit --retry-request; never retry in a loop.
  resume-setup  verify a new boot and current prerequisites; --plan FILE --approve-plan SHA256
  reboot-status  read the saved checkpoint; --plan-sha SHA256
  host-status  read current installed-service and authenticated plane state; --plan CERTIFICATION_PLAN_FILE
  certification-status  read redacted historical certification/activation evidence; --plan-sha SHA256
  plan-certification  obtain an authenticated frozen model/runtime plan; --agent-plan FILE
  certify-agent  acquire and certify while inactive; --plan FILE --approve-plan SHA256 --run
  activate-agent  original-owner approval and paused startup; --plan FILE --approve-plan SHA256 --start
  plan-workload  review explicit local model/runtime pins; use --help.
  check-workload  run approved inference with host services inactive.
                  --plan FILE --approve-plan SHA256 --run
  workload-status  read historical workload evidence; --plan-sha SHA256
  check-runtime-memory  explicitly allocate/fill/sample/free the reviewed UMA budget (Linux, root).
                        --setup-plan FILE --approve-plan SHA256 --allocate
  runtime-memory-status  read dated allocation evidence; --setup-plan-sha SHA256
  support-bundle  export redacted, historical installer diagnostics (Linux, root).
                  --plan-sha SHA256 [--setup-plan-sha SHA256] --output /absolute/new.json
                  Add --workload-plan-sha SHA256 for inference/context evidence.
                  Add --certification-plan-sha SHA256 for certification/activation recovery.
                  Add --reboot-plan-sha SHA256 for prerequisite reboot recovery.
                  Any one plan digest also works; use support-bundle --help.
  check-agent  verify saved device identity, plane protocol and local preflight.
               --plan FILE --approve-plan SHA256 [--heartbeat] [--wireguard]; does not activate services.
               --heartbeat records one HTTPS heartbeat, not a verified tunnel.
  onboarding-status  read the dated readiness snapshot; --plan-sha SHA256
  register-agent  device login and browser-approved registration; leaves service inactive.
                  --plan FILE --approve-plan SHA256 --issuer HTTPS_REALM
                  --dashboard-url HTTPS_ORIGIN --class-id CLASS
  registration-status  read redacted registration state; --plan-sha SHA256
  agent-install-status  read persistent install status; --plan-sha SHA256
  plan-agent  verify and plan agent, trust, rollback helper and systemd installation.
               Use plan-agent --help; requires completed prerequisite plan.
  install-agent  apply/resume reviewed first installation; leaves services inactive.
               --plan FILE --approve-plan SHA256
  apply-setup  apply or resume reviewed Linux prerequisites; requires root.
               --plan FILE --approve-plan SHA256 [--state-dir DIR]
  setup-status show the persistent stage/error for a prerequisite application.
               --plan-sha SHA256 [--state-dir DIR]
  plan-setup  review versioned Linux prerequisite package and headless changes.
              Resource/readiness flags plus --profile auto and optional --headless.
              --wireguard-mode kernel (default) or userspace; only userspace needs wireguard-go.
              --list-profiles lists candidate policies; this command applies nothing.
  download  fetch a signed deploy CLI or agent using independently trusted key material.
            --artifact deploy (default) or agent; --match-installer pins release identity.
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
