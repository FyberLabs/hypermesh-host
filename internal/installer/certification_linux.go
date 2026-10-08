//go:build linux

package installer

import (
 "bytes"
 "context"
 "errors"
 "encoding/json"
 "flag"
 "fmt"
 "io"
 "os"
 "path/filepath"
 "strings"
 "strconv"
 "time"

 "github.com/FyberLabs/hypermesh-host/internal/agent"
 "github.com/FyberLabs/hypermesh-host/internal/deviceauth"
 "github.com/FyberLabs/hypermesh-host/internal/hostregistration"
)

type certificationPlan struct {
 Schema int `json:"schema"`
 SHA256 string `json:"plan_sha256"`
 Agent agentInstallPlan `json:"agent"`
 Run hostregistration.CertificationRun `json:"run"`
 ModelPath string `json:"model_path"`
}
type certificationJournal struct {
 Schema int `json:"schema"`
 PlanSHA256 string `json:"plan_sha256"`
 RunID string `json:"run_id"`
 State string `json:"state"`
 RecordedAt time.Time `json:"recorded_at"`
 Evidence *hostregistration.CertificationEvidence `json:"evidence,omitempty"`
 FailureCode string `json:"failure_code,omitempty"`
}
func certificationDigest(p certificationPlan)string{p.SHA256="";raw,_:=json.Marshal(p);return digestSetupBytes(raw)}
func validateCertificationPlan(p certificationPlan,approved string)error{
 if p.Schema!=1||p.SHA256!=approved||!setupDigestRE.MatchString(approved)||certificationDigest(p)!=approved||
  agentPlanDigest(p.Agent)!=p.Agent.SHA256||validateInstalledAgentInputs(p.Agent)!=nil||
  p.Run.Manifest.Validate()!=nil||!setupDigestRE.MatchString(p.Run.ManifestSHA256)||
  !hostregistration.UUID.MatchString(p.Run.RunID)||p.Run.TenantID!=p.Agent.TenantID||
  !hostregistration.UUID.MatchString(p.Run.DeviceID)||!hostregistration.UUID.MatchString(p.Run.RequestID)||
  p.Run.State!="planned"||p.Run.SellingAllowed==nil||*p.Run.SellingAllowed{
  return fmt.Errorf("certification approval does not match a supported current plan")
 }
 m:=p.Run.Manifest
 if _,err:=certificationURL(m.SourceRef);err!=nil{return err}
 expected:=filepath.Join(p.Agent.Setup.Preflight.Resources.ModelDirectory,"cache",strings.TrimPrefix(m.FileHash,"sha256:")+".gguf")
 if p.ModelPath!=expected{return fmt.Errorf("model destination is outside the approved content-addressed cache")}
 return nil
}
func certificationStatePath(state,sha string)string{return filepath.Join(state,"certification-"+sha+".json")}
func validCertificationJournal(j certificationJournal)bool{
 if j.Schema!=1||!setupDigestRE.MatchString(j.PlanSHA256)||!hostregistration.UUID.MatchString(j.RunID)||j.RecordedAt.IsZero()||
 !supportOneOf(j.State,"running","evidence-ready","passed","failed","activating","active"){return false}
 if supportOneOf(j.State,"evidence-ready","passed","activating","active")&&j.Evidence==nil{return false}
 return true
}
func saveCertification(path string,j certificationJournal)error{
 if !validCertificationJournal(j){return fmt.Errorf("invalid certification state")}
 if err:=secureSetupDirectory(filepath.Dir(path),false);err!=nil{return err}
 if _,err:=os.Lstat(path);err==nil{if _,err=readSetupRegular(path,true);err!=nil{return err}}else if !os.IsNotExist(err){return err}
 raw,_:=json.Marshal(j)
 f,err:=os.CreateTemp(filepath.Dir(path),".certification-");if err!=nil{return err};defer os.Remove(f.Name())
 if _,err=f.Write(raw);err!=nil{f.Close();return err};if err=f.Sync();err!=nil{f.Close();return err};if err=f.Close();err!=nil{return err}
 if err=os.Rename(f.Name(),path);err!=nil{return err}
 d,err:=os.Open(filepath.Dir(path));if err!=nil{return err};defer d.Close();return d.Sync()
}
func readCertificationIdentity(p certificationPlan)(registrationJournal,*hostregistration.Client,error){
 var j registrationJournal
 raw,err:=readSetupRegular(registrationPath(defaultSetupState,p.Agent.SHA256),true)
 if err==nil{err=decodeSetupJSON(raw,&j)}
 if err==nil{err=validateRegistration(j,p.Agent,j.Issuer,j.Dashboard,j.Request.ClassID)}
 if err!=nil||j.State!="registered-inactive"||j.DeviceID!=p.Run.DeviceID||j.Request.RequestID!=p.Run.RequestID||j.Request.ClassID!=p.Run.Manifest.ClassID{
 return j,nil,fmt.Errorf("certification plan does not match saved registration")
 }
 if err=checkRegistrationIdentity("",j,true);err!=nil{return j,nil,err}
 c,err:=hostregistration.New(p.Agent.ControlPlane,p.Agent.TenantID);return j,c,err
}
func certPreflight(ctx context.Context,p certificationPlan,j registrationJournal)error{
 if err:=verifyRegistrationService(ctx,p.Agent);err!=nil{return err}
 if err:=checkRegistrationIdentity("",j,true);err!=nil{return err}
 if _,err:=os.Lstat("/var/lib/hypermesh/lease.json");!os.IsNotExist(err){return fmt.Errorf("reconcile existing lease before certification")}
 _,err:=registrationPreflight(ctx,p.Agent,j.Request.ClassID);return err
}
func certTunnel(ctx context.Context,p certificationPlan,j registrationJournal,c *hostregistration.Client)error{
 receipt,err:=c.PrepareTunnel(ctx,j.Request,j.DeviceID,j.DeviceSecret);if err!=nil{return err}
 _,err=newWireguardProbe().check(ctx,receipt.Tunnel,j.PrivateKey,j.Request.WGPublicKey,p.Agent.Setup.WireGuardMode,p.SHA256)
 return err
}

func RunPlanCertification(ctx context.Context,args []string,out,diagnostic io.Writer)int{
 f:=flag.NewFlagSet("plan-certification",flag.ContinueOnError);f.SetOutput(diagnostic)
 path:=f.String("agent-plan","","installed agent plan")
 if err:=f.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
 if f.NArg()!=0||*path==""{fmt.Fprintln(diagnostic,"require --agent-plan FILE");return 2}
 if os.Geteuid()!=0{fmt.Fprintln(diagnostic,"requires root to read registered device identity");return 1}
 var p certificationPlan;p.Schema=1
 raw,err:=readSetupRegular(*path,false);if err==nil{err=decodeSetupJSON(raw,&p.Agent)}
 if err==nil{err=validateInstalledAgentInputs(p.Agent)}
 if err==nil&&agentPlanDigest(p.Agent)!=p.Agent.SHA256{err=fmt.Errorf("agent plan digest differs")}
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 unlock,err:=lockSetupState(defaultSetupState);if err!=nil{fmt.Fprintln(diagnostic,err);return 1};defer unlock()
 var j registrationJournal
 raw,err=readSetupRegular(registrationPath(defaultSetupState,p.Agent.SHA256),true)
 if err==nil{err=decodeSetupJSON(raw,&j)}
 if err==nil{err=validateRegistration(j,p.Agent,j.Issuer,j.Dashboard,j.Request.ClassID)}
 if err==nil&&j.State!="registered-inactive"{err=fmt.Errorf("complete inactive registration first")}
 if err==nil{err=certPreflight(ctx,p,j)}
 var c *hostregistration.Client
 if err==nil{c,err=hostregistration.New(p.Agent.ControlPlane,p.Agent.TenantID)}
 var id string
 if err==nil{id,err=hostregistration.NewRunID()}
 if err==nil{p.Run,err=c.PlanCertification(ctx,j.Request,j.DeviceID,j.DeviceSecret,id)}
 if err==nil{
  p.ModelPath=filepath.Join(p.Agent.Setup.Preflight.Resources.ModelDirectory,"cache",strings.TrimPrefix(p.Run.Manifest.FileHash,"sha256:")+".gguf")
  p.SHA256=certificationDigest(p);err=validateCertificationPlan(p,p.SHA256)
 }
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 if json.NewEncoder(out).Encode(p)!=nil{return 1};return 0
}

type certificationDeps struct {
 Preflight func(context.Context)error
 Acquire func(context.Context)error
 Workload func(context.Context)(hostregistration.CertificationEvidence,error)
 Tunnel func(context.Context)error
 Report func(context.Context,hostregistration.CertificationEvidence)(bool,error)
 Save func(certificationJournal)error
}
func performCertification(ctx context.Context,p certificationPlan,j *certificationJournal,d certificationDeps)error{
 // Upload retries retain the exact evidence; never overwrite a terminal server result.
 if j.State=="passed"{return nil}
 if j.State!="evidence-ready"{
  *j=certificationJournal{Schema:1,PlanSHA256:p.SHA256,RunID:p.Run.RunID,State:"running",RecordedAt:time.Now().UTC()}
  if err:=d.Save(*j);err!=nil{return err}
  fail:=func(code string)error{j.State="failed";j.FailureCode=code;j.RecordedAt=time.Now().UTC();_ = d.Save(*j);return fmt.Errorf("certification failed (%s); services remain inactive",code)}
  if d.Preflight(ctx)!=nil{return fail("preflight")}
  if d.Acquire(ctx)!=nil{return fail("acquisition")}
  if d.Preflight(ctx)!=nil{return fail("preflight")}
  e,err:=d.Workload(ctx);if err!=nil{return fail("workload")}
  if d.Tunnel(ctx)!=nil{return fail("tunnel")}
  if d.Preflight(ctx)!=nil||ctx.Err()!=nil{return fail("postflight")}
  e.TunnelVerified=true
  j.Evidence=&e;j.State="evidence-ready";j.RecordedAt=time.Now().UTC()
  if err=d.Save(*j);err!=nil{return fmt.Errorf("cannot persist certification evidence; preserve state")}
 }
 if j.Evidence==nil{return fmt.Errorf("incomplete certification journal")}
 passed,err:=d.Report(ctx,*j.Evidence)
 if err!=nil{return fmt.Errorf("certification upload unconfirmed; retry this same plan and retained evidence")}
 j.State="failed";j.FailureCode="class_band"
 if passed{j.State="passed";j.FailureCode=""}
 j.RecordedAt=time.Now().UTC()
 if err=d.Save(*j);err!=nil{return fmt.Errorf("certification outcome persistence failed; retry retained evidence")}
 if !passed{return fmt.Errorf("certification failed the authoritative class envelope; review hardware and prepare a fresh run")}
 return nil
}
func RunCertifyAgent(ctx context.Context,args []string,out,diagnostic io.Writer)int{
 return runCertificationCommand(ctx,args,out,diagnostic,false)
}
func RunActivateAgent(ctx context.Context,args []string,out,diagnostic io.Writer)int{
 return runCertificationCommand(ctx,args,out,diagnostic,true)
}
func runCertificationCommand(ctx context.Context,args []string,out,diagnostic io.Writer,activate bool)int{
 name:="certify-agent";if activate{name="activate-agent"}
 f:=flag.NewFlagSet(name,flag.ContinueOnError);f.SetOutput(diagnostic)
 path:=f.String("plan","","reviewed certification plan")
 approved:=f.String("approve-plan","","exact certification plan_sha256")
 run:=f.Bool("run",false,"approve model acquisition and pinned certification")
 start:=f.Bool("start",false,"approve service startup using the registered device; selling starts once startup is verified")
 tokenPath:=f.String("model-token-file","","optional private Hugging Face access token file")
 if err:=f.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
 if f.NArg()!=0||*path==""||!setupDigestRE.MatchString(*approved)||(!activate&&!*run)||(activate&&!*start){
 fmt.Fprintln(diagnostic,"require --plan FILE --approve-plan SHA256 and --run (certify) or --start (activate)");return 2}
 if os.Geteuid()!=0{fmt.Fprintln(diagnostic,"requires root");return 1}
 var p certificationPlan
 raw,err:=readSetupRegular(*path,false);if err==nil{err=decodeSetupJSON(raw,&p)}
 if err==nil{err=validateCertificationPlan(p,*approved)}
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 unlock,err:=lockSetupState(defaultSetupState);if err!=nil{fmt.Fprintln(diagnostic,err);return 1};defer unlock()
 ctx,cancel:=context.WithTimeout(ctx,45*time.Minute);defer cancel()
 identity,c,err:=readCertificationIdentity(p)
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 journalPath:=certificationStatePath(defaultSetupState,p.SHA256)
 var j certificationJournal
 if _,e:=os.Lstat(journalPath);e==nil{
  raw,err=readSetupRegular(journalPath,true);if err==nil{err=decodeSetupJSON(raw,&j)}
  if err!=nil||!validCertificationJournal(j)||j.PlanSHA256!=p.SHA256||j.RunID!=p.Run.RunID{fmt.Fprintln(diagnostic,"invalid certification journal; preserve state");return 1}
 }else if !os.IsNotExist(e){fmt.Fprintln(diagnostic,"cannot inspect certification state");return 1}
 save:=func(v certificationJournal)error{return saveCertification(journalPath,v)}
 if activate {
  err=activateCertified(ctx,p,identity,c,&j,save,diagnostic)
 }else{
  if j.State=="failed"&&j.FailureCode=="class_band"{fmt.Fprintln(diagnostic,"terminal certification failure; review and prepare a fresh run");return 1}
  if j.State=="active"||j.State=="activating"{fmt.Fprintln(diagnostic,"activation started; use activate-agent to reconcile");return 1}
  var token string
  token,err=modelAccessToken(*tokenPath)
  if err==nil{
   d:=certificationDeps{
    Preflight:func(ctx context.Context)error{return certPreflight(ctx,p,identity)},
    Acquire:func(ctx context.Context)error{
     if !p.Run.ExpiresAt.After(time.Now()){return fmt.Errorf("run expired")}
     if err:=agentDirectory(filepath.Dir(p.ModelPath),true);err!=nil{return err}
     address,err:=certificationURL(p.Run.Manifest.SourceRef);if err!=nil{return err}
     transfer,cancel:=context.WithTimeout(ctx,30*time.Minute);defer cancel()
     return acquireModel(transfer,p.ModelPath,p.Run.Manifest.FileHash,p.Run.Manifest.SizeBytes,func(ctx context.Context)(io.ReadCloser,error){
      return modelBytes(ctx,modelTransport(token),address,token)
     })
    },
    Workload:func(ctx context.Context)(hostregistration.CertificationEvidence,error){
     m:=p.Run.Manifest
     request:=agent.WorkloadPreflight{PlanSHA256:p.SHA256,CatalogID:m.CatalogID,ImageDigest:m.ImageDigest,ModelPath:p.ModelPath,FileHash:m.FileHash,ContextTokens:m.ContextTokens,FullContext:true,SuiteID:m.SuiteID}
     config,err:=os.MkdirTemp(defaultSetupState,".certification-docker-");if err!=nil{return hostregistration.CertificationEvidence{},err};defer os.RemoveAll(config)
     receipt,err:=agent.RunWorkloadPreflight(ctx,request,"/var/lib/hypermesh",func(ctx context.Context,_ string,args ...string)([]byte,error){return workloadDocker(ctx,config,args...)})
     if err!=nil||receipt.ContextFit==nil||!receipt.ContextFit.ValidFor(m.ContextTokens)||!receipt.CleanupComplete{return hostregistration.CertificationEvidence{},fmt.Errorf("workload evidence incomplete")}
     return hostregistration.CertificationEvidence{HostImageHash:receipt.HostImageHash,FileHash:m.FileHash,ImageDigest:m.ImageDigest,
      SampleMS:receipt.SampleMS,TokensPerSecond:receipt.TokensPerSecond,ThermalC:receipt.ThermalC,
      ContextTokens:m.ContextTokens,PromptTokens:receipt.ContextFit.PromptTokens,GeneratedTokens:receipt.ContextFit.GeneratedTokens,
      ContextElapsedMS:receipt.ContextFit.ElapsedMS,CleanupComplete:true},nil
    },
    Tunnel:func(ctx context.Context)error{return certTunnel(ctx,p,identity,c)},
    Report:func(ctx context.Context,e hostregistration.CertificationEvidence)(bool,error){
     result,err:=c.ReportCertification(ctx,identity.Request,identity.DeviceID,identity.DeviceSecret,p.Run,e)
     return err==nil&&result.State=="passed",err
    },Save:save,
   }
   err=performCertification(ctx,p,&j,d)
  }
 }
 // The private evidence journal has no credentials; report only stage identity.
 _=json.NewEncoder(out).Encode(map[string]any{"plan_sha256":p.SHA256,"run_id":p.Run.RunID,"state":j.State,"failure_code":j.FailureCode,"selling_allowed":false})
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1};return 0
}

func activationMarker(p certificationPlan)agentPayload{
 raw,_:=json.Marshal(map[string]string{"plan_sha256":p.SHA256,"run_id":p.Run.RunID,"device_id":p.Run.DeviceID})
 return agentPayload{agentFile{"/var/lib/hypermesh/activation-approved",digestSetupBytes(raw),0600},raw}
}
type activationDeps struct{
 Save func(certificationJournal)error
 Publish func()error
 Start func(context.Context)error
 Verify func(context.Context)error
 Rollback func(context.Context)error
}
func startCertified(ctx context.Context,j *certificationJournal,permit time.Time,d activationDeps)error{
 if !permit.After(time.Now())||ctx.Err()!=nil{return fmt.Errorf("activation permit expired")}
 j.State="activating";j.RecordedAt=time.Now().UTC();j.FailureCode=""
 if err:=d.Save(*j);err!=nil{return err}
 rollback:=func()error{
  clean,cancel:=context.WithTimeout(context.Background(),45*time.Second);defer cancel()
  err:=d.Rollback(clean)
  j.State="passed";j.FailureCode="service_start";j.RecordedAt=time.Now().UTC()
  if err!=nil{j.State="activating";j.FailureCode="cleanup"}
  if d.Save(*j)!=nil{return fmt.Errorf("activation cleanup/status unconfirmed; preserve state and inspect services")}
  return fmt.Errorf("activation did not complete; inspect service state and retry the same approved plan")
 }
 if d.Publish()!=nil{return rollback()}
 if ctx.Err()!=nil||!permit.After(time.Now())||d.Start(ctx)!=nil||d.Verify(ctx)!=nil||ctx.Err()!=nil{return rollback()}
 j.State="active";j.RecordedAt=time.Now().UTC()
 if d.Save(*j)!=nil{return rollback()}
 return nil
}


type serviceInvocation struct { PID int; Restarts int; Invocation string }
func activeInvocation(raw []byte)(serviceInvocation,error){
 values:=map[string]string{}
 for _,line:=range strings.Split(strings.TrimSpace(string(raw)),"\n"){
  key,value,ok:=strings.Cut(line,"=");if !ok||values[key]!=""{return serviceInvocation{},fmt.Errorf("invalid systemd status")}
  values[key]=value
 }
 pid,pe:=strconv.Atoi(values["MainPID"]);restarts,re:=strconv.Atoi(values["NRestarts"])
 invocation:=values["InvocationID"]
 if values["ActiveState"]!="active"||values["SubState"]!="running"||pe!=nil||pid<=0||re!=nil||restarts<0||len(invocation)!=32||strings.Trim(invocation,"0123456789abcdef")!=""{
  return serviceInvocation{},fmt.Errorf("agent is not running in a stable invocation")
 }
 return serviceInvocation{pid,restarts,invocation},nil
}
func waitStableAgent(ctx context.Context,window,poll time.Duration,probe func(context.Context)([]byte,error))error{
 var started time.Time
 var first serviceInvocation
 for{
  raw,err:=probe(ctx);if err!=nil{return fmt.Errorf("cannot verify agent startup")}
  current,err:=activeInvocation(raw);if err!=nil{return err}
  if first.PID==0{first=current;started=time.Now()}else if current!=first{return fmt.Errorf("agent restarted during startup")}
  if ctx.Err()!=nil{return fmt.Errorf("startup verification interrupted")}
  if time.Since(started)>=window{return nil}
  timer:=time.NewTimer(poll)
  select{case <-ctx.Done():timer.Stop();return fmt.Errorf("startup verification interrupted");case <-timer.C:}
 }
}

func verifyAgentStartup(ctx context.Context)error{
   verify,cancel:=context.WithTimeout(ctx,25*time.Second);defer cancel()
   return waitStableAgent(verify,15*time.Second,500*time.Millisecond,func(ctx context.Context)([]byte,error){
    return (setupSource{}).output(ctx,"systemctl","--system","show","hypermesh-agent.service",
     "-p","ActiveState","-p","SubState","-p","MainPID","-p","NRestarts","-p","InvocationID")
   })
}
func activateCertified(ctx context.Context,p certificationPlan,identity registrationJournal,c *hostregistration.Client,j *certificationJournal,save func(certificationJournal)error,diagnostic io.Writer)error{
 return activateCertifiedMode(ctx,p,identity,c,j,save,diagnostic,false)
}
func activateCertifiedMode(ctx context.Context,p certificationPlan,identity registrationJournal,c *hostregistration.Client,j *certificationJournal,save func(certificationJournal)error,diagnostic io.Writer,resume bool)error{
 if !supportOneOf(j.State,"passed","activating","active")||j.Evidence==nil{return fmt.Errorf("complete certification before activation")}
 marker:=activationMarker(p)
 own,err:=agentFileMatches(marker.Path,marker);if err!=nil{return err}
 if own&&j.State=="passed"{return fmt.Errorf("unexpected activation marker; preserve state for recovery")}
 if err=verifyInstalledForActivation(ctx,"",p.Agent,own);err!=nil{return err}
 if err=checkAgentUnitPaths(ctx,setupSource{},"",agentPayloads(p.Agent,nil));err!=nil{return err}
 if err=checkRegistrationIdentity("",identity,true);err!=nil{return err}
 if _,e:=os.Lstat("/var/lib/hypermesh/lease.json");!os.IsNotExist(e){return fmt.Errorf("existing lease blocks activation")}
 if _,err=registrationPreflight(ctx,p.Agent,identity.Request.ClassID);err!=nil{return err}
 if err=verifyAcquiredModel(ctx,p.ModelPath,p.Run.Manifest.FileHash,p.Run.Manifest.SizeBytes);err!=nil{return err}
 // A repeat command must not restart an already active installation.
 if own&&j.State=="active"{
  raw,e:=(setupSource{}).output(ctx,"systemctl","is-active","hypermesh-agent.service")
  if e==nil&&strings.TrimSpace(string(raw))=="active"{
   if err=verifyAgentStartup(ctx);err!=nil||resume{return err}
   return releaseActivationSell(ctx)
  }
 }
 // Retry after a crash first returns our previously started service to inactive.
 if own {
  if _,err=(setupSource{}).output(ctx,"systemctl","stop","hypermesh-agent.service");err!=nil{return fmt.Errorf("cannot stop previous activation")}
  if p.Agent.Setup.WireGuardMode=="userspace"{
   if _,err=(setupSource{}).output(ctx,"systemctl","stop","hypermesh-wg.service");err!=nil{return fmt.Errorf("cannot stop previous WireGuard activation")}
  }
 }
 if err=inactiveMemoryServices(ctx,setupSource{});err!=nil{return err}
 if err=certTunnel(ctx,p,identity,c);err!=nil{return err}
 var permit hostregistration.CertificationRun
 if resume{
  // Maintenance resume is distinct from completing the original enrollment.
  token,e:=deviceauth.Authorize(ctx,identity.Issuer,diagnostic);if e!=nil{return e}
  if err=c.Login(ctx,token.Bearer(),func(sub string)error{
   if sub!=identity.Owner{return fmt.Errorf("original registration owner required")};return nil
  });err!=nil{return err}
  if err=c.ApproveResume(ctx,p.Run);err!=nil{return err}
  permit,err=c.ResumePermit(ctx,identity.Request,identity.DeviceID,identity.DeviceSecret,p.Run)
 }else{
  // Completed owner-approved enrollment is durable; no browser session is needed.
  permit,err=c.ActivationPermit(ctx,identity.Request,identity.DeviceID,identity.DeviceSecret,p.Run)
 }
 if err!=nil{return err}
 // Recheck local authority immediately before publishing the startup marker.
 if err=verifyInstalledForActivation(ctx,"",p.Agent,own);err!=nil{return err}
 if err=checkRegistrationIdentity("",identity,true);err!=nil{return err}
 if err=checkAgentUnitPaths(ctx,setupSource{},"",agentPayloads(p.Agent,nil));err!=nil{return err}
 if err=inactiveMemoryServices(ctx,setupSource{});err!=nil{return err}
 // The guard keeps the plane's paused state until startup is verified, so a
 // rollback or crash can retry with a fresh permit. It is released below.
 if err=prepareActivationSell(activationSellPath,time.Now());err!=nil{return err}
 d:=activationDeps{Save:save,Publish:func()error{if own{return nil};return writeAgentFile(marker.Path,marker)},
  Start:func(ctx context.Context)error{
   if _,err:=(setupSource{}).output(ctx,"systemctl","daemon-reload");err!=nil{return err}
   _,err:=(setupSource{}).output(ctx,"systemctl","enable","--now","hypermesh-agent.service");return err
  },
  Verify:func(ctx context.Context)error{
   return verifyAgentStartup(ctx)
  },
  Rollback:func(ctx context.Context)error{
   if _,err:=(setupSource{}).output(ctx,"systemctl","disable","--now","hypermesh-agent.service");err!=nil{return err}
   if p.Agent.Setup.WireGuardMode=="userspace"{
    if _,err:=(setupSource{}).output(ctx,"systemctl","stop","hypermesh-wg.service");err!=nil{return err}
   }
   if err:=inactiveMemoryServices(ctx,setupSource{});err!=nil{return err}
   exists,err:=agentFileMatches(marker.Path,marker);if err!=nil{return err}
   if exists{if err=os.Remove(marker.Path);err!=nil{return err}}
   dir,err:=os.Open(filepath.Dir(marker.Path));if err!=nil{return err};defer dir.Close();return dir.Sync()
  },
 }
 if err=startCertified(ctx,j,*permit.PermitExpiresAt,d);err!=nil||resume{return err}
 // A certified host sells as soon as its service is verified.
 return releaseActivationSell(ctx)
}

const activationSellPath="/var/lib/hypermesh/sell.json"
var activationSellGuard=[]byte("{\"sell_state\":\"paused\",\"source\":\""+agent.SellSourceActivation+"\"}")

// activationKeepsSell reports whether an existing sell.json is kept as is.
// A current pause (owner, maintenance or an earlier guard) is kept; selling,
// an empty state or an expired timed pause is replaced by the guard.
func activationKeepsSell(raw []byte,now time.Time)(bool,error){
 // Earlier installers wrote exactly these bytes at activation. The agent
 // clears them, so replace them with the guard instead of keeping them.
 if strings.TrimSpace(string(raw))==`{"sell_state":"paused"}`{return false,nil}
 var posture agent.SellPosture
 if decodeSetupJSON(raw,&posture)!=nil{return false,fmt.Errorf("existing sell posture conflicts; inspect locally")}
 switch posture.SellState{
 case "selling","":return false,nil
 case "paused":
  if posture.Until==""{return true,nil}
  until,err:=time.Parse(time.RFC3339Nano,posture.Until)
  if err!=nil{return false,fmt.Errorf("existing sell posture conflicts; inspect locally")}
  return until.After(now),nil
 }
 return false,fmt.Errorf("existing sell posture conflicts; inspect locally")
}
func prepareActivationSell(path string,now time.Time)error{
 guard:=agentPayload{agentFile{path,digestSetupBytes(activationSellGuard),0600},activationSellGuard}
 _,e:=os.Lstat(path)
 if os.IsNotExist(e){return writeAgentFile(path,guard)}
 if e!=nil{return fmt.Errorf("cannot inspect sell posture")}
 raw,e:=readSetupRegular(path,true)
 if e!=nil{return fmt.Errorf("existing sell posture conflicts; inspect locally")}
 keep,err:=activationKeepsSell(raw,now);if err!=nil||keep{return err}
 // The service is stopped here, so replacing the file cannot race the agent.
 if err=os.Remove(path);err!=nil{return fmt.Errorf("cannot replace sell posture")}
 return writeAgentFile(path,guard)
}

// releaseActivationSell asks the running agent to clear the activation guard.
// An owner pause is left in place.
func releaseActivationSell(ctx context.Context)error{
 return releaseActivationSellAt(ctx,"/var/lib/hypermesh",45*time.Second)
}
func releaseActivationSellAt(ctx context.Context,stateDir string,limit time.Duration)error{
 release,cancel:=context.WithTimeout(ctx,limit);defer cancel()
 for{
  var reply struct{SellState string `json:"sell_state"`;PauseSource string `json:"pause_source"`}
  err:=ownerSocketRequest(release,stateDir,"POST","/v0/activation_release",map[string]string{},&reply)
  if errors.Is(err,errOwnerRouteMissing){return releaseActivationSellFile(filepath.Join(stateDir,"sell.json"))}
  if err==nil&&reply.PauseSource!=agent.SellSourceActivation&&reply.SellState!=""{return nil}
  timer:=time.NewTimer(time.Second)
  select{
  case <-release.Done():timer.Stop()
   return fmt.Errorf("service is active but its activation sell pause was not released; run the same command again")
  case <-timer.C:
  }
 }
}

// releaseActivationSellFile clears the guard for an agent that predates the
// release route, for example one kept by an installer-only upgrade. Only the
// exact guard this installer wrote is replaced; any other posture is the
// agent's and is left alone.
func releaseActivationSellFile(path string)error{
 raw,err:=readSetupRegular(path,true)
 if err!=nil{return fmt.Errorf("cannot inspect sell posture")}
 if !bytes.Equal(bytes.TrimSpace(raw),activationSellGuard){return nil}
 f,err:=os.CreateTemp(filepath.Dir(path),".sell-")
 if err!=nil{return fmt.Errorf("cannot release activation sell pause")}
 defer os.Remove(f.Name())
 _,err=f.Write([]byte("{\"sell_state\":\"selling\"}\n"))
 if err==nil{err=f.Chmod(0o600)}
 if err==nil{err=f.Sync()}
 if e:=f.Close();err==nil{err=e}
 if err==nil{err=os.Rename(f.Name(),path)}
 if err!=nil{return fmt.Errorf("cannot release activation sell pause")}
 return nil
}
