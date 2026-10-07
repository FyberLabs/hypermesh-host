//go:build linux

package installer

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/agent"
	"github.com/FyberLabs/hypermesh-host/internal/buildinfo"
	"github.com/FyberLabs/hypermesh-host/internal/hostregistration"
)

type qualificationJournal struct {
	FailureDetail string                               `json:"failure_detail,omitempty"`
	Schema        int                                  `json:"schema"`
	AgentSHA      string                               `json:"agent_sha256"`
	Run           hostregistration.QualificationRun    `json:"run"`
	State         string                               `json:"state"`
	Report        hostregistration.QualificationReport `json:"report"`
}
type qualificationDeps struct {
	Check   func(context.Context) error
	Acquire func(context.Context) error
	Tunnel  func(context.Context) error
	Sample  func(context.Context, string) (hostregistration.QualificationSample, error)
	Save    func(qualificationJournal) error
	Report  func(context.Context, hostregistration.QualificationReport) error
}

// Intent is durable before execution. Interrupted samples are retained as a
// failed attempt; they never silently turn into a successful repeated run.
func performQualification(ctx context.Context, j *qualificationJournal, d qualificationDeps) error {
	if j.State == "reported" {
		if j.Report.FailureCode != "" {
			return fmt.Errorf("qualification failed (%s): %s; for guided setup, rerun hypermesh-deploy to request and approve a fresh measurement attempt; for manual qualify-host, ask the operator to authorize a new run, then rerun qualify-host with the same --agent-plan and the new --run-id; registration is retained", j.Report.FailureCode, j.FailureDetail)
		}
		return nil
	}
	if j.State == "running" {
		j.Report.FailureCode = "interrupted"
		j.State = "evidence-ready"
		if err := d.Save(*j); err != nil {
			return err
		}
	}
	if j.State == "approved" {
		j.State = "running"
		if err := d.Save(*j); err != nil {
			return err
		}
		fail := func(code string) { j.Report.FailureCode = code }
		if err := d.Check(ctx); err != nil {
			fail("preflight")
		} else if err = d.Acquire(ctx); err != nil {
			fail("acquisition")
		} else {
			for i := 0; i < 3; i++ {
				if err = d.Check(ctx); err != nil {
					fail("preflight")
					break
				}
				if err = d.Tunnel(ctx); err != nil {
					fail("tunnel")
					break
				}
				sample, e := d.Sample(ctx, j.Run.Manifest.MeasurementIDs[i])
				if e != nil || sample.MeasurementID != j.Run.Manifest.MeasurementIDs[i] {
					fail("workload")
					var failure *agent.WorkloadFailure
					if errors.As(e, &failure) {
						j.FailureDetail = failure.Error()
					}
					break
				}
				sample.TunnelVerified = true
				j.Report.Samples = append(j.Report.Samples, sample)
				if err = d.Save(*j); err != nil {
					return err
				}
			}
		}
		j.State = "evidence-ready"
		if err := d.Save(*j); err != nil {
			return err
		}
	}
	if j.State != "evidence-ready" {
		return fmt.Errorf("invalid qualification checkpoint")
	}
	if err := d.Report(ctx, j.Report); err != nil {
		return err
	}
	j.State = "reported"
	if err := d.Save(*j); err != nil {
		return err
	}
	if j.Report.FailureCode != "" {
		return fmt.Errorf("qualification recorded a %s failure: %s; for guided setup, rerun hypermesh-deploy to request and approve a fresh measurement attempt; for manual qualify-host, ask the operator to authorize a new run, then rerun qualify-host with the same --agent-plan and the new --run-id; registration is retained", j.Report.FailureCode, j.FailureDetail)
	}
	return nil
}
func qualificationSample(ctx context.Context, p certificationPlan, measurementID string) (hostregistration.QualificationSample, error) {
	m := p.Run.Manifest
	request := agent.WorkloadPreflight{PlanSHA256: measurementID, CatalogID: m.CatalogID, ImageDigest: m.ImageDigest, ModelPath: p.ModelPath, FileHash: m.FileHash, ContextTokens: m.ContextTokens, FullContext: true, SuiteID: m.SuiteID}
	config, err := os.MkdirTemp(defaultSetupState, ".qualification-docker-")
	if err != nil {
		return hostregistration.QualificationSample{}, err
	}
	defer os.RemoveAll(config)
	receipt, err := agent.RunWorkloadPreflight(ctx, request, "/var/lib/hypermesh", func(ctx context.Context, _ string, args ...string) ([]byte, error) {
		return workloadDocker(ctx, config, args...)
	})
	if err != nil {
		return hostregistration.QualificationSample{}, err
	}
	if receipt.ContextFit == nil || !receipt.ContextFit.ValidFor(m.ContextTokens) || !receipt.CleanupComplete {
		return hostregistration.QualificationSample{}, fmt.Errorf("qualification workload or cleanup incomplete")
	}
	return hostregistration.QualificationSample{MeasurementID: measurementID, CertificationEvidence: hostregistration.CertificationEvidence{HostImageHash: receipt.HostImageHash, FileHash: m.FileHash, ImageDigest: m.ImageDigest, SampleMS: receipt.SampleMS, TokensPerSecond: receipt.TokensPerSecond, ThermalC: receipt.ThermalC, ContextTokens: m.ContextTokens, PromptTokens: receipt.ContextFit.PromptTokens, GeneratedTokens: receipt.ContextFit.GeneratedTokens, ContextElapsedMS: receipt.ContextFit.ElapsedMS, CleanupComplete: true}}, nil
}
func RunQualifyHost(ctx context.Context, args []string, input io.Reader, out, diagnostic io.Writer) int {
	f := flag.NewFlagSet("qualify-host", flag.ContinueOnError)
	f.SetOutput(diagnostic)
	id := f.String("run-id", "", "operator-authorized qualification run UUID")
	path := f.String("agent-plan", "", "installed agent plan; defaults to the guided installation")
	tokenPath := f.String("model-token-file", "", "optional private Hugging Face token file")
	if err := f.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 || !hostregistration.UUID.MatchString(*id) {
		fmt.Fprintln(diagnostic, "require --run-id UUID from the program operator")
		return 2
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(diagnostic, "requires root for the registered inactive host")
		return 1
	}
	if *path == "" {
		dir := filepath.Join(defaultSetupState, "guided")
		var s guidedState
		if err := guidedRead(filepath.Join(dir, "session.json"), &s, true); err != nil {
			fmt.Fprintln(diagnostic, err)
			return 1
		}
		planDir, err := guidedPlanDirectory(dir, s)
		if err != nil {
			fmt.Fprintln(diagnostic, err)
			return 1
		}
		*path = filepath.Join(planDir, "agent.json")
	}
	unlock, err := lockSetupState(defaultSetupState)
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	defer unlock()
	var p certificationPlan
	p.Schema = 1
	err = readQualificationAgentPlan(*path, &p.Agent)
	if err == nil {
		err = validateInstalledAgentInputs(p.Agent)
	}
	if err == nil && agentPlanDigest(p.Agent) != p.Agent.SHA256 {
		err = fmt.Errorf("agent plan changed")
	}
	var identity registrationJournal
	if err == nil {
		err = guidedRead(registrationPath(defaultSetupState, p.Agent.SHA256), &identity, true)
	}
	if err == nil {
		err = validateRegistration(identity, p.Agent, identity.Issuer, identity.Dashboard, identity.Request.ClassID)
	}
	if err == nil && identity.State != "registered-inactive" {
		err = fmt.Errorf("qualification requires a registered inactive host")
	}
	if err == nil {
		err = certPreflight(ctx, p, identity)
	}
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	c, err := hostregistration.New(p.Agent.ControlPlane, p.Agent.TenantID)
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	journalPath := filepath.Join(defaultSetupState, "qualification-"+*id+".json")
	var j qualificationJournal
	if _, e := os.Lstat(journalPath); e == nil {
		err = guidedRead(journalPath, &j, true)
		if err == nil && (j.Schema != 1 || j.AgentSHA != p.Agent.SHA256 || j.Run.RunID != *id || j.Run.DeviceID != identity.DeviceID || j.Run.RequestID != identity.Request.RequestID || j.Run.TenantID != p.Agent.TenantID || j.Run.Manifest.ClassID != identity.Request.ClassID || j.Run.Manifest.ValidateQualification() != nil || !setupDigestRE.MatchString(j.Run.ManifestSHA256) || !validQualificationCheckpoint(j) || !supportOneOf(j.State, "approved", "running", "evidence-ready", "reported")) {
			err = fmt.Errorf("invalid qualification journal; preserve state")
		}
	} else if os.IsNotExist(e) {
		j.Schema = 1
		j.AgentSHA = p.Agent.SHA256
		j.Run, err = c.PlanQualification(ctx, identity.Request, identity.DeviceID, identity.DeviceSecret, *id)
		if err == nil && (j.Run.State != "planned" || !j.Run.ExpiresAt.After(time.Now())) {
			err = fmt.Errorf("request a new qualification run; this attempt has ended")
		}
		if err == nil {
			m := j.Run.Manifest
			fmt.Fprintf(out, "Lab qualification: %s on %s\nModel: %s\nSHA-256: %s\nRuntime: %s\nContext: %d tokens; three independent model-load and measured generation runs.\n", m.CatalogID, p.Agent.HostName, m.SourceRef, m.FileHash, m.ImageDigest, m.ContextTokens)
			ui := guidedUI{in: bufio.NewReader(input), out: out, ctx: ctx}
			err = ui.confirm("Download the pinned model and run these tests? Evidence goes to operator review; services remain inactive and selling stays disabled.")
		}
		if err == nil {
			j.State = "approved"
			j.Report.Samples = []hostregistration.QualificationSample{}
			j.Report.Software = hostregistration.QualificationSoftware{Kernel: p.Agent.Setup.Platform.Kernel, OSVersion: p.Agent.Setup.Platform.Version, InstallerCommit: buildinfo.Current()}
			err = writeGuided(journalPath, j)
		}
	} else {
		err = e
	}
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	p.Run = j.Run.CertificationRun
	p.ModelPath = filepath.Join(p.Agent.Setup.Preflight.Resources.ModelDirectory, "cache", strings.TrimPrefix(p.Run.Manifest.FileHash, "sha256:")+".gguf")
	p.SHA256 = certificationDigest(p)
	if _, err = certificationURL(p.Run.Manifest.SourceRef); err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	token, err := modelAccessToken(*tokenPath)
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Minute)
	defer cancel()
	err = performQualification(ctx, &j, qualificationDeps{
		Check: func(ctx context.Context) error {
			if !j.Run.ExpiresAt.After(time.Now()) {
				return fmt.Errorf("run expired")
			}
			return certPreflight(ctx, p, identity)
		},
		Acquire: func(ctx context.Context) error {
			if err := agentDirectory(filepath.Dir(p.ModelPath), true); err != nil {
				return err
			}
			address, err := certificationURL(p.Run.Manifest.SourceRef)
			if err != nil {
				return err
			}
			transfer, cancel := context.WithTimeout(ctx, 30*time.Minute)
			defer cancel()
			return acquireModel(transfer, p.ModelPath, p.Run.Manifest.FileHash, p.Run.Manifest.SizeBytes, func(ctx context.Context) (io.ReadCloser, error) {
				return modelBytes(ctx, modelTransport(token), address, token)
			})
		},
		Tunnel: func(ctx context.Context) error { return certTunnel(ctx, p, identity, c) },
		Sample: func(ctx context.Context, measurementID string) (hostregistration.QualificationSample, error) {
			fmt.Fprintf(out, "Running measurement %d/3…\n", len(j.Report.Samples)+1)
			return qualificationSample(ctx, p, measurementID)
		},
		Save: func(value qualificationJournal) error { return writeGuided(journalPath, value) },
		Report: func(ctx context.Context, report hostregistration.QualificationReport) error {
			result, err := c.ReportQualification(ctx, identity.Request, identity.DeviceID, identity.DeviceSecret, j.Run, report)
			expected := "passed"
			if report.FailureCode != "" {
				expected = "failed"
			}
			if err == nil && result.State != expected {
				return fmt.Errorf("plane rejected qualification evidence; operator review required")
			}
			return err
		},
	})
	fmt.Fprintln(out, "Qualification evidence saved at", journalPath)
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	fmt.Fprintln(out, "Measurements recorded. The program operator must review and publish the baseline before normal host certification. No services were activated.")
	return 0
}

func validQualificationCheckpoint(j qualificationJournal) bool {
	if j.Run.Manifest.ValidateQualification() != nil {
		return false
	}
	for i, sample := range j.Report.Samples {
		if i >= 3 || sample.MeasurementID != j.Run.Manifest.MeasurementIDs[i] {
			return false
		}
	}
	if len(j.Report.Samples) > 3 || !supportOneOf(j.Report.FailureCode, "", "preflight", "acquisition", "workload", "tunnel", "interrupted") {
		return false
	}
	if j.Run.Purpose != "lab-qualification-v1" || j.Run.Repetitions != 3 || j.Run.SellingAllowed == nil || *j.Run.SellingAllowed || j.Run.ActivationAllowed != nil {
		return false
	}
	if j.State == "approved" && (len(j.Report.Samples) != 0 || j.Report.FailureCode != "") {
		return false
	}
	if supportOneOf(j.State, "evidence-ready", "reported") && j.Report.FailureCode == "" && len(j.Report.Samples) != 3 {
		return false
	}
	return j.Report.Software.Kernel != "" && j.Report.Software.OSVersion != "" && len(j.Report.Software.InstallerCommit) == 40
}

func readQualificationAgentPlan(path string, plan *agentInstallPlan) error {
	raw, err := readSetupRegular(path, false)
	if err != nil {
		return err
	}
	return decodeSetupJSON(raw, plan)
}

// Called before ordinary certification planning. The guide remains at this
// stage until an authoritative baseline exists; it never treats review as failure.
func guidedQualification(ctx context.Context, path string, ui guidedUI, diagnostic io.Writer) (bool, error) {
	var plan agentInstallPlan
	if err := readQualificationAgentPlan(path, &plan); err != nil {
		return false, err
	}
	if err := validateInstalledAgentInputs(plan); err != nil {
		return false, err
	}
	if agentPlanDigest(plan) != plan.SHA256 {
		return false, fmt.Errorf("agent plan changed")
	}
	var identity registrationJournal
	if err := guidedRead(registrationPath(defaultSetupState, plan.SHA256), &identity, true); err != nil {
		return false, err
	}
	if err := validateRegistration(identity, plan, identity.Issuer, identity.Dashboard, identity.Request.ClassID); err != nil {
		return false, err
	}
	if identity.State != "registered-inactive" {
		return false, fmt.Errorf("qualification requires inactive registration")
	}
	client, err := hostregistration.New(plan.ControlPlane, plan.TenantID)
	if err != nil {
		return false, err
	}
	next, err := client.NextQualification(ctx, identity.Request, identity.DeviceID, identity.DeviceSecret)
	if err != nil {
		return false, err
	}
	if next.Action == "certification" {
		return true, nil
	}
	if next.Action == "qualification" {
		fmt.Fprintln(ui.out, "This hardware class has no reference benchmark. Your host will measure the configured model for network-admin review.")
		args, err := qualificationArgs(ui, path, *next.RunID)
		if err != nil {
			return false, err
		}
		if code := RunQualifyHost(ctx, args, ui.in, ui.out, diagnostic); code != 0 {
			return false, fmt.Errorf("qualification paused; evidence and registration are retained")
		}
	}
	if next.Action == "attention" {
		fmt.Fprintln(ui.out, "This plane has not offered a fresh qualification attempt. The program operator must restore host-requested retries; continuation will check again. Your measurements and registration are retained; services remain inactive.")
	} else {
		fmt.Fprintln(ui.out, "Measurements await network-admin review. Setup is saved; automatic continuation will check for approval.")
	}
	fmt.Fprintln(ui.out, "Admin review: "+identity.Dashboard+"/operator/benchmarks")
	return false, nil
}

func qualificationArgs(ui guidedUI, path, id string) ([]string, error) {
	tokenPath, err := ui.ask("Private Hugging Face token file for a gated model (Enter for public or cached models)", "")
	if err != nil {
		return nil, err
	}
	args := []string{"--agent-plan", path, "--run-id", id}
	if tokenPath != "" {
		if _, err = modelAccessToken(tokenPath); err != nil {
			return nil, err
		}
		args = append(args, "--model-token-file", tokenPath)
	}
	return args, nil
}
