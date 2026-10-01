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
 "time"

 "github.com/FyberLabs/hypermesh-host/internal/hostregistration"
)

type onboardingGate struct {
 ID string `json:"id"`
 State string `json:"state"`
 NextAction string `json:"next_action"`
}
type onboardingJournal struct {
 Schema int `json:"schema"`
 PlanSHA256 string `json:"plan_sha256"`
 RequestID string `json:"request_id"`
 DeviceID string `json:"device_id"`
 HostName string `json:"host_name"`
 State string `json:"state"`
 RecordedAt time.Time `json:"recorded_at"`
 PlaneCheckedAt *time.Time `json:"plane_checked_at,omitempty"`
 LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty"`
 ProtocolVerified bool `json:"protocol_verified"`
 LocalPreflightPassed bool `json:"local_preflight_passed"`
 ReportedSellState string `json:"reported_sell_state,omitempty"`
 ActivationAllowed bool `json:"activation_allowed"`
 SellingAllowed bool `json:"selling_allowed"`
 Gates []onboardingGate `json:"gates"`
 FailureCode string `json:"failure_code,omitempty"`
 NextAction string `json:"next_action"`
}
func onboardingPath(state,sha string)string{return filepath.Join(state,"onboarding-"+sha+".json")}

func saveOnboarding(path string,j onboardingJournal)error {
 if err:=secureSetupDirectory(filepath.Dir(path),false);err!=nil{return err}
 if _,err:=os.Lstat(path);err==nil {
  if _,err=readSetupRegular(path,true);err!=nil{return fmt.Errorf("unsafe existing onboarding journal")}
 } else if !os.IsNotExist(err){return fmt.Errorf("cannot inspect onboarding journal")}
 raw,err:=json.Marshal(j);if err!=nil{return fmt.Errorf("cannot encode onboarding status")}
 f,err:=os.CreateTemp(filepath.Dir(path),".onboarding-");if err!=nil{return fmt.Errorf("cannot stage onboarding status")}
 defer os.Remove(f.Name())
 if _,err=f.Write(raw);err!=nil{f.Close();return fmt.Errorf("cannot write onboarding status")}
 if err=f.Sync();err!=nil{f.Close();return fmt.Errorf("cannot sync onboarding status")}
 if err=f.Close();err!=nil{return fmt.Errorf("cannot close onboarding status")}
 if err=os.Rename(f.Name(),path);err!=nil{return fmt.Errorf("cannot publish onboarding status")}
 dir,err:=os.Open(filepath.Dir(path));if err!=nil{return fmt.Errorf("cannot open onboarding directory")};defer dir.Close()
 if dir.Sync()!=nil{return fmt.Errorf("cannot sync onboarding directory")}
 return nil
}

// Gate messages are local constants; never echo arbitrary plane response text.
// A snapshot cannot authorize activation, even when the plane reports green.
func onboardingGates(s hostregistration.OnboardingSnapshot)[]onboardingGate {
 gates:=[]onboardingGate{
  {"control_plane","passed","Saved device credential and registration binding verified over HTTPS."},
  {"local_preflight","passed","Current resource, hardware and approved prerequisite checks passed."},
  {"class","passed","Platform class is configured."},
 }
 if !*s.ClassAvailable {gates[2]=onboardingGate{"class","blocked","Platform must restore or admit the requested hardware class."}}
 a:=onboardingGate{"path_a","pending","Platform hardware intake approval is still required."}
 switch s.PathAState {case "accepted":a=onboardingGate{"path_a","passed","Plane reports accepted hardware intake."};case "rejected","unknown":a=onboardingGate{"path_a","blocked","Review hardware intake with the platform; do not bypass it."}}
 gates=append(gates,a)
 b:=onboardingGate{"path_b","pending","Run the approved certification workflow; no benchmark result is recorded."}
 switch s.PathBState {case "green":b=onboardingGate{"path_b","passed","Plane reports green certification; this check did not rerun the benchmark."};case "suspended","unknown":b=onboardingGate{"path_b","blocked","Resolve certification suspension or unknown state before offering workloads."}}
 gates=append(gates,b)
 hold:=onboardingGate{"schedule","passed","No scheduling hold is reported."}
 if *s.ScheduleHold {hold=onboardingGate{"schedule","blocked","Review the hold reason and recovery action in the host dashboard."}}
 gates=append(gates,hold,
  onboardingGate{"heartbeat","pending","Agent heartbeat and current liveness must still be verified; this read sends no heartbeat."},
  onboardingGate{"aiuditor","pending","Authenticated AIuditor program and certification connectivity checks remain required."},
  onboardingGate{"tunnel","pending","An authenticated WireGuard handshake and data path still need verification."},
  onboardingGate{"activation","pending","Service activation requires the remaining onboarding workflow; leave services inactive."},
  onboardingGate{"selling","blocked","Selling remains disabled until certification, connectivity and owner approval are complete."})
 return gates
}
type onboardingDeps struct {
 Verify func(context.Context)error
 Preflight func(context.Context)error
 Remote func(context.Context)(hostregistration.OnboardingSnapshot,error)
 Save func(onboardingJournal)error
}
func performOnboardingCheck(ctx context.Context,j *onboardingJournal,deps onboardingDeps)error {
 // Replace previous success before probing. Crash/cancellation can leave only
 // 'checking', never make old evidence look like the latest successful check.
 j.State="checking";j.RecordedAt=time.Now().UTC()
 j.PlaneCheckedAt=nil;j.LastHeartbeatAt=nil;j.ProtocolVerified=false;j.LocalPreflightPassed=false
 j.ActivationAllowed=false;j.SellingAllowed=false;j.ReportedSellState="";j.Gates=nil;j.FailureCode=""
 j.NextAction="Check is in progress or was interrupted; rerun check-agent with the same approved plan."
 if err:=deps.Save(*j);err!=nil{return err}
 fail:=func(code,message string,err error)error{
  j.State="failed";j.FailureCode=code;j.RecordedAt=time.Now().UTC();j.NextAction=message
  if saveErr:=deps.Save(*j);saveErr!=nil{return fmt.Errorf("check failed and status could not be saved; preserve installer state")}
  return err
 }
 if err:=deps.Verify(ctx);err!=nil{return fail("local_identity","Repair changed local installation or identity files; preserve registration state.",err)}
 if err:=deps.Preflight(ctx);err!=nil{return fail("local_preflight","Run plan-setup to inspect current hardware, resource and prerequisite blockers; preserve registration.",err)}
 j.LocalPreflightPassed=true
 snapshot,err:=deps.Remote(ctx)
 if err!=nil{return fail("plane_check","Verify the deployed onboarding API, connection and device registration; retry without deleting credentials.",err)}
 if err=ctx.Err();err!=nil{return fail("interrupted","Check interrupted; rerun with the same approved plan.",fmt.Errorf("onboarding check interrupted"))}
 j.State="checked";j.RecordedAt=time.Now().UTC();j.ProtocolVerified=true
 j.PlaneCheckedAt=&snapshot.CheckedAt;j.LastHeartbeatAt=snapshot.LastHeartbeatAt
 j.Gates=onboardingGates(snapshot);j.ReportedSellState=snapshot.SellState
 j.NextAction="Authenticated API and local preflight checks passed. Complete the listed onboarding gates before activation or selling."
 return deps.Save(*j)
}

func RunCheckAgent(ctx context.Context,args []string,out,diagnostic io.Writer)int {
 flags:=flag.NewFlagSet("check-agent",flag.ContinueOnError);flags.SetOutput(diagnostic)
 path:=flags.String("plan","","installed agent plan")
 approved:=flags.String("approve-plan","","exact agent plan_sha256")
 if err:=flags.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
 if flags.NArg()!=0||*path==""||!setupDigestRE.MatchString(*approved){fmt.Fprintln(diagnostic,"require --plan and --approve-plan");return 2}
 if os.Geteuid()!=0{fmt.Fprintln(diagnostic,"check-agent requires root to read the saved private identity");return 1}
 var p agentInstallPlan
 raw,err:=readSetupRegular(*path,false)
 if err==nil{err=decodeSetupJSON(raw,&p)}
 if err==nil&&(p.SHA256!=*approved||agentPlanDigest(p)!=*approved){err=fmt.Errorf("agent approval digest does not match")}
 if err==nil{err=validateAgentInputs(p)}
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 unlock,err:=lockSetupState(defaultSetupState)
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1};defer unlock()
 ctx,cancel:=context.WithTimeout(ctx,3*time.Minute);defer cancel()
 var registration registrationJournal
 raw,err=readSetupRegular(registrationPath(defaultSetupState,p.SHA256),true)
 if err==nil{err=decodeSetupJSON(raw,&registration)}
 if err==nil{err=validateRegistration(registration,p,registration.Issuer,registration.Dashboard,registration.Request.ClassID)}
 if err!=nil||registration.State!="registered-inactive"{
  fmt.Fprintln(diagnostic,"complete register-agent first; registration must match the installed plan and remain inactive");return 1
 }
 client,err:=hostregistration.New(p.ControlPlane,p.TenantID)
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 j:=onboardingJournal{Schema:1,PlanSHA256:p.SHA256,RequestID:registration.Request.RequestID,DeviceID:registration.DeviceID,HostName:p.HostName}
 deps:=onboardingDeps{
  Verify:func(ctx context.Context)error{
   if err:=verifyRegistrationService(ctx,p);err!=nil{return err}
   return checkRegistrationIdentity("",registration,true)
  },
  Preflight:func(ctx context.Context)error{_,err:=registrationPreflight(ctx,p,registration.Request.ClassID);return err},
  Remote:func(ctx context.Context)(hostregistration.OnboardingSnapshot,error){return client.CheckDevice(ctx,registration.Request,registration.DeviceID,registration.DeviceSecret)},
  Save:func(value onboardingJournal)error{return saveOnboarding(onboardingPath(defaultSetupState,p.SHA256),value)},
 }
 err=performOnboardingCheck(ctx,&j,deps)
 if json.NewEncoder(out).Encode(j)!=nil{return 1}
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 return 0
}
func RunOnboardingStatus(_ context.Context,args []string,out,diagnostic io.Writer)int {
 flags:=flag.NewFlagSet("onboarding-status",flag.ContinueOnError);flags.SetOutput(diagnostic)
 digest:=flags.String("plan-sha","","agent plan_sha256")
 if err:=flags.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
 if flags.NArg()!=0||!setupDigestRE.MatchString(*digest){fmt.Fprintln(diagnostic,"require --plan-sha SHA256");return 2}
 if err:=secureSetupDirectory(defaultSetupState,false);err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 raw,err:=readSetupRegular(onboardingPath(defaultSetupState,*digest),true)
 var j onboardingJournal
 if err==nil{err=decodeSetupJSON(raw,&j)}
 if err!=nil||j.Schema!=1||j.PlanSHA256!=*digest||!hostregistration.UUID.MatchString(j.RequestID)||
 !hostregistration.UUID.MatchString(j.DeviceID)||!agentNameRE.MatchString(j.HostName)||j.ActivationAllowed||j.SellingAllowed||
 (j.State!="checking"&&j.State!="checked"&&j.State!="failed"){
  fmt.Fprintln(diagnostic,"onboarding status unavailable or invalid");return 1
 }
 // This is a dated local observation, not a live readiness or activation claim.
 if json.NewEncoder(out).Encode(j)!=nil{return 1}
 return 0
}
