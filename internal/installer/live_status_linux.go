//go:build linux

package installer

import (
 "context"
 "encoding/json"
 "flag"
 "fmt"
 "io"
 "os"
 "os/exec"
 "syscall"
 "strings"
 "time"

 "github.com/FyberLabs/hypermesh-host/internal/hostregistration"
)

// A fresh observation never grants an activation permit or permission to sell.
type liveHostStatus struct {
 Schema int `json:"schema"`
 PlanSHA256 string `json:"plan_sha256"`
 ObservedAt time.Time `json:"observed_at"`
 FinishedAt time.Time `json:"finished_at"`
 AttentionRequired bool `json:"attention_required"`
 ActivationAllowed bool `json:"activation_allowed"`
 SellingAllowed bool `json:"selling_allowed"`
 TunnelProbed bool `json:"tunnel_probed"`
 HeartbeatMaxAgeSeconds int `json:"heartbeat_max_age_seconds"`
 HeartbeatAgeSeconds *int64 `json:"heartbeat_age_seconds,omitempty"`
 PlaneSellState string `json:"plane_sell_state,omitempty"`
 Certification supportStage `json:"certification_history"`
 Gates []onboardingGate `json:"gates"`
}
type liveStatusDeps struct {
 Installation func(context.Context) error
 Identity func() error
 Certification func() supportStage
 Marker func() (bool,error)
 Service func(context.Context)([]byte,error)
 Reboot func()(bool,error)
 Remote func(context.Context)(hostregistration.OnboardingSnapshot,error)
 Now func()time.Time
}
func liveGate(id string,ok bool,pass,blocked string) onboardingGate {
 if ok{return onboardingGate{id,"passed",pass}}
 return onboardingGate{id,"blocked",blocked}
}

// Interpret only requested properties. Never print raw systemd output/errors.
func liveService(raw []byte)(serviceInvocation,bool,error) {
 values:=map[string]string{}
 for _,line:=range strings.Split(strings.TrimSpace(string(raw)),"\n"){
  key,value,ok:=strings.Cut(line,"=")
  if !ok || !supportOneOf(key,"LoadState","UnitFileState","ActiveState","SubState","MainPID","NRestarts","InvocationID","NeedDaemonReload","FragmentPath","DropInPaths"){
   return serviceInvocation{},false,fmt.Errorf("unsupported service observation")
  }
  if _,exists:=values[key];exists{return serviceInvocation{},false,fmt.Errorf("duplicate service property")}
  values[key]=value
 }
 if len(values)!=10{return serviceInvocation{},false,fmt.Errorf("incomplete service observation")}
 if values["LoadState"]!="loaded" || values["UnitFileState"]!="enabled" || values["NeedDaemonReload"]!="no" ||
  values["FragmentPath"]!="/etc/systemd/system/hypermesh-agent.service" || values["DropInPaths"]!="" {return serviceInvocation{},false,nil}
 if values["ActiveState"]!="active" || values["SubState"]!="running" {return serviceInvocation{},false,nil}
 v,err:=activeInvocation([]byte("ActiveState=active\nSubState=running\nMainPID="+values["MainPID"]+
  "\nNRestarts="+values["NRestarts"]+"\nInvocationID="+values["InvocationID"]))
 return v,err==nil,err
}
func collectLiveHostStatus(ctx context.Context,p certificationPlan,d liveStatusDeps) liveHostStatus {
 report:=liveHostStatus{Schema:1,PlanSHA256:p.SHA256,ObservedAt:d.Now().UTC(),HeartbeatMaxAgeSeconds:120}
 add:=func(g onboardingGate){report.Gates=append(report.Gates,g);if g.State!="passed"{report.AttentionRequired=true}}
 finish:=func()liveHostStatus{
  report.FinishedAt=d.Now().UTC()
  if ctx.Err()!=nil{add(onboardingGate{"observation","blocked","Status check was interrupted or timed out. Rerun host-status; no host changes were made."})}
  return report
 }
 report.Certification=d.Certification()
 history:=report.Certification
 add(liveGate("certification_history",history.Collection=="collected"&&history.State=="active",
  "Saved certification passed; this historical record is checked separately from current plane state.",
  history.NextAction))
 installed:=d.Installation(ctx)==nil
 add(liveGate("installed_files",installed,"Installed payloads and systemd search paths match the reviewed installation.",
  "Installed files or systemd configuration differ or are unavailable. Preserve state and inspect the approved plan and signed update history; do not reinstall or overwrite keys."))
 identity:=d.Identity()==nil
 add(liveGate("identity",identity,"Local device identity matches the selected registration.",
  "Local registration or identity is missing, retired or changed. Preserve journals and keys; inspect registration-status before repair."))
 marker,markerErr:=d.Marker()
 add(liveGate("activation_marker",markerErr==nil&&marker,"Activation marker matches this certification run.",
  "Activation marker is missing or differs. Consult certification-status and the original reviewed plan; never create or replace the marker by hand."))
 raw,serviceErr:=d.Service(ctx)
 first,running,parseErr:=liveService(raw)
 serviceOK:=serviceErr==nil&&parseErr==nil&&running
 reboot,rebootErr:=d.Reboot()
 add(liveGate("reboot",rebootErr==nil&&!reboot,"No OS reboot-required marker is present.",
  "A reboot is required or its marker cannot be inspected. Confirm there is no active lease, arrange downtime in the dashboard, and review the OS requirement before rebooting."))
 if identity&&ctx.Err()==nil {
  s,err:=d.Remote(ctx)
  now:=d.Now().UTC()
  fresh:=err==nil && !s.CheckedAt.IsZero() && !s.CheckedAt.Before(report.ObservedAt.Add(-30*time.Second)) &&
   !s.CheckedAt.After(now.Add(30*time.Second))
  add(liveGate("control_plane",fresh,"Fresh authenticated device status received from the plane.",
   "Plane status is unavailable, stale or incompatible. Check network, plane deployment and registration in the dashboard; preserve credentials."))
  if fresh {
   add(liveGate("class",s.ClassAvailable!=nil&&*s.ClassAvailable,"Hardware class is configured.","Platform must admit or restore this hardware class."))
   add(liveGate("intake",s.PathAState=="accepted","Plane reports accepted intake.","Review pending or rejected hardware intake in the host dashboard."))
   add(liveGate("certification",s.PathBState=="green","Plane currently reports green certification.","Review current certification status in the dashboard; resolve suspension or complete the reviewed certification flow."))
   add(liveGate("schedule",s.ScheduleHold!=nil&&!*s.ScheduleHold,"No scheduling restriction is reported.","Review the scheduling restriction and recovery action in the host dashboard."))
   add(onboardingProgramGate(s.AIuditorProgramState))
   // Use the plane's own clock for heartbeat age; future timestamps never pass.
   heartbeatOK:=false
   if s.LastHeartbeatAt!=nil {
    delta:=s.CheckedAt.Sub(*s.LastHeartbeatAt)
    if delta>=0 {age:=int64(delta/time.Second);report.HeartbeatAgeSeconds=&age;heartbeatOK=delta<=120*time.Second}
   }
   add(liveGate("heartbeat",heartbeatOK,"The plane has a heartbeat no older than 120 seconds; this is not a tunnel test.",
    "Heartbeat is absent, future-dated or older than 120 seconds. Inspect agent service and connectivity; this status command does not send a heartbeat."))
   if supportOneOf(s.SellState,"paused","selling","draining","off","n/a","unknown"){report.PlaneSellState=s.SellState}
   add(liveGate("selling_state",supportOneOf(s.SellState,"paused","selling","draining","off"),
    "Plane selling posture is recognized; it is reported without changing it or granting permission.",
    "Plane selling posture is unknown or not applicable to a model host. Inspect the host dashboard before changing it."))
  }
 } else {
  add(onboardingGate{"control_plane","blocked","Plane was not queried because local identity was unverified or the check was interrupted."})
 }
 // Bound the observation across network I/O. A restart between samples cannot
 // inherit a prior green process observation.
 if serviceOK {
  after,err:=d.Service(ctx);last,active,e:=liveService(after)
  serviceOK=err==nil&&e==nil&&active&&last==first
 }
 add(liveGate("agent_service",serviceOK,"Agent is enabled and running with the same invocation across this observation.",
  "Agent is stopped, disabled, restarting, has unreviewed or stale loaded configuration, or is unobservable. Inspect systemd locally and certification-status. Use the controlled activation flow for an incomplete startup; do not bypass its approval gates."))
 return finish()
}

func liveCertificationHistory(dir string,p certificationPlan) supportStage {
 stage:=supportCertification(dir,p.SHA256)
 if stage.Collection!="collected"{return stage}
 var j certificationJournal
 if supportRead(dir,"certification-"+p.SHA256+".json",&j)!="collected"||j.RunID!=p.Run.RunID{
  return supportUnavailable("certification","invalid")
 }
 if e:=j.Evidence;e!=nil {
  m:=p.Run.Manifest
  if e.FileHash!=m.FileHash||e.ImageDigest!=m.ImageDigest||e.ContextTokens!=m.ContextTokens{
   return supportUnavailable("certification","invalid")
  }
 }
 return stage
}
func liveRebootRequired(root string)(bool,error) {
 // /var/run commonly aliases /run. Inspect the canonical marker without
 // following a user-controlled marker symlink or reading its arbitrary text.
 info,err:=os.Lstat(agentDest(root,"/run/reboot-required"))
 if os.IsNotExist(err){return false,nil}
 if err!=nil||!info.Mode().IsRegular(){return false,fmt.Errorf("cannot inspect reboot marker")}
 return true,nil
}
func liveIdentity(root,state string,p certificationPlan)(registrationJournal,*hostregistration.Client,error) {
 var j registrationJournal
 raw,err:=readSetupRegular(registrationPath(state,p.Agent.SHA256),true)
 if err==nil{err=decodeSetupJSON(raw,&j)}
 if err==nil{err=validateRegistration(j,p.Agent,j.Issuer,j.Dashboard,j.Request.ClassID)}
 if err!=nil||j.State!="registered-inactive"||j.DeviceID!=p.Run.DeviceID||
  j.Request.RequestID!=p.Run.RequestID||j.Request.ClassID!=p.Run.Manifest.ClassID{
  return j,nil,fmt.Errorf("saved registration does not match the selected plan")
 }
 if err=checkRegistrationIdentity(root,j,true);err!=nil{return j,nil,err}
 c,err:=newRegistrationClient(p.Agent.ControlPlane,p.Agent.TenantID)
 return j,c,err
}
func RunHostStatus(ctx context.Context,args []string,out,diagnostic io.Writer) int {
 return runHostStatus(ctx,args,out,diagnostic,defaultSetupState,os.Geteuid()==0,nil)
}
func runHostStatus(ctx context.Context,args []string,out,diagnostic io.Writer,stateDir string,root bool,override *liveStatusDeps) int {
 f:=flag.NewFlagSet("host-status",flag.ContinueOnError);f.SetOutput(diagnostic)
 path:=f.String("plan","","certification plan containing the installed agent plan")
 if err:=f.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
 if f.NArg()!=0||*path==""{fmt.Fprintln(diagnostic,"require --plan FILE");return 2}
 if !root{fmt.Fprintln(diagnostic,"host-status requires root to verify private installed identity");return 1}
 var p certificationPlan
 raw,err:=readSetupRegular(*path,false);if err==nil{err=decodeSetupJSON(raw,&p)}
 if err==nil{err=validateCertificationPlan(p,p.SHA256)}
 if err!=nil{fmt.Fprintln(diagnostic,"Certification plan unavailable, invalid or from a different installer build. Use its matching signed installer and preserve the original plan.");return 1}
 unlock,err:=supportLock(stateDir)
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1};defer unlock()
 ctx,cancel:=context.WithTimeout(ctx,90*time.Second);defer cancel()
 var identity registrationJournal
 var client *hostregistration.Client
 d:=liveStatusDeps{
  Now:time.Now,
  Certification:func()supportStage{return liveCertificationHistory(stateDir,p)},
  Installation:func(ctx context.Context)error{
   if err:=verifyInstalledForActivation(ctx,"",p.Agent,true);err!=nil{return err}
   return checkAgentUnitPaths(ctx,setupSource{Output:liveSystemdOutput},"",agentPayloads(p.Agent,nil))
  },
  Identity:func()error{var err error;identity,client,err=liveIdentity("",stateDir,p);return err},
  Marker:func()(bool,error){m:=activationMarker(p);return agentFileMatches(m.Path,m)},
  Service:func(ctx context.Context)([]byte,error){
   return liveSystemdOutput(ctx,"systemctl","--system","show","--all","hypermesh-agent.service",
    "-p","LoadState","-p","UnitFileState","-p","ActiveState","-p","SubState",
    "-p","MainPID","-p","NRestarts","-p","InvocationID",
    "-p","NeedDaemonReload","-p","FragmentPath","-p","DropInPaths")
  },
  Reboot:func()(bool,error){return liveRebootRequired("")},
  Remote:func(ctx context.Context)(hostregistration.OnboardingSnapshot,error){
   return client.CheckDevice(ctx,identity.Request,identity.DeviceID,identity.DeviceSecret)
  },
 }
 if override!=nil{d=*override}
 report:=collectLiveHostStatus(ctx,p,d)
 if json.NewEncoder(out).Encode(report)!=nil{return 1}
 if report.AttentionRequired{return 1}
 return 0
}

// Fixed local tools and environment prevent PATH, SYSTEMD_HOST and D-Bus
// overrides from turning a local readiness check into a different machine's
// observation. Neither tool invocation below performs a mutation.
func liveSystemdCommand(ctx context.Context,name string,args ...string)(*exec.Cmd,error) {
 if !supportOneOf(name,"systemctl","systemd-analyze"){return nil,fmt.Errorf("unsupported status tool")}
 cmd:=exec.CommandContext(ctx,"/usr/bin/"+name,args...)
 cmd.Env=[]string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin","LC_ALL=C","SYSTEMD_COLORS=0","SYSTEMD_PAGER="}
 cmd.SysProcAttr=&syscall.SysProcAttr{Setpgid:true}
 cmd.Cancel=func()error{return syscall.Kill(-cmd.Process.Pid,syscall.SIGKILL)}
 cmd.WaitDelay=time.Second
 return cmd,nil
}
func liveSystemdOutput(ctx context.Context,name string,args ...string)([]byte,error) {
 ctx,cancel:=context.WithTimeout(ctx,20*time.Second);defer cancel()
 cmd,err:=liveSystemdCommand(ctx,name,args...);if err!=nil{return nil,err}
 var output setupOutput
 cmd.Stdout=&output;cmd.Stderr=io.Discard
 if err=cmd.Run();err!=nil{return nil,fmt.Errorf("local systemd observation unavailable")}
 return output.Bytes(),nil
}
