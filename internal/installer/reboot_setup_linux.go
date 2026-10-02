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
 "path/filepath"
 "regexp"
 "strings"
 "time"

 "github.com/FyberLabs/hypermesh-host/internal/hostregistration"
)

type setupRebootPlan struct {
 Schema int `json:"schema"`
 SHA256 string `json:"plan_sha256"`
 Scope string `json:"scope"`
 Setup SetupPlan `json:"setup"`
 MachineSHA256 string `json:"machine_sha256"`
 BootID string `json:"boot_id"`
 OSRequired bool `json:"os_reboot_required"`
 Headless bool `json:"headless_transition"`
}
type setupRebootJournal struct {
 Schema int `json:"schema"`
 PlanSHA256 string `json:"plan_sha256"`
 BootID string `json:"requested_boot_id"`
 ObservedBootID string `json:"observed_boot_id,omitempty"`
 State string `json:"state"`
 RecordedAt time.Time `json:"recorded_at"`
 FailureCode string `json:"failure_code,omitempty"`
}
func setupRebootDigest(p setupRebootPlan)string{p.SHA256="";raw,_:=json.Marshal(p);return digestSetupBytes(raw)}
func validateSetupRebootPlan(p setupRebootPlan,approved string)error{
 if p.Schema!=1||p.Scope!="pre-enrollment"||!setupDigestRE.MatchString(approved)||p.SHA256!=approved||
  setupRebootDigest(p)!=approved||!setupDigestRE.MatchString(p.MachineSHA256)||!hostregistration.UUID.MatchString(p.BootID)||
  (!p.OSRequired&&!p.Headless)||p.Headless!=(p.Setup.HeadlessRequested&&p.Setup.Platform.DefaultTarget=="graphical.target"){
  return fmt.Errorf("reboot approval does not match a supported pre-enrollment plan")
 }
 return validateSetupApproval(p.Setup,p.Setup.PlanSHA256)
}
func validSetupRebootJournal(j setupRebootJournal)bool{
 if j.Schema!=1||!setupDigestRE.MatchString(j.PlanSHA256)||!hostregistration.UUID.MatchString(j.BootID)||j.RecordedAt.IsZero(){return false}
 switch j.State{
 case "requested":return j.ObservedBootID==""&&supportOneOf(j.FailureCode,"","request_unconfirmed","interrupted","preflight")
 case "verifying":return hostregistration.UUID.MatchString(j.ObservedBootID)&&j.ObservedBootID!=j.BootID&&j.FailureCode==""
 case "verified":return hostregistration.UUID.MatchString(j.ObservedBootID)&&j.ObservedBootID!=j.BootID&&j.FailureCode==""
 case "verification-failed":return hostregistration.UUID.MatchString(j.ObservedBootID)&&j.ObservedBootID!=j.BootID&&j.FailureCode=="postflight"
 }
 return false
}
func setupRebootPath(dir,sha string)string{return filepath.Join(dir,"reboot-"+sha+".json")}
func saveSetupReboot(path string,j setupRebootJournal)error{
 if !validSetupRebootJournal(j){return fmt.Errorf("invalid reboot journal")}
 if err:=secureSetupDirectory(filepath.Dir(path),false);err!=nil{return err}
 if _,err:=os.Lstat(path);err==nil{if _,err=readSetupRegular(path,true);err!=nil{return err}}else if !os.IsNotExist(err){return err}
 raw,_:=json.Marshal(j)
 f,err:=os.CreateTemp(filepath.Dir(path),".reboot-");if err!=nil{return err};defer os.Remove(f.Name())
 if _,err=f.Write(raw);err!=nil{f.Close();return err};if err=f.Sync();err!=nil{f.Close();return err};if err=f.Close();err!=nil{return err}
 if err=os.Rename(f.Name(),path);err!=nil{return err}
 d,err:=os.Open(filepath.Dir(path));if err!=nil{return err};defer d.Close();return d.Sync()
}
func rebootIdentity(root string)(string,string,error){
 machine,err:=readSetupRegular(agentDest(root,"/etc/machine-id"),false)
 if err!=nil{return "","",fmt.Errorf("machine identity unavailable")}
 id:=strings.TrimSpace(string(machine))
 if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(id)||strings.Trim(id,"0")==""{return "","",fmt.Errorf("invalid machine identity")}
 boot,err:=readSetupRegular(agentDest(root,"/proc/sys/kernel/random/boot_id"),false)
 b:=strings.TrimSpace(string(boot))
 if err!=nil||!hostregistration.UUID.MatchString(b){return "","",fmt.Errorf("boot identity unavailable")}
 return digestSetupBytes([]byte(id)),b,nil
}

// This initial-install operation never stops a registered or installed agent.
// Refuse both filesystem traces and loaded units, including leftover state.
func guardRebootHost(ctx context.Context,root,state string,source setupSource)error{
 for _,path:=range []string{"/var/lib/hypermesh/device.json","/var/lib/hypermesh/lease.json",
  "/var/lib/hypermesh/activation-approved","/var/lib/hypermesh/registration-keys",
  "/usr/local/bin/hypermesh-agent","/etc/hypermesh/agent.env"} {
  full:=agentDest(root,path)
  parent:=filepath.Dir(full)
  for {if _,err:=os.Lstat(parent);!os.IsNotExist(err){break};parent=filepath.Dir(parent)}
  if err:=agentDirectory(parent,false);err!=nil{return fmt.Errorf("host state path is unsafe")}
  if _,err:=os.Lstat(full);!os.IsNotExist(err){return fmt.Errorf("installed or enrolled host requires a separate maintenance flow")}
 }
 entries,err:=os.ReadDir(state);if err!=nil{return fmt.Errorf("cannot inspect installer state")}
 for _,entry:=range entries{
  for _,prefix:=range []string{"agent-","registration-","onboarding-","certification-"}{
   if strings.HasPrefix(entry.Name(),prefix){return fmt.Errorf("existing installation history blocks initial-setup reboot")}
  }
 }
 if err=checkAgentUnitPaths(ctx,source,root,nil);err!=nil{return err}
 for _,unit:=range []string{"hypermesh-agent.service","hypermesh-wg.service"}{
  raw,e:=source.output(ctx,"systemctl","--system","show","--all",unit,"-p","LoadState","-p","ActiveState","-p","MainPID")
  // A cached or transient unit is also outside the pre-install scope.
  values:=map[string]string{}
  for _,line:=range strings.Split(strings.TrimSpace(string(raw)),"\n"){
   key,value,ok:=strings.Cut(line,"=")
   if !ok{return fmt.Errorf("cannot verify absence of host services")}
   if _,exists:=values[key];exists{return fmt.Errorf("ambiguous service state")}
   values[key]=value
  }
  if e!=nil||len(values)!=3||values["LoadState"]!="not-found"||values["ActiveState"]!="inactive"||values["MainPID"]!="0"{
   return fmt.Errorf("host services must be absent before initial-setup reboot")
  }
 }
 return ctx.Err()
}
func checkSetupReboot(ctx context.Context,p setupRebootPlan,root,state string,source setupSource,preflight func(context.Context,SetupPlan)(Plan,error),post bool)error{
 var j setupJournal
 if supportRead(state,p.Setup.PlanSHA256+".json",&j)!="collected"||validateSetupJournal(j)!=nil||
  j.PlanSHA256!=p.Setup.PlanSHA256||j.Stage!="prerequisites"||j.State!="complete"||j.OriginalTarget!=p.Setup.Platform.DefaultTarget||j.Error!=""{
  return fmt.Errorf("complete the approved prerequisite application before reboot")
 }
 if err:=guardRebootHost(ctx,root,state,source);err!=nil{return err}
 remaining,target,err:=validateSetupHost(ctx,p.Setup,setupApplyDeps{Source:source,Preflight:preflight})
 if err!=nil{return err}
 if len(remaining)!=0||(p.Setup.HeadlessRequested&&target!="multi-user.target"){return fmt.Errorf("approved prerequisites are incomplete")}
 needed,err:=liveRebootRequired(root);if err!=nil{return err}
 if post&&needed{return fmt.Errorf("OS still reports a reboot requirement; inspect packages before continuing")}
 if !post&&needed!=p.OSRequired{return fmt.Errorf("reboot requirement changed; review a fresh reboot plan")}
 if post&&p.Headless{
  raw,err:=source.output(ctx,"systemctl","--system","show","graphical.target","-p","ActiveState","--value")
  if err!=nil||strings.TrimSpace(string(raw))!="inactive"{return fmt.Errorf("approved headless boot has not been observed")}
 }
 return guardRebootHost(ctx,root,state,source)
}
type setupRebootDeps struct{
 Identity func()(string,string,error)
 Check func(context.Context,bool)error
 Save func(setupRebootJournal)error
 Request func(context.Context)error
 Now func()time.Time
}
func matchRebootIdentity(p setupRebootPlan,d setupRebootDeps)(string,error){
 machine,boot,err:=d.Identity()
 if err!=nil||machine!=p.MachineSHA256||!hostregistration.UUID.MatchString(boot){return "",fmt.Errorf("reboot plan belongs to another machine or identity is unavailable")}
 return boot,nil
}
func requestSetupReboot(ctx context.Context,p setupRebootPlan,j *setupRebootJournal,retry bool,d setupRebootDeps)error{
 if err:=validateSetupRebootPlan(p,p.SHA256);err!=nil{return err}
 boot,err:=matchRebootIdentity(p,d);if err!=nil{return err}
 if boot!=p.BootID{return fmt.Errorf("boot already changed; use resume-setup, not another reboot request")}
 if j.State!="" {
  if !validSetupRebootJournal(*j)||j.PlanSHA256!=p.SHA256||j.BootID!=p.BootID{return fmt.Errorf("invalid reboot checkpoint; preserve it")}
  if j.State!="requested"||!retry{return fmt.Errorf("a reboot request is already recorded; inspect it before explicitly using --retry-request")}
 }
 if ctx.Err()!=nil{return fmt.Errorf("reboot request interrupted")}
 if err=d.Check(ctx,false);err!=nil{return err}
 *j=setupRebootJournal{Schema:1,PlanSHA256:p.SHA256,BootID:p.BootID,State:"requested",RecordedAt:d.Now().UTC()}
 if err=d.Save(*j);err!=nil{return fmt.Errorf("cannot durably save reboot checkpoint; no reboot requested")}
 fail:=func(code string)error{
  j.FailureCode=code;j.RecordedAt=d.Now().UTC()
  if d.Save(*j)!=nil{return fmt.Errorf("reboot outcome and checkpoint update unconfirmed; preserve state")}
  return fmt.Errorf("reboot outcome unconfirmed; inspect reboot-status before retrying")
 }
 if ctx.Err()!=nil{return fail("interrupted")}
 if d.Check(ctx,false)!=nil{return fail("preflight")}
 boot,err=matchRebootIdentity(p,d)
 if err!=nil||boot!=p.BootID||ctx.Err()!=nil{return fail("preflight")}
 // A successful return confirms only request submission. The durable requested
 // state intentionally survives process termination or a lost systemd reply.
 if err=d.Request(ctx);err!=nil{return fail("request_unconfirmed")}
 return nil
}
func resumeSetupReboot(ctx context.Context,p setupRebootPlan,j *setupRebootJournal,d setupRebootDeps)error{
 if err:=validateSetupRebootPlan(p,p.SHA256);err!=nil{return err}
 if !validSetupRebootJournal(*j)||j.PlanSHA256!=p.SHA256||j.BootID!=p.BootID{return fmt.Errorf("matching reboot checkpoint required")}
 boot,err:=matchRebootIdentity(p,d);if err!=nil{return err}
 if boot==p.BootID{return fmt.Errorf("the original boot is still running; no reboot completion can be recorded")}
 candidate:=*j;candidate.ObservedBootID=boot;candidate.RecordedAt=d.Now().UTC()
 candidate.State="verifying";candidate.FailureCode=""
 if err=d.Save(candidate);err!=nil{return fmt.Errorf("cannot persist verification intent; no success confirmed")}
 *j=candidate
 candidate.State="verification-failed";candidate.FailureCode="postflight"
 checkErr:=ctx.Err()
 if checkErr==nil{checkErr=d.Check(ctx,true)}
 after,identityErr:=matchRebootIdentity(p,d)
 if checkErr==nil&&identityErr==nil&&after==boot&&ctx.Err()==nil{candidate.State="verified";candidate.FailureCode=""}
 if err=d.Save(candidate);err!=nil{return fmt.Errorf("cannot persist post-reboot verification; retry resume-setup")}
 *j=candidate
 if j.State!="verified"{return fmt.Errorf("post-reboot checks failed; inspect prerequisites and review a fresh setup plan if the kernel, OS or packages changed")}
 return nil
}
func rebootNext(state string)string{
 switch state{
 case "requested":return "A reboot was requested or interrupted. Reconnect and run resume-setup with the same reviewed plan. On the same boot, inspect OS inhibitors and reboot-status before explicitly retrying; never loop reboot requests."
 case "verifying":return "Post-reboot verification is running or was interrupted. Rerun resume-setup with the same reviewed plan; no successful postflight is recorded."
 case "verified":return "A different boot and the approved prerequisites were verified. Continue with plan-agent and install-agent; enrollment, certification and activation remain separate approvals."
 default:return "Post-reboot verification failed. Preserve state; inspect reboot-required, headless state, hardware, resources and package/kernel drift. Changed prerequisites require a new reviewed setup plan."
 }
}
func supportReboot(dir,sha string)supportStage{
 if sha==""{return supportUnavailable("reboot","not-requested")}
 var j setupRebootJournal
 state:=supportRead(dir,"reboot-"+sha+".json",&j)
 if state!="collected"{return supportUnavailable("reboot",state)}
 if j.PlanSHA256!=sha||!validSetupRebootJournal(j){return supportUnavailable("reboot","invalid")}
 at:=j.RecordedAt.UTC()
 return supportStage{Component:"reboot",Collection:"collected",State:j.State,RecordedAt:&at,
  FailureCode:j.FailureCode,ErrorRecorded:j.FailureCode!="",NextAction:rebootNext(j.State)}
}
func rebootSetupSource()setupSource{
 return setupSource{Output:func(ctx context.Context,name string,args ...string)([]byte,error){
  // The prerequisite validator only needs read-only inventory/systemd probes
  // once all approved additions are installed. Never invoke APT here.
  if name=="dpkg-query"{
   ctx,cancel:=context.WithTimeout(ctx,20*time.Second);defer cancel()
   cmd:=rebootInventoryCommand(ctx,args...)
   var output setupOutput;cmd.Stdout=&output;cmd.Stderr=io.Discard
   if cmd.Run()!=nil{return nil,fmt.Errorf("package inventory unavailable")};return output.Bytes(),nil
  }
  return liveSystemdOutput(ctx,name,args...)
 }}
}
func RunPlanReboot(ctx context.Context,args []string,out,diagnostic io.Writer)int{
 return runPlanReboot(ctx,args,out,diagnostic,defaultSetupState,os.Geteuid()==0)
}
func runPlanReboot(ctx context.Context,args []string,out,diagnostic io.Writer,state string,root bool)int{
 f:=flag.NewFlagSet("plan-reboot",flag.ContinueOnError);f.SetOutput(diagnostic)
 path:=f.String("setup-plan","","completed prerequisite plan")
 if err:=f.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
 if f.NArg()!=0||*path==""{fmt.Fprintln(diagnostic,"require --setup-plan FILE");return 2}
 if !root{fmt.Fprintln(diagnostic,"plan-reboot requires root");return 1}
 var p setupRebootPlan;p.Schema=1;p.Scope="pre-enrollment"
 raw,err:=readSetupRegular(*path,false);if err==nil{err=decodeSetupJSON(raw,&p.Setup)}
 if err==nil{err=validateSetupApproval(p.Setup,p.Setup.PlanSHA256)}
 if err!=nil{fmt.Fprintln(diagnostic,"prerequisite plan is unavailable or invalid");return 1}
 unlock,err:=supportLock(state);if err!=nil{fmt.Fprintln(diagnostic,err);return 1};defer unlock()
 ctx,cancel:=context.WithTimeout(ctx,3*time.Minute);defer cancel()
 p.MachineSHA256,p.BootID,err=rebootIdentity("")
 if err==nil{p.OSRequired,err=liveRebootRequired("")}
 p.Headless=p.Setup.HeadlessRequested&&p.Setup.Platform.DefaultTarget=="graphical.target"
 p.SHA256=setupRebootDigest(p)
 if err==nil&&!p.OSRequired&&!p.Headless{err=fmt.Errorf("no OS reboot requirement or approved headless transition was found")}
 if err==nil{err=validateSetupRebootPlan(p,p.SHA256)}
 if err==nil{err=checkSetupReboot(ctx,p,"",state,rebootSetupSource(),freshSetupPreflight,false)}
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 if json.NewEncoder(out).Encode(p)!=nil{return 1};return 0
}
func RunRebootSetup(ctx context.Context,args []string,out,diagnostic io.Writer)int{return runRebootSetup(ctx,args,out,diagnostic,false,defaultSetupState,os.Geteuid()==0,nil)}
func RunResumeSetup(ctx context.Context,args []string,out,diagnostic io.Writer)int{return runRebootSetup(ctx,args,out,diagnostic,true,defaultSetupState,os.Geteuid()==0,nil)}
func runRebootSetup(ctx context.Context,args []string,out,diagnostic io.Writer,resume bool,state string,root bool,override *setupRebootDeps)int{
 name:="reboot-setup";if resume{name="resume-setup"}
 f:=flag.NewFlagSet(name,flag.ContinueOnError);f.SetOutput(diagnostic)
 path:=f.String("plan","","reviewed reboot plan")
 approved:=f.String("approve-plan","","exact reboot plan_sha256")
 reboot:=f.Bool("reboot",false,"request system reboot; disconnects this session")
 retry:=f.Bool("retry-request",false,"explicitly retry an unconfirmed request on the original boot")
 if err:=f.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
 if f.NArg()!=0||*path==""||!setupDigestRE.MatchString(*approved)||(!resume&&!*reboot)||(resume&&(*reboot||*retry)){
  fmt.Fprintln(diagnostic,"require --plan FILE --approve-plan SHA256 and --reboot only for reboot-setup");return 2}
 if !root{fmt.Fprintln(diagnostic,"reboot/resume requires root");return 1}
 var p setupRebootPlan
 raw,err:=readSetupRegular(*path,false);if err==nil{err=decodeSetupJSON(raw,&p)}
 if err==nil{err=validateSetupRebootPlan(p,*approved)}
 if err!=nil{fmt.Fprintln(diagnostic,"invalid reboot approval or installer build; preserve and review the plan");return 1}
 unlock,err:=lockSetupState(state);if err!=nil{fmt.Fprintln(diagnostic,err);return 1};defer unlock()
 ctx,cancel:=context.WithTimeout(ctx,3*time.Minute);defer cancel()
 journalPath:=setupRebootPath(state,p.SHA256)
 var j setupRebootJournal
 if _,e:=os.Lstat(journalPath);e==nil{
  raw,err=readSetupRegular(journalPath,true);if err==nil{err=decodeSetupJSON(raw,&j)}
  if err!=nil||!validSetupRebootJournal(j){fmt.Fprintln(diagnostic,"invalid reboot checkpoint; preserve state");return 1}
 }else if !os.IsNotExist(e){fmt.Fprintln(diagnostic,"cannot inspect reboot checkpoint");return 1}
 d:=setupRebootDeps{
  Identity:func()(string,string,error){return rebootIdentity("")},
  Check:func(ctx context.Context,post bool)error{return checkSetupReboot(ctx,p,"",state,rebootSetupSource(),freshSetupPreflight,post)},
  Save:func(v setupRebootJournal)error{return saveSetupReboot(journalPath,v)},Now:time.Now,
  Request:func(ctx context.Context)error{
   // Normal systemd request: no force, inhibitor bypass, scheduled retry or
   // boot-time unit. A lost reply must never trigger an automatic reboot loop.
   return submitSetupReboot(ctx)
  },
 }
 if override!=nil{d.Identity=override.Identity;d.Check=override.Check;d.Request=override.Request;if override.Now!=nil{d.Now=override.Now}}
 if resume{err=resumeSetupReboot(ctx,p,&j,d)}else{err=requestSetupReboot(ctx,p,&j,*retry,d)}
 stage:=supportReboot(state,p.SHA256)
 // The saved checkpoint is historical, including when a new attempt fails
 // before it can replace a previous verified record.
 if json.NewEncoder(out).Encode(map[string]any{"operation":name,"attempt_succeeded":err==nil,"checkpoint":stage})!=nil{return 1}
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1};return 0
}
func RunRebootStatus(ctx context.Context,args []string,out,diagnostic io.Writer)int{
 f:=flag.NewFlagSet("reboot-status",flag.ContinueOnError);f.SetOutput(diagnostic)
 sha:=f.String("plan-sha","","reboot plan digest")
 if err:=f.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
 if f.NArg()!=0||!setupDigestRE.MatchString(*sha){fmt.Fprintln(diagnostic,"require --plan-sha SHA256");return 2}
 if os.Geteuid()!=0{fmt.Fprintln(diagnostic,"reboot-status requires root");return 1}
 unlock,err:=supportLock(defaultSetupState);if err!=nil{fmt.Fprintln(diagnostic,err);return 1};defer unlock()
 if ctx.Err()!=nil{return 1}
 stage:=supportReboot(defaultSetupState,*sha)
 if json.NewEncoder(out).Encode(stage)!=nil||stage.Collection!="collected"{return 1};return 0
}

func rebootInventoryCommand(ctx context.Context,args ...string)*exec.Cmd{
 cmd:=exec.CommandContext(ctx,"/usr/bin/dpkg-query",args...)
 cmd.Env=[]string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin","LC_ALL=C"}
 cmd.SysProcAttr=&syscall.SysProcAttr{Setpgid:true}
 cmd.Cancel=func()error{return syscall.Kill(-cmd.Process.Pid,syscall.SIGKILL)}
 cmd.WaitDelay=time.Second
 return cmd
}

func setupRebootCommand(ctx context.Context)(*exec.Cmd,error){
 return liveSystemdCommand(ctx,"systemctl","--system","--no-ask-password","--check-inhibitors=yes","reboot")
}
func submitSetupReboot(ctx context.Context)error{
 ctx,cancel:=context.WithTimeout(ctx,20*time.Second);defer cancel()
 cmd,err:=setupRebootCommand(ctx);if err!=nil{return err}
 cmd.Stdout=io.Discard;cmd.Stderr=io.Discard
 if cmd.Run()!=nil{return fmt.Errorf("reboot submission unconfirmed; inspect inhibitors and system logs")}
 return nil
}
