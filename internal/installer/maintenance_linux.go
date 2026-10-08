//go:build linux

package installer

import (
 "bytes"
 "context"
 "encoding/json"
 "flag"
 "fmt"
 "io"
 "net"
 "net/http"
 "os"
 "path/filepath"
 "strings"
 "time"

 "github.com/FyberLabs/hypermesh-host/internal/hostregistration"
)
type maintenancePlan struct{
 Schema int `json:"schema"`
 SHA256 string `json:"plan_sha256"`
 Operation string `json:"operation"`
 Certification certificationPlan `json:"certification"`
 MachineSHA256 string `json:"machine_sha256"`
 BootID string `json:"boot_id"`
}
type maintenanceJournal struct{
 Schema int `json:"schema"`
 PlanSHA256 string `json:"plan_sha256"`
 State string `json:"state"`
 RecordedAt time.Time `json:"recorded_at"`
}
func maintenanceDigest(p maintenancePlan)string{p.SHA256="";raw,_:=json.Marshal(p);return digestSetupBytes(raw)}
func validateMaintenance(p maintenancePlan,sha string)error{
 if p.Schema!=1||!supportOneOf(p.Operation,"stop","reboot")||!setupDigestRE.MatchString(sha)||p.SHA256!=sha||maintenanceDigest(p)!=sha||
 !setupDigestRE.MatchString(p.MachineSHA256)||!hostregistration.UUID.MatchString(p.BootID){return fmt.Errorf("invalid maintenance approval")}
 return validateCertificationPlan(p.Certification,p.Certification.SHA256)
}
func saveMaintenance(path string,j maintenanceJournal)error{
 if j.Schema!=1||!setupDigestRE.MatchString(j.PlanSHA256)||!supportOneOf(j.State,"draining","stopping","stopped","reboot-requested","resuming","complete")||j.RecordedAt.IsZero(){return fmt.Errorf("invalid maintenance checkpoint")}
 if _,err:=os.Lstat(path);err==nil{if _,err=readSetupRegular(path,true);err!=nil{return err}}else if !os.IsNotExist(err){return err}
 raw,_:=json.Marshal(j);return recoveryWrite(path,raw,0600)
}
func maintenanceOwner(ctx context.Context,method,endpoint string,body any,out any)error{
 return ownerSocketRequest(ctx,"/var/lib/hypermesh",method,endpoint,body,out)
}
// ownerSocketRequest calls the agent's local owner socket in stateDir with
// the owner token stored beside it.
func ownerSocketRequest(ctx context.Context,stateDir,method,endpoint string,body any,out any)error{
 token,err:=readSetupRegular(filepath.Join(stateDir,"owner.token"),true);if err!=nil{return fmt.Errorf("local owner credential unavailable")}
 if len(token)>256||strings.TrimSpace(string(token))==""{return fmt.Errorf("invalid local owner credential")}
 sock:=filepath.Join(stateDir,"owner.sock")
 info,err:=os.Lstat(sock)
 if err!=nil||info.Mode()&os.ModeSocket==0||info.Mode().Perm()!=0600{return fmt.Errorf("local owner socket unavailable or unsafe")}
 raw,_:=json.Marshal(body)
 req,err:=http.NewRequestWithContext(ctx,method,"http://localhost"+endpoint,bytes.NewReader(raw));if err!=nil{return err}
 req.Header.Set("Authorization","Bearer "+strings.TrimSpace(string(token)));req.Header.Set("Content-Type","application/json")
 transport:=&http.Transport{DialContext:func(ctx context.Context,_,_ string)(net.Conn,error){return (&net.Dialer{}).DialContext(ctx,"unix",sock)}}
 defer transport.CloseIdleConnections()
 client:=http.Client{Transport:transport,Timeout:30*time.Second,CheckRedirect:func(*http.Request,[]*http.Request)error{return fmt.Errorf("redirect refused")}}
 response,err:=client.Do(req);if err!=nil{return fmt.Errorf("local owner request unconfirmed")}
 defer response.Body.Close()
 if response.StatusCode!=200{return fmt.Errorf("local owner request refused")}
 b,err:=io.ReadAll(io.LimitReader(response.Body,(64<<10)+1))
 if err!=nil||len(b)>64<<10||json.Unmarshal(b,out)!=nil{return fmt.Errorf("invalid local owner response")}
 return nil
}
func maintenanceSellAfter(ctx context.Context)string{
 wait,cancel:=context.WithTimeout(ctx,15*time.Second);defer cancel()
 for{
  var local struct{Maintenance bool `json:"maintenance"`;SellState string `json:"sell_state"`}
  if maintenanceOwner(wait,"GET","/v0/status",nil,&local)==nil&&!local.Maintenance&&local.SellState!=""{return local.SellState}
  timer:=time.NewTimer(time.Second)
  select{case <-wait.Done():timer.Stop();return "unknown";case <-timer.C:}
 }
}
// maintenanceHoldReleased decides whether resume may proceed without the
// matching hold. Only a missing marker on a resume that already reached the
// resuming checkpoint counts as released; any other hold problem stops.
func maintenanceHoldReleased(holdErr error,markerMissing bool,state string)(bool,error){
 if holdErr==nil{return false,nil}
 if markerMissing&&supportOneOf(state,"resuming","complete"){return true,nil}
 return false,holdErr
}
func maintenanceMarkerMatches(sha string)error{
 raw,err:=readSetupRegular("/var/lib/hypermesh/maintenance.json",true)
 var v struct{Schema int `json:"schema"`;PlanSHA256 string `json:"plan_sha256"`;RequestedAt time.Time `json:"requested_at"`}
 if err!=nil||decodeSetupJSON(raw,&v)!=nil||v.Schema!=1||v.PlanSHA256!=sha||v.RequestedAt.IsZero(){return fmt.Errorf("matching maintenance hold required")}
 return nil
}
func noMaintenanceContainers(ctx context.Context)error{
 config,err:=os.MkdirTemp(defaultSetupState,".maintenance-docker-");if err!=nil{return err};defer os.RemoveAll(config)
 raw,err:=workloadDocker(ctx,config,"ps","--filter","name=^/hypermesh-","--format","{{.Names}}")
 if err!=nil||strings.TrimSpace(string(raw))!=""{return fmt.Errorf("Hypermesh workload containers remain or cannot be inspected")}
 return nil
}
type maintenanceDeps struct{
 Hold func(context.Context)error
 Idle func(context.Context)error
 Stop func(context.Context)error
 Stopped func(context.Context)error
 Save func(maintenanceJournal)error
 Reboot func(context.Context)error
}
func drainMaintenance(ctx context.Context,p maintenancePlan,j *maintenanceJournal,d maintenanceDeps,retry bool)error{
 if j.PlanSHA256!=p.SHA256||j.Schema!=1||!supportOneOf(j.State,"draining","stopping","stopped","reboot-requested"){return fmt.Errorf("maintenance checkpoint cannot be applied")}
 if j.State=="reboot-requested"&&!retry{return fmt.Errorf("reboot already requested; reconnect and resume-agent, or explicitly inspect before --retry-request")}
 if j.State=="draining"{
  if err:=d.Hold(ctx);err!=nil{return err}
  if err:=d.Idle(ctx);err!=nil{return err}
  j.State="stopping";j.RecordedAt=time.Now().UTC();if err:=d.Save(*j);err!=nil{return err}
 }
 if j.State=="stopping"{
  if err:=d.Stop(ctx);err!=nil{return err}
  if err:=d.Stopped(ctx);err!=nil{return err}
  j.State="stopped";j.RecordedAt=time.Now().UTC();if err:=d.Save(*j);err!=nil{return err}
 }
 if err:=d.Stopped(ctx);err!=nil{return err}
 if p.Operation=="reboot"{
  j.State="reboot-requested";j.RecordedAt=time.Now().UTC()
  if err:=d.Save(*j);err!=nil{return err}
  if err:=ctx.Err();err!=nil{return err}
  if err:=d.Stopped(ctx);err!=nil{return err}
  if err:=d.Reboot(ctx);err!=nil{return fmt.Errorf("reboot outcome unconfirmed; preserve checkpoint and inspect before retry")}
 }
 return nil
}
func RunPlanMaintenance(ctx context.Context,args []string,out,diagnostic io.Writer)int{
 f:=flag.NewFlagSet("plan-maintenance",flag.ContinueOnError);f.SetOutput(diagnostic)
 path:=f.String("plan","","active certification plan");op:=f.String("operation","stop","stop or reboot")
 if err:=f.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
 if f.NArg()!=0||*path==""||!supportOneOf(*op,"stop","reboot"){fmt.Fprintln(diagnostic,"require --plan CERTIFICATION_PLAN --operation stop|reboot");return 2}
 if os.Geteuid()!=0{fmt.Fprintln(diagnostic,"maintenance requires root");return 1}
 p:=maintenancePlan{Schema:1,Operation:*op}
 raw,err:=readSetupRegular(*path,false);if err==nil{err=decodeSetupJSON(raw,&p.Certification)}
 if err==nil{err=validateCertificationPlan(p.Certification,p.Certification.SHA256)}
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 unlock,err:=supportLock(defaultSetupState);if err!=nil{fmt.Fprintln(diagnostic,err);return 1};defer unlock()
 ctx,cancel:=context.WithTimeout(ctx,90*time.Second);defer cancel()
 if err=verifyInstalledForActivation(ctx,"",p.Certification.Agent,true);err==nil{_,_,err=readCertificationIdentity(p.Certification)}
 if err==nil{err=checkAgentUnitPaths(ctx,rebootSetupSource(),"",agentPayloads(p.Certification.Agent,nil))}
 if err==nil{p.MachineSHA256,p.BootID,err=rebootIdentity("")}
 p.SHA256=maintenanceDigest(p)
 if err==nil{err=validateMaintenance(p,p.SHA256)}
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 if json.NewEncoder(out).Encode(p)!=nil{return 1};return 0
}
func RunMaintainAgent(ctx context.Context,args []string,out,diagnostic io.Writer)int{return runMaintenance(ctx,args,out,diagnostic,false)}
func RunResumeAgent(ctx context.Context,args []string,out,diagnostic io.Writer)int{return runMaintenance(ctx,args,out,diagnostic,true)}
func runMaintenance(ctx context.Context,args []string,out,diagnostic io.Writer,resume bool)int{
 f:=flag.NewFlagSet("maintain-agent",flag.ContinueOnError);f.SetOutput(diagnostic)
 path:=f.String("plan","","reviewed maintenance plan");sha:=f.String("approve-plan","","exact plan digest")
 apply:=f.Bool("apply",false,"approve drain and service stop");reboot:=f.Bool("reboot",false,"approve host reboot")
 start:=f.Bool("start",false,"approve original-owner authorization and service startup")
 retry:=f.Bool("retry-request",false,"explicitly retry unconfirmed same-boot reboot request")
 wait:=f.Duration("drain-timeout",30*time.Minute,"wait for current work to finish; at most 24h")
 if err:=f.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
 if f.NArg()!=0||*path==""||!setupDigestRE.MatchString(*sha)||*wait<=0||*wait>24*time.Hour||(!resume&&!*apply)||(resume&&(!*start||*apply||*reboot||*retry)){
  fmt.Fprintln(diagnostic,"require approved maintenance plan and --apply or resume --start");return 2}
 if os.Geteuid()!=0{fmt.Fprintln(diagnostic,"maintenance requires root");return 1}
 var p maintenancePlan;raw,err:=readSetupRegular(*path,false);if err==nil{err=decodeSetupJSON(raw,&p)}
 if err==nil{err=validateMaintenance(p,*sha)}
 if err==nil&&!resume&&(p.Operation=="reboot")!=*reboot{err=fmt.Errorf("reboot plan requires explicit --reboot")}
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 unlock,err:=lockRebootState("",agentSetupState(p.Certification.Agent));if err!=nil{fmt.Fprintln(diagnostic,err);return 1};defer unlock()
 ctx,cancel:=context.WithTimeout(ctx,*wait+10*time.Minute);defer cancel()
 machine,boot,err:=rebootIdentity("")
 if err!=nil||machine!=p.MachineSHA256||(!resume&&boot!=p.BootID)||(resume&&p.Operation=="reboot"&&boot==p.BootID){fmt.Fprintln(diagnostic,"maintenance machine/boot does not match; preserve state and use resume after reboot");return 1}
 identity,client,err:=readCertificationIdentity(p.Certification)
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 journalPath:=filepath.Join(defaultSetupState,"maintenance-"+p.SHA256+".json")
 j:=maintenanceJournal{Schema:1,PlanSHA256:p.SHA256,State:"draining",RecordedAt:time.Now().UTC()}
 if _,e:=os.Lstat(journalPath);e==nil{raw,err=readSetupRegular(journalPath,true);if err==nil{err=decodeSetupJSON(raw,&j)}}else if os.IsNotExist(e)&&!resume{err=saveMaintenance(journalPath,j)}else{err=fmt.Errorf("matching maintenance checkpoint required")}
 if err!=nil||j.Schema!=1||j.PlanSHA256!=p.SHA256||j.RecordedAt.IsZero(){fmt.Fprintln(diagnostic,"maintenance checkpoint invalid");return 1}
 save:=func(v maintenanceJournal)error{return saveMaintenance(journalPath,v)}
 installed:=func(ctx context.Context)error{
  if err:=verifyInstalledForActivation(ctx,"",p.Certification.Agent,true);err!=nil{return err}
  if err:=checkRegistrationIdentity("",identity,true);err!=nil{return err}
  return checkAgentUnitPaths(ctx,rebootSetupSource(),"",agentPayloads(p.Certification.Agent,nil))
 }
 stopped:=func(ctx context.Context)error{
  if err:=installed(ctx);err!=nil{return err}
  if err:=maintenanceMarkerMatches(p.SHA256);err!=nil{return err}
  if err:=recoveryGuard(ctx,"",p.Certification.Agent,rebootSetupSource());err!=nil{return err}
  return noMaintenanceContainers(ctx)
 }
 sellAfter:=""
 // A retry after the hold was released skips the paused-hold checks: the
 // marker is only removed after they passed, and the agent may since have
 // restored selling or the owner's pause.
 released:=false
 if resume{
  if !supportOneOf(j.State,"stopped","reboot-requested","resuming","complete"){err=fmt.Errorf("drain/stop must complete before resume")}
  if err==nil{
   holdErr:=maintenanceMarkerMatches(p.SHA256)
   _,e:=os.Lstat("/var/lib/hypermesh/maintenance.json")
   released,err=maintenanceHoldReleased(holdErr,os.IsNotExist(e),j.State)
  }
  if err==nil{err=installed(ctx)}
  if err==nil{err=noMaintenanceContainers(ctx)}
  if err==nil{needed,e:=liveRebootRequired("");if e!=nil||needed{err=fmt.Errorf("OS reboot requirement must be resolved before resume")}}
  if err==nil{
   j.State="resuming";j.RecordedAt=time.Now().UTC();err=save(j)
  }
  if err==nil{
   var cert certificationJournal
   raw,err=readSetupRegular(certificationStatePath(defaultSetupState,p.Certification.SHA256),true)
   if err==nil{err=decodeSetupJSON(raw,&cert)}
   if err==nil&&(!validCertificationJournal(cert)||cert.PlanSHA256!=p.Certification.SHA256||cert.RunID!=p.Certification.Run.RunID){err=fmt.Errorf("invalid certification checkpoint")}
   if err==nil{err=activateCertifiedMode(ctx,p.Certification,identity,client,&cert,func(v certificationJournal)error{return saveCertification(certificationStatePath(defaultSetupState,p.Certification.SHA256),v)},diagnostic,true)}
  }
  if err==nil&&!released{
   var local struct{DeviceID string `json:"device_id"`;SellState string `json:"sell_state"`;Until string `json:"until"`}
   err=maintenanceOwner(ctx,"GET","/v0/status",nil,&local)
   if err==nil&&(local.DeviceID!=identity.DeviceID||local.SellState!="paused"||local.Until!=""){err=fmt.Errorf("resume must retain an indefinite pause")}
  }
  if err==nil&&!released{
   var posture struct{SellState string `json:"sell_state"`;Until string `json:"until,omitempty"`}
   raw,err=readSetupRegular("/var/lib/hypermesh/sell.json",true)
   if err==nil&&(decodeSetupJSON(raw,&posture)!=nil||posture.SellState!="paused"||posture.Until!=""){err=fmt.Errorf("persistent indefinite pause required")}
  }
  if err==nil&&!released{
   remote,e:=client.CheckDevice(ctx,identity.Request,identity.DeviceID,identity.DeviceSecret)
   if e!=nil||remote.PathAState!="accepted"||remote.PathBState!="green"||remote.ScheduleHold==nil||*remote.ScheduleHold||remote.SellState!="paused"||remote.LastHeartbeatAt==nil||time.Since(*remote.LastHeartbeatAt)<0||time.Since(*remote.LastHeartbeatAt)>120*time.Second||remote.CheckedAt.Before(time.Now().Add(-30*time.Second))||remote.CheckedAt.After(time.Now().Add(30*time.Second)){err=fmt.Errorf("fresh plane readiness required before releasing maintenance hold")}
  }
  if err==nil{
   if _,e:=os.Lstat("/var/lib/hypermesh/maintenance.json");e==nil{
    err=maintenanceMarkerMatches(p.SHA256);if err==nil{err=os.Remove("/var/lib/hypermesh/maintenance.json")}
   }else if !os.IsNotExist(e){err=e}
  }
  if err==nil{err=recoverySync("/var/lib/hypermesh")}
  if err==nil{j.State="complete";j.RecordedAt=time.Now().UTC();err=save(j)}
  // With the hold released the agent restores a kept owner pause or sells.
  if err==nil{sellAfter=maintenanceSellAfter(ctx)}
 }else{
  d:=maintenanceDeps{
   Hold:func(ctx context.Context)error{
    if err:=installed(ctx);err!=nil{return err}
    var reply struct{Maintenance bool `json:"maintenance"`;SellingAllowed bool `json:"selling_allowed"`}
    if err:=maintenanceOwner(ctx,"POST","/v0/maintenance",map[string]string{"plan_sha256":p.SHA256},&reply);err!=nil{return err}
    if !reply.Maintenance||reply.SellingAllowed{return fmt.Errorf("agent does not confirm maintenance hold")}
    return maintenanceMarkerMatches(p.SHA256)
   },
   Idle:func(ctx context.Context)error{
    drain,cancel:=context.WithTimeout(ctx,*wait);defer cancel()
    for{
     var local struct{DeviceID string `json:"device_id"`;Maintenance bool `json:"maintenance"`;Pending bool `json:"work_pending"`;SellState string `json:"sell_state"`;Until string `json:"until"`;Lease json.RawMessage `json:"lease"`}
     e:=maintenanceOwner(drain,"GET","/v0/status",nil,&local)
     if e==nil&&local.DeviceID==identity.DeviceID&&local.Maintenance&&!local.Pending&&local.SellState=="paused"&&local.Until==""&&(len(local.Lease)==0||string(local.Lease)=="null"){
      remote,e:=client.CheckDevice(drain,identity.Request,identity.DeviceID,identity.DeviceSecret)
      if e==nil&&remote.SellState=="paused"&&remote.LastHeartbeatAt!=nil&&time.Since(*remote.LastHeartbeatAt)>=0&&time.Since(*remote.LastHeartbeatAt)<120*time.Second&&noMaintenanceContainers(drain)==nil{
       if _,e=os.Lstat("/var/lib/hypermesh/lease.json");os.IsNotExist(e){return nil}
      }
     }
     timer:=time.NewTimer(2*time.Second)
     select{case <-drain.Done():timer.Stop();return fmt.Errorf("drain incomplete; host remains paused, do not force reboot");case <-timer.C:}
    }
   },
   Stop:func(ctx context.Context)error{
    if err:=installed(ctx);err!=nil{return err}
    if err:=maintenanceMarkerMatches(p.SHA256);err!=nil{return err}
    if _,err:=os.Lstat("/var/lib/hypermesh/lease.json");!os.IsNotExist(err){return fmt.Errorf("lease appeared during drain")}
    if err:=noMaintenanceContainers(ctx);err!=nil{return err}
    if _,err:=liveSystemdOutput(ctx,"systemctl","--system","disable","--now","hypermesh-agent.service");err!=nil{return err}
    if p.Certification.Agent.Setup.WireGuardMode=="userspace"{_,err:=liveSystemdOutput(ctx,"systemctl","--system","disable","--now","hypermesh-wg.service");return err}
    return nil
   },Stopped:stopped,Save:save,Reboot:submitSetupReboot,
  }
  err=drainMaintenance(ctx,p,&j,d,*retry)
 }
 report:=map[string]any{"plan_sha256":p.SHA256,"state":j.State,"attempt_succeeded":err==nil,"selling_allowed":false}
 if sellAfter!=""{report["sell_state"]=sellAfter}
 if json.NewEncoder(out).Encode(report)!=nil{return 1}
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1};return 0
}
