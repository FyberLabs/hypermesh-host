//go:build linux

package installer

import (
 "context"
 "encoding/json"
 "flag"
 "fmt"
 "io"
 "math"
 "os"
 "regexp"
 "strings"

 "github.com/FyberLabs/hypermesh-host/internal/hostregistration"
 "github.com/FyberLabs/hypermesh-host/internal/pathb"
)

// These are recorded observations, not a current authorization or service probe.
// No run/device identity, source URL, runtime address or model hash is exported.
type supportCertificationEvidence struct {
 ContextTokens int `json:"context_tokens"`
 PromptTokens int `json:"prompt_tokens"`
 GeneratedTokens int `json:"generated_tokens"`
 ContextElapsedMS int64 `json:"context_elapsed_ms"`
 SampleMS int `json:"sample_ms"`
 TokensPerSecond float64 `json:"tokens_per_second"`
 ThermalC *float64 `json:"thermal_c,omitempty"`
 CleanupRecorded bool `json:"cleanup_recorded"`
 TunnelRecorded bool `json:"tunnel_recorded"`
 ResultAccepted bool `json:"result_accepted"`
 ClassPassed bool `json:"class_passed"`
 StartupRecorded bool `json:"startup_recorded"`
}

func validSupportCertificationEvidence(e hostregistration.CertificationEvidence) bool {
 hash := func(value string) bool {
  return strings.HasPrefix(value,"sha256:") && setupDigestRE.MatchString(strings.TrimPrefix(value,"sha256:"))
 }
 fit := pathb.ContextFit{Protocol:pathb.ContextFitProtocol,ContextTokens:e.ContextTokens,
  PromptTokens:e.PromptTokens,GeneratedTokens:e.GeneratedTokens,ElapsedMS:e.ContextElapsedMS}
 return hash(e.HostImageHash) && hash(e.FileHash) &&
  regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]*@sha256:[a-f0-9]{64}$`).MatchString(e.ImageDigest) &&
  e.SampleMS>0 && e.TokensPerSecond>0 && !math.IsNaN(e.TokensPerSecond) && !math.IsInf(e.TokensPerSecond,0) &&
  (e.ThermalC==nil || (!math.IsNaN(*e.ThermalC) && !math.IsInf(*e.ThermalC,0))) &&
  fit.ValidFor(e.ContextTokens) && e.CleanupComplete && e.TunnelVerified
}

// A parsed success label alone must never produce success/retry guidance.
// The private journal does not embed the plan; this validates internal shape,
// not the original manifest or the plane's current verdict.
func validSupportCertification(j certificationJournal) bool {
 if !validCertificationJournal(j) { return false }
 if j.Evidence!=nil && !validSupportCertificationEvidence(*j.Evidence) { return false }
 switch j.State {
 case "running":
  return j.Evidence==nil && j.FailureCode==""
 case "evidence-ready","active":
  return j.Evidence!=nil && j.FailureCode==""
 case "passed":
  return j.Evidence!=nil && supportOneOf(j.FailureCode,"","service_start")
 case "activating":
  return j.Evidence!=nil && supportOneOf(j.FailureCode,"","cleanup")
 case "failed":
  if j.FailureCode=="class_band" { return j.Evidence!=nil }
  return j.Evidence==nil && supportOneOf(j.FailureCode,"preflight","acquisition","workload","tunnel","postflight")
 }
 return false
}

func certificationRecoveryAction(state, code string) string {
 switch state {
 case "running":
  return "Certification was interrupted or its final status was not saved. Confirm the previous process has exited and reconcile temporary containers before retrying certify-agent with the same reviewed, unexpired plan."
 case "evidence-ready":
  return "The result upload is unconfirmed. Retain this exact journal and retry certify-agent with the same reviewed plan to resend identical evidence. If the plane rejects expiry or changed pins, preserve this run and prepare a new reviewed plan."
 case "passed":
  if code=="service_start" {
   return "Startup failed and rollback was recorded complete. Inspect service diagnostics locally, then retry activate-agent with the same reviewed plan and original owner. An expired run needs a new reviewed certification."
  }
  return "Certification passed historically. Use activate-agent with the same reviewed plan for fresh owner approval and current gates; an expired run needs a new reviewed certification."
 case "activating":
  return "Activation or rollback is unconfirmed. Inspect service state and rerun activate-agent with the same reviewed plan to reconcile its matching marker. Do not delete journals or create an activation marker by hand."
 case "active":
  return "Stable paused startup was recorded at this timestamp. Check current service and dashboard state, heartbeat and scheduling restrictions before changing selling posture. This record does not prove current uptime, connectivity or permission to sell."
 case "failed":
  switch code {
  case "class_band":
   return "The plane rejected the measured class envelope. Review hardware and the configured class baseline, then prepare a new reviewed run; this run is terminal."
  case "acquisition":
   return "Check model access/license, immutable source pins, models-filesystem capacity and plan expiry locally. Preserve the cache and identity; retry the reviewed unexpired plan after resolving the blocker."
  case "workload":
   return "Inspect model/runtime compatibility and reconcile temporary containers; cleanup is unconfirmed. Retry the reviewed unexpired plan only after resolving the workload failure."
  case "tunnel":
   return "Check gateway allocation and UDP/overlay connectivity. Preserve enrollment and keys; retry the reviewed unexpired plan after resolving connectivity and reconciling temporary resources."
  default:
   return "Inspect current prerequisites, service inactivity, scheduling restrictions, lease state and models-filesystem resources. Preserve enrollment; retry the reviewed unexpired plan after resolving blockers."
  }
 }
 return "Preserve installer state and inspect this journal locally."
}

func supportCertification(dir, sha string) supportStage {
 if sha=="" { return supportUnavailable("certification","not-requested") }
 var j certificationJournal
 state:=supportRead(dir,"certification-"+sha+".json",&j)
 if state!="collected" { return supportUnavailable("certification",state) }
 if j.PlanSHA256!=sha || !validSupportCertification(j) { return supportUnavailable("certification","invalid") }
 at:=j.RecordedAt.UTC()
 stage:=supportStage{Component:"certification",Collection:"collected",State:j.State,
  FailureCode:j.FailureCode,ErrorRecorded:j.FailureCode!="",RecordedAt:&at,
  NextAction:certificationRecoveryAction(j.State,j.FailureCode)}
 if j.Evidence!=nil {
  e:=j.Evidence
  stage.Certification=&supportCertificationEvidence{
   ContextTokens:e.ContextTokens,PromptTokens:e.PromptTokens,GeneratedTokens:e.GeneratedTokens,
   ContextElapsedMS:e.ContextElapsedMS,SampleMS:e.SampleMS,TokensPerSecond:e.TokensPerSecond,
   ThermalC:e.ThermalC,CleanupRecorded:e.CleanupComplete,TunnelRecorded:e.TunnelVerified,
   ResultAccepted:j.State!="evidence-ready",ClassPassed:supportOneOf(j.State,"passed","activating","active"),
   StartupRecorded:j.State=="active",
  }
 }
 return stage
}

func RunCertificationStatus(ctx context.Context,args []string,out,diagnostic io.Writer) int {
 return runCertificationStatus(ctx,args,out,diagnostic,defaultSetupState,os.Geteuid()==0)
}
func runCertificationStatus(ctx context.Context,args []string,out,diagnostic io.Writer,stateDir string,root bool) int {
 f:=flag.NewFlagSet("certification-status",flag.ContinueOnError);f.SetOutput(diagnostic)
 sha:=f.String("plan-sha","","certification plan digest")
 if err:=f.Parse(args);err!=nil { if err==flag.ErrHelp{return 0};return 2 }
 if f.NArg()!=0 || !setupDigestRE.MatchString(*sha) { fmt.Fprintln(diagnostic,"require --plan-sha SHA256");return 2 }
 if !root { fmt.Fprintln(diagnostic,"certification-status requires root to read private installer journals");return 1 }
 bundle,err:=collectSupportWithCertification(ctx,stateDir,"","","",*sha)
 if err!=nil { fmt.Fprintln(diagnostic,err);return 1 }
 if json.NewEncoder(out).Encode(bundle)!=nil { return 1 }
 if !bundle.CollectionComplete { fmt.Fprintln(diagnostic,"Certification journal unavailable or invalid; preserve state and inspect locally.");return 1 }
 return 0
}
