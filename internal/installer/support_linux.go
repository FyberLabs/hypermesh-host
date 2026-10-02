//go:build linux

package installer

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"syscall"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/buildinfo"
)

// Support exports are a separate allowlisted schema, never a journal archive.
// In particular, do not add raw errors, environment, URLs, keys or identities.
type supportBundle struct {
	Schema int `json:"schema"`
	GeneratedAt time.Time `json:"generated_at"`
	CollectorCommit string `json:"collector_commit"`
	CollectorBuild int `json:"collector_build"`
	Architecture string `json:"architecture"`
	AgentPlanSHA256 string `json:"agent_plan_sha256,omitempty"`
	SetupPlanSHA256 string `json:"setup_plan_sha256,omitempty"`
	WorkloadPlanSHA256 string `json:"workload_plan_sha256,omitempty"`
	CertificationPlanSHA256 string `json:"certification_plan_sha256,omitempty"`
	RebootPlanSHA256 string `json:"reboot_plan_sha256,omitempty"`
	HistoricalOnly bool `json:"historical_only"`
	CollectionComplete bool `json:"collection_complete"`
	Stages []supportStage `json:"stages"`
}
type supportStage struct {
	Component string `json:"component"`
	Collection string `json:"collection"`
	State string `json:"state,omitempty"`
	Stage string `json:"stage,omitempty"`
	ErrorRecorded bool `json:"error_recorded"`
	CompletedFiles int `json:"completed_files,omitempty"`
	FailureCode string `json:"failure_code,omitempty"`
	RecordedAt *time.Time `json:"recorded_at,omitempty"`
	PlaneCheckedAt *time.Time `json:"plane_checked_at,omitempty"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty"`
	HeartbeatRecordedAt *time.Time `json:"heartbeat_recorded_at,omitempty"`
	MemoryAllocationBytes int64 `json:"memory_allocation_bytes,omitempty"`
	MemoryBackend string `json:"memory_backend,omitempty"`
	MemoryVerified bool `json:"memory_verified,omitempty"`
	Workload *supportWorkloadEvidence `json:"workload,omitempty"`
	Certification *supportCertificationEvidence `json:"certification,omitempty"`
	Gates []supportGate `json:"gates,omitempty"`
	NextAction string `json:"next_action"`
}
type supportGate struct {
	ID string `json:"id"`
	State string `json:"state"`
}

func supportOneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed { if value == candidate { return true } }
	return false
}

// Never echo parser, filesystem or saved diagnostic text in a shareable file.
func supportRead(dir, name string, value any) string {
	path := filepath.Join(dir, name)
	if _, err := os.Lstat(path); os.IsNotExist(err) { return "missing" }
	raw, err := readSetupRegular(path, true)
	if err != nil { return "unavailable" }
	if decodeSetupJSON(raw, value) != nil { return "invalid" }
	return "collected"
}
func supportUnavailable(component, state string) supportStage {
	action := "Preserve installer state and check this journal locally; it could not be safely summarized."
	if state == "missing" { action = "No journal exists for this stage and selected plan; confirm the plan digest and whether the stage has started." }
	if state == "not-requested" { action = "Supply its plan digest to include this stage." }
	return supportStage{Component:component, Collection:state, NextAction:action}
}
func supportSetup(dir, sha string) supportStage {
	if sha == "" { return supportUnavailable("setup", "not-requested") }
	var j setupJournal
	state := supportRead(dir, sha+".json", &j)
	if state != "collected" { return supportUnavailable("setup", state) }
	if j.PlanSHA256 != sha || validateSetupJournal(j) != nil { return supportUnavailable("setup", "invalid") }
	action := "Rerun apply-setup with the same reviewed plan after inspecting local prerequisite blockers."
	if j.State == "complete" && j.Stage == "prerequisites" { action = "Prerequisites were recorded complete; continue with the approved agent installation." }
	return supportStage{Component:"setup", Collection:"collected", State:j.State, Stage:j.Stage, ErrorRecorded:j.Error!="", NextAction:action}
}
func supportAgent(dir, sha string) supportStage {
	if sha == "" { return supportUnavailable("agent", "not-requested") }
	var j agentInstallJournal
	state := supportRead(dir, "agent-"+sha+".json", &j)
	if state != "collected" { return supportUnavailable("agent", state) }
	if j.Schema != 1 || j.PlanSHA256 != sha || !supportOneOf(j.State, "pending", "installing", "failed", "installed-awaiting-enrollment") {
		return supportUnavailable("agent", "invalid")
	}
	seen := map[string]bool{}
	for _, path := range j.Completed {
		if seen[path] || !supportOneOf(path, "/usr/local/bin/hypermesh-agent", "/usr/local/lib/hypermesh/update-rollback",
			"/etc/hypermesh/update-pubkey.asc", "/etc/hypermesh/agent.env", "/etc/systemd/system/hypermesh-agent.service",
			"/etc/systemd/system/hypermesh-wg.service") { return supportUnavailable("agent", "invalid") }
		seen[path] = true
	}

	if !j.Started && len(j.Completed)>0 { return supportUnavailable("agent", "invalid") }
	if j.State=="installing" && !j.Started { return supportUnavailable("agent", "invalid") }
	if j.State=="pending" && (j.Started || len(j.Completed)>0) { return supportUnavailable("agent", "invalid") }
	if j.State=="installed-awaiting-enrollment" {
		if !j.Started { return supportUnavailable("agent", "invalid") }
		// Every mode installs these five payloads. The optional userspace unit
		// cannot be inferred from a journal alone; this is not plan verification.
		for _, path := range []string{"/usr/local/bin/hypermesh-agent", "/usr/local/lib/hypermesh/update-rollback",
			"/etc/hypermesh/update-pubkey.asc", "/etc/hypermesh/agent.env", "/etc/systemd/system/hypermesh-agent.service"} {
			if !seen[path] { return supportUnavailable("agent", "invalid") }
		}
	}
	action := "Preserve the installation journal and retry install-agent with the same approved plan after resolving local conflicts."
	if j.State == "installed-awaiting-enrollment" { action = "Agent files were recorded installed; use register-agent for browser-approved enrollment." }
	return supportStage{Component:"agent", Collection:"collected", State:j.State, ErrorRecorded:j.Error!="", CompletedFiles:len(j.Completed), NextAction:action}
}
func supportRegistration(dir, sha string) supportStage {
	if sha == "" { return supportUnavailable("registration", "not-requested") }
	var j registrationJournal
	state := supportRead(dir, "registration-"+sha+".json", &j)
	if state != "collected" { return supportUnavailable("registration", state) }
	if j.Schema != 1 || j.PlanSHA256 != sha || !supportOneOf(j.State, "prepared", "pending", "approved", "completed", "registered-inactive", "expired", "denied", "retired") {
		return supportUnavailable("registration", "invalid")
	}
	action := "Rerun register-agent with the same plan and owner login; do not delete or share the private journal."
	switch j.State {
	case "denied", "expired": action = "Use register-agent --restart-approval with the same owner to request fresh browser approval and keys."
	case "retired": action = "This identity was retired; preserve it and use the fresh reinstall workflow when available."
	case "registered-inactive": action = "Registration was recorded complete; run check-agent and complete the remaining onboarding gates."
	}
	return supportStage{Component:"registration", Collection:"collected", State:j.State, NextAction:action}
}
func supportOnboarding(dir, sha string) supportStage {
	if sha == "" { return supportUnavailable("onboarding", "not-requested") }
	var j onboardingJournal
	state := supportRead(dir, "onboarding-"+sha+".json", &j)
	if state != "collected" { return supportUnavailable("onboarding", state) }
	if j.Schema != 1 || j.PlanSHA256 != sha || !supportOneOf(j.State, "checking", "checked", "failed") ||
		j.ActivationAllowed || j.SellingAllowed || j.RecordedAt.IsZero() || (!validHeartbeatJournal(j) || !validWireGuardJournal(j)) {
		return supportUnavailable("onboarding", "invalid")
	}
	code := j.FailureCode
	if !supportOneOf(code, "", "local_identity", "local_preflight", "plane_check", "interrupted", "status_persistence", "aiuditor_program", "heartbeat", "wireguard") { code = "unknown" }
	seen := map[string]bool{}
	gates := []supportGate{}
	for _, gate := range j.Gates {
		if seen[gate.ID] || !supportOneOf(gate.ID, "control_plane", "local_preflight", "class", "path_a", "path_b",
			"schedule", "heartbeat", "aiuditor", "tunnel", "activation", "selling") ||
			!supportOneOf(gate.State, "passed", "pending", "blocked") {
			return supportUnavailable("onboarding", "invalid")
		}
		seen[gate.ID] = true
		gates = append(gates, supportGate{gate.ID, gate.State})
	}
	action := "Rerun check-agent for current evidence; this saved observation cannot authorize activation or selling."
	switch code {
	case "local_identity": action = "Inspect changed installation or identity files locally; preserve registration and keys."
	case "local_preflight": action = "Run plan-setup to inspect current hardware, resource and prerequisite blockers."
	case "plane_check": action = "Check the deployed onboarding API and network connection; retry without deleting credentials."
	case "aiuditor_program": action = "Check the plane AIuditor configuration, service authentication and tenant/class suite; rerun check-agent for a specific blocker."
	case "heartbeat": action = "Heartbeat is unconfirmed; check the plane and dashboard holds, then rerun check-agent --heartbeat with the same approved plan and identity."
	case "wireguard": action = "Check gateway peer allocation and UDP/overlay reachability; rerun check-agent --wireguard with the same approved plan and identity."
	case "status_persistence": action = "Check disk space and state-directory permissions, then rerun check-agent."
	}
	recorded := j.RecordedAt.UTC()
	utc := func(t *time.Time)*time.Time { if t == nil { return nil }; v := t.UTC(); return &v }
	return supportStage{Component:"onboarding", Collection:"collected", State:j.State, FailureCode:code,
		ErrorRecorded:code!="" || j.State=="failed", RecordedAt:&recorded, PlaneCheckedAt:utc(j.PlaneCheckedAt),
		LastHeartbeatAt:utc(j.LastHeartbeatAt), HeartbeatRecordedAt:utc(j.HeartbeatRecordedAt), Gates:gates, NextAction:action}
}

// Acquire the existing apply lock without creating state. Holding a shared lock
// excludes every cooperating setup/install/registration/check writer.
func supportLock(dir string) (func(), error) {
	if secureSetupDirectory(dir, false) != nil { return nil, fmt.Errorf("installer state directory is missing or unsafe") }
	path := filepath.Join(dir, "apply.lock")
	before, err := os.Lstat(path)
	if err != nil || !privateSetupFile(before) { return nil, fmt.Errorf("installer lock is missing or unsafe") }
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil { return nil, fmt.Errorf("cannot open installer lock") }
	after, err := f.Stat()
	if err != nil || !privateSetupFile(after) || !os.SameFile(before, after) {
		f.Close(); return nil, fmt.Errorf("installer lock changed while opening")
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) != nil {
		f.Close(); return nil, fmt.Errorf("installer is busy; retry support-bundle after the current operation finishes")
	}
	return func(){ _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

func supportRuntimeMemory(dir,sha string)supportStage {
 if sha==""{return supportUnavailable("runtime-memory","not-requested")}
 var j runtimeMemoryJournal
 state:=supportRead(dir,"runtime-memory-"+sha+".json",&j)
 if state!="collected"{return supportUnavailable("runtime-memory",state)}
 if j.SetupPlanSHA256!=sha||!validMemoryJournal(j){return supportUnavailable("runtime-memory","invalid")}
 at:=j.RecordedAt.UTC()
 return supportStage{Component:"runtime-memory",Collection:"collected",State:j.State,
  RecordedAt:&at,ErrorRecorded:j.FailureCode!="",FailureCode:j.FailureCode,
  MemoryAllocationBytes:j.Request.AllocationBytes,MemoryBackend:j.Request.Backend,
  MemoryVerified:j.State=="verified",NextAction:memoryNextAction(j.State)}
}


type supportWorkloadEvidence struct {
 ContextTokens int `json:"context_tokens"`
 FullContextRequested bool `json:"full_context_requested"`
 InferenceVerified bool `json:"inference_verified"`
 CleanupComplete bool `json:"cleanup_complete"`
 SampleMS int `json:"sample_ms,omitempty"`
 TokensPerSecond float64 `json:"tokens_per_second,omitempty"`
 FullContextVerified bool `json:"full_context_verified"`
 PromptTokens int `json:"prompt_tokens,omitempty"`
 GeneratedTokens int `json:"generated_tokens,omitempty"`
 ContextElapsedMS int64 `json:"context_elapsed_ms,omitempty"`
}

// Export measurements only. Plans contain local paths, catalog names and
// registry addresses that must not enter a shareable support artifact.
func supportWorkload(dir, sha, setupSHA string) supportStage {
 if sha == "" { return supportUnavailable("workload", "not-requested") }
 var j workloadJournal
 state := supportRead(dir, "workload-"+sha+".json", &j)
 if state != "collected" { return supportUnavailable("workload", state) }
 if j.Plan.SHA256 != sha || !validWorkloadJournal(j) {
  return supportUnavailable("workload", "invalid")
 }
 if setupSHA != "" && j.Plan.Setup.PlanSHA256 != setupSHA {
  stage := supportUnavailable("workload", "invalid")
  stage.NextAction = "The selected workload belongs to a different prerequisite plan. Select matching plan digests; preserve the original journals."
  return stage
 }
 at := j.RecordedAt.UTC()
 evidence := &supportWorkloadEvidence{
  ContextTokens:j.Plan.Request.ContextTokens,
  FullContextRequested:j.Plan.Request.FullContext,
 }
 if j.State == "verified" {
  r := j.Receipt
  evidence.InferenceVerified = true
  evidence.CleanupComplete = r.CleanupComplete
  evidence.SampleMS = r.SampleMS
  evidence.TokensPerSecond = r.TokensPerSecond
  if r.ContextFit != nil {
   evidence.FullContextVerified = true
   evidence.PromptTokens = r.ContextFit.PromptTokens
   evidence.GeneratedTokens = r.ContextFit.GeneratedTokens
   evidence.ContextElapsedMS = r.ContextFit.ElapsedMS
  }
 }
 action := workloadNext(j.State)
 switch j.FailureCode {
 case "preflight":
  action = "Inspect current prerequisites, models-filesystem space, service inactivity and existing lease state. Preserve enrollment and rerun the reviewed workload after resolving blockers."
 case "runtime":
  action = "Inspect the approved model hash, runtime image, drivers and temporary container locally. Cleanup is unconfirmed; reconcile the named container before retrying the reviewed workload."
 case "interrupted":
  action = "Execution was interrupted. Confirm the prior process has exited and reconcile its temporary container before retrying; preserve installer and enrollment state."
 case "status_persistence":
  action = "Check installer-state disk space and permissions, then reconcile temporary containers and retry the reviewed workload. No successful receipt was saved."
 }
 return supportStage{Component:"workload", Collection:"collected", State:j.State,
  ErrorRecorded:j.State=="failed", FailureCode:j.FailureCode, RecordedAt:&at,
  Workload:evidence, NextAction:action}
}

func collectSupport(ctx context.Context, dir, agentSHA, setupSHA string) (supportBundle, error) {
 return collectSupportWithWorkload(ctx,dir,agentSHA,setupSHA,"")
}

func collectSupportWithWorkload(ctx context.Context, dir, agentSHA, setupSHA, workloadSHA string) (supportBundle, error) {
 return collectSupportWithCertification(ctx,dir,agentSHA,setupSHA,workloadSHA,"")
}

func collectSupportWithCertification(ctx context.Context, dir, agentSHA, setupSHA, workloadSHA, certificationSHA string) (supportBundle, error) {
 return collectSupportWithReboot(ctx,dir,agentSHA,setupSHA,workloadSHA,certificationSHA,"")
}
func collectSupportWithReboot(ctx context.Context, dir, agentSHA, setupSHA, workloadSHA, certificationSHA,rebootSHA string) (supportBundle,error) {
	var bundle supportBundle
	if (agentSHA=="" && setupSHA=="" && workloadSHA=="" && certificationSHA=="" && rebootSHA=="") || (agentSHA!="" && !setupDigestRE.MatchString(agentSHA)) ||
		(setupSHA!="" && !setupDigestRE.MatchString(setupSHA)) ||
        (workloadSHA!="" && !setupDigestRE.MatchString(workloadSHA)) ||
        (certificationSHA!="" && !setupDigestRE.MatchString(certificationSHA)) ||
        (rebootSHA!="" && !setupDigestRE.MatchString(rebootSHA)) { return bundle, fmt.Errorf("select valid installer plan digests") }
	unlock, err := supportLock(dir)
	if err != nil { return bundle, err }
	defer unlock()
	if ctx.Err()!=nil { return bundle, fmt.Errorf("support collection interrupted") }
	commit := buildinfo.Current()
	if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(commit) { commit = "unknown" }
	bundle = supportBundle{Schema:1, GeneratedAt:time.Now().UTC(), CollectorCommit:commit, CollectorBuild:buildinfo.CurrentBuild(),
		Architecture:runtime.GOARCH, AgentPlanSHA256:agentSHA, SetupPlanSHA256:setupSHA, WorkloadPlanSHA256:workloadSHA, CertificationPlanSHA256:certificationSHA, RebootPlanSHA256:rebootSHA, HistoricalOnly:true, CollectionComplete:true}
	bundle.Stages = []supportStage{supportSetup(dir,setupSHA), supportAgent(dir,agentSHA), supportRegistration(dir,agentSHA), supportOnboarding(dir,agentSHA), supportRuntimeMemory(dir,setupSHA),supportWorkload(dir,workloadSHA,setupSHA),supportCertification(dir,certificationSHA),supportReboot(dir,rebootSHA)}
	for _, stage := range bundle.Stages {
		if stage.Collection!="collected" && stage.Collection!="not-requested" { bundle.CollectionComplete=false }
	}
	if ctx.Err()!=nil { return supportBundle{}, fmt.Errorf("support collection interrupted") }
	return bundle, nil
}

// Publish a complete 0600 file without replacing an existing destination. The
// output directory must already be owner-controlled, like installer state.
func writeSupport(ctx context.Context, path string, bundle supportBundle) error {
	if !filepath.IsAbs(path) || filepath.Clean(path)!=path || agentDirectory(filepath.Dir(path),false)!=nil {
		return fmt.Errorf("support output requires an absolute path in an existing owner-controlled directory")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) { return fmt.Errorf("support output already exists or cannot be inspected; choose a new file") }
	raw, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil { return fmt.Errorf("cannot encode support bundle") }
	f, err := os.CreateTemp(filepath.Dir(path), ".hypermesh-support-")
	if err != nil { return fmt.Errorf("cannot stage support bundle") }
	defer os.Remove(f.Name())
	if _, err=f.Write(append(raw,'\n')); err!=nil { f.Close(); return fmt.Errorf("cannot write support bundle") }
	if f.Sync()!=nil { f.Close(); return fmt.Errorf("cannot sync support bundle") }
	if f.Close()!=nil { return fmt.Errorf("cannot close support bundle") }
	if ctx.Err()!=nil { return fmt.Errorf("support export interrupted") }
	if os.Link(f.Name(),path)!=nil { return fmt.Errorf("cannot publish support bundle without replacing an existing file") }
	if os.Remove(f.Name())!=nil { return fmt.Errorf("support file published but staging cleanup failed; inspect the output directory") }
	d, err := os.Open(filepath.Dir(path))
	if err!=nil { return fmt.Errorf("support file published but durability could not be confirmed") }
	defer d.Close()
	if d.Sync()!=nil { return fmt.Errorf("support file published but durability could not be confirmed") }
	return nil
}

func RunSupportBundle(ctx context.Context, args []string, out, diagnostic io.Writer) int {
	return runSupportBundle(ctx,args,out,diagnostic,defaultSetupState,os.Geteuid()==0)
}
func runSupportBundle(ctx context.Context,args []string,out,diagnostic io.Writer,stateDir string,root bool) int {
	flags:=flag.NewFlagSet("support-bundle",flag.ContinueOnError)
	flags.SetOutput(diagnostic)
	agentSHA:=flags.String("plan-sha","","agent plan digest; includes install, registration and onboarding")
	setupSHA:=flags.String("setup-plan-sha","","optional prerequisite plan digest")
	workloadSHA:=flags.String("workload-plan-sha","","optional workload plan digest; includes pinned inference and context evidence")
	certificationSHA:=flags.String("certification-plan-sha","","optional certification plan digest; includes acquisition and activation recovery")
	rebootSHA:=flags.String("reboot-plan-sha","","optional prerequisite reboot plan digest")
	output:=flags.String("output","","new absolute output file in an owner-controlled directory (0600)")
	if err:=flags.Parse(args);err!=nil { if err==flag.ErrHelp{return 0};return 2 }
	if flags.NArg()!=0 || *output=="" || (*agentSHA=="" && *setupSHA=="" && *workloadSHA=="" && *certificationSHA=="" && *rebootSHA=="") ||
		(*agentSHA!="" && !setupDigestRE.MatchString(*agentSHA)) || (*setupSHA!="" && !setupDigestRE.MatchString(*setupSHA)) || (*workloadSHA!="" && !setupDigestRE.MatchString(*workloadSHA)) || (*certificationSHA!="" && !setupDigestRE.MatchString(*certificationSHA)) || (*rebootSHA!="" && !setupDigestRE.MatchString(*rebootSHA)) {
		fmt.Fprintln(diagnostic,"require --output and at least one of --plan-sha, --setup-plan-sha, --workload-plan-sha, --certification-plan-sha or --reboot-plan-sha");return 2
	}
	if !root { fmt.Fprintln(diagnostic,"support-bundle requires root to read private installer journals");return 1 }
	bundle,err:=collectSupportWithReboot(ctx,stateDir,*agentSHA,*setupSHA,*workloadSHA,*certificationSHA,*rebootSHA)
	if err==nil { err=writeSupport(ctx,*output,bundle) }
	if err!=nil { fmt.Fprintln(diagnostic,err);return 1 }
	if !bundle.CollectionComplete { fmt.Fprintln(diagnostic,"Partial redacted bundle saved; see each stage's collection status. Preserve the original journals locally.");return 1 }
	if _,err=fmt.Fprintln(out,"Redacted historical support bundle saved. This does not verify current readiness.");err!=nil{return 1}
	return 0
}
