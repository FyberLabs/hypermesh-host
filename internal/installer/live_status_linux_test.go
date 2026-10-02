//go:build linux

package installer

import (
 "bytes"
 "context"
 "encoding/json"
 "fmt"
 "os"
 "path/filepath"
 "strings"
 "testing"
 "time"

 "github.com/FyberLabs/hypermesh-host/internal/hostregistration"
)

func livePlanFixture(t *testing.T)(certificationPlan,registrationJournal) {
 t.Helper()
 a,j:=registrationFixture(t)
 id,err:=hostregistration.NewRunID();if err!=nil{t.Fatal(err)}
 j.Owner="PRIVATE_OWNER";j.State="registered-inactive";j.DeviceID=id
 no:=false
 p:=certificationPlan{Schema:1,Agent:a,Run:hostregistration.CertificationRun{
  ProtocolVersion:1,RunID:id,TenantID:a.TenantID,DeviceID:id,RequestID:j.Request.RequestID,
  State:"planned",SellingAllowed:&no,ManifestSHA256:strings.Repeat("e",64),ExpiresAt:time.Now().Add(time.Hour),
  Manifest:hostregistration.CertificationManifest{SuiteID:"pack-v1",CatalogID:"reviewed",ClassID:"agx-large",
   FileHash:"sha256:"+strings.Repeat("c",64),ImageDigest:"private.invalid/runtime@sha256:"+strings.Repeat("d",64),
   SourceRef:"bartowski/reviewed@"+strings.Repeat("a",40)+"/model.gguf",SizeBytes:123,
   Runtime:"nvidia",Backend:"cuda-jetson",ContextTokens:4096,FullContext:true,DecodeTokS:10,
  },
 }}
 p.ModelPath=filepath.Join(a.Setup.Preflight.Resources.ModelDirectory,"cache",strings.Repeat("c",64)+".gguf")
 p.SHA256=certificationDigest(p)
 if err=validateCertificationPlan(p,p.SHA256);err!=nil{t.Fatal(err)}
 return p,j
}
func liveServiceFixture()[]byte {
 return []byte("NeedDaemonReload=no\nFragmentPath=/etc/systemd/system/hypermesh-agent.service\nDropInPaths=\nLoadState=loaded\nUnitFileState=enabled\nActiveState=active\nSubState=running\nMainPID=123\nNRestarts=0\nInvocationID="+strings.Repeat("a",32))
}
func liveDepsFixture(now time.Time)liveStatusDeps {
 yes,no:=true,false
 heartbeat:=now.Add(-30*time.Second)
 return liveStatusDeps{
  Now:func()time.Time{return now},
  Installation:func(context.Context)error{return nil},
  Identity:func()error{return nil},
  Certification:func()supportStage{return supportStage{Component:"certification",Collection:"collected",State:"active",NextAction:"Inspect current state."}},
  Marker:func()(bool,error){return true,nil},
  Service:func(context.Context)([]byte,error){return liveServiceFixture(),nil},
  Reboot:func()(bool,error){return false,nil},
  Remote:func(context.Context)(hostregistration.OnboardingSnapshot,error){
   return hostregistration.OnboardingSnapshot{CheckedAt:now,ClassAvailable:&yes,ScheduleHold:&no,
    AIuditorProgramState:"ready",PathAState:"accepted",PathBState:"green",SellState:"paused",LastHeartbeatAt:&heartbeat},nil
  },
 }
}
func TestLiveHostStatusGatesAndRedaction(t *testing.T) {
 for _,kind:=range []string{"pass","installation","identity","history","activating","marker","marker-error","service","restart","disabled","reboot","reboot-error","remote","clock-old","clock-future","class","intake","certification","hold","aiuditor","heartbeat-missing","heartbeat-old","heartbeat-future","sell-unknown","cancel"} {
  t.Run(kind,func(t *testing.T){
   now:=time.Now().UTC();d:=liveDepsFixture(now);calls:=0
   original:=d.Remote
   d.Remote=func(ctx context.Context)(hostregistration.OnboardingSnapshot,error){
    calls++;s,err:=original(ctx)
    switch kind {
    case "remote":return s,fmt.Errorf("PRIVATE_TOKEN https://private.invalid")
    case "clock-old":s.CheckedAt=now.Add(-time.Hour)
    case "clock-future":s.CheckedAt=now.Add(time.Hour)
    case "class":*s.ClassAvailable=false
    case "intake":s.PathAState="rejected"
    case "certification":s.PathBState="suspended"
    case "hold":*s.ScheduleHold=true
    case "sell-unknown":s.SellState="unknown"
    case "aiuditor":s.AIuditorProgramState="unauthorized"
    case "heartbeat-missing":s.LastHeartbeatAt=nil
    case "heartbeat-old":stamp:=now.Add(-121*time.Second);s.LastHeartbeatAt=&stamp
    case "heartbeat-future":stamp:=now.Add(time.Second);s.LastHeartbeatAt=&stamp
    }
    return s,err
   }
   switch kind {
   case "installation":d.Installation=func(context.Context)error{return fmt.Errorf("PRIVATE_PATH")}
   case "identity":d.Identity=func()error{return fmt.Errorf("PRIVATE_SECRET")}
   case "history":d.Certification=func()supportStage{return supportUnavailable("certification","invalid")}
   case "activating":d.Certification=func()supportStage{return supportStage{Collection:"collected",State:"activating",NextAction:"Reconcile activation."}}
   case "marker":d.Marker=func()(bool,error){return false,nil}
   case "marker-error":d.Marker=func()(bool,error){return false,fmt.Errorf("PRIVATE_PATH")}
   case "service":d.Service=func(context.Context)([]byte,error){return []byte("PRIVATE_SYSTEMD"),fmt.Errorf("PRIVATE_ERROR")}
   case "disabled":d.Service=func(context.Context)([]byte,error){return bytes.ReplaceAll(liveServiceFixture(),[]byte("enabled"),[]byte("disabled")),nil}
   case "restart":
    probes:=0;d.Service=func(context.Context)([]byte,error){probes++;raw:=liveServiceFixture();if probes>1{raw=bytes.ReplaceAll(raw,[]byte("MainPID=123"),[]byte("MainPID=124"))};return raw,nil}
   case "reboot":d.Reboot=func()(bool,error){return true,nil}
   case "reboot-error":d.Reboot=func()(bool,error){return false,fmt.Errorf("PRIVATE_ERROR")}
   }
   ctx,cancel:=context.WithCancel(context.Background());defer cancel()
   if kind=="cancel"{cancel()}
   report:=collectLiveHostStatus(ctx,certificationPlan{SHA256:strings.Repeat("a",64)},d)
   if report.AttentionRequired!=(kind!="pass"){t.Fatalf("%s: %+v",kind,report)}
   if report.ActivationAllowed||report.SellingAllowed||report.TunnelProbed{t.Fatal("observation granted authority")}
   if (kind=="identity"||kind=="cancel")&&calls!=0{t.Fatal("queried plane without verified identity")}
   if kind=="pass"&&(calls!=1||report.HeartbeatAgeSeconds==nil||*report.HeartbeatAgeSeconds!=30||len(report.Gates)!=14){t.Fatalf("%+v",report)}
   raw,_:=json.Marshal(report)
   if strings.Contains(string(raw),"PRIVATE_")||strings.Contains(string(raw),"private.invalid"){t.Fatal("untrusted output leaked")}
  })
 }
}
func TestLiveServiceRejectsIncompleteAndUnstableState(t *testing.T) {
 for _,kind:=range []string{"valid","reload","fragment","dropin","absent","masked","exited","pid","invocation","restarts","duplicate-empty","missing","extra"}{
  raw:=string(liveServiceFixture())
  switch kind {
  case "reload":raw=strings.Replace(raw,"NeedDaemonReload=no","NeedDaemonReload=yes",1)
  case "fragment":raw=strings.Replace(raw,"FragmentPath=/etc/systemd/system/hypermesh-agent.service","FragmentPath=/run/systemd/system/hypermesh-agent.service",1)
  case "dropin":raw=strings.Replace(raw,"DropInPaths=","DropInPaths=/private/override.conf",1)
  case "absent":raw=strings.Replace(raw,"LoadState=loaded","LoadState=not-found",1)
  case "masked":raw=strings.Replace(raw,"UnitFileState=enabled","UnitFileState=masked",1)
  case "exited":raw=strings.Replace(raw,"SubState=running","SubState=exited",1)
  case "pid":raw=strings.Replace(raw,"MainPID=123","MainPID=0",1)
  case "invocation":raw=strings.Replace(raw,strings.Repeat("a",32),"",1)
  case "restarts":raw=strings.Replace(raw,"NRestarts=0","NRestarts=-1",1)
  case "duplicate-empty":raw="InvocationID=\n"+raw
  case "missing":raw=strings.Replace(raw,"LoadState=loaded\n","",1)
  case "extra":raw+="\nPrivateToken=do-not-export"
  }
  _,ok,err:=liveService([]byte(raw))
  if (ok&&err==nil)!=(kind=="valid"){t.Fatalf("accepted %s",kind)}
 }
}
func TestLiveCertificationRequiresMatchingRunAndPins(t *testing.T) {
 for _,kind:=range []string{"valid","run","hash","image","context","plan"} {
  t.Run(kind,func(t *testing.T){
   p,_:=livePlanFixture(t);dir,j:=supportCertificationFixture(t)
   j.PlanSHA256=p.SHA256;j.RunID=p.Run.RunID;j.State="active"
   writeSupportCertificationFixture(t,dir,j)
   switch kind{
   case "run":p.Run.RunID,_=hostregistration.NewRunID()
   case "hash":p.Run.Manifest.FileHash="sha256:"+strings.Repeat("e",64)
   case "image":p.Run.Manifest.ImageDigest="other@sha256:"+strings.Repeat("e",64)
   case "context":p.Run.Manifest.ContextTokens=8192
   case "plan":p.SHA256=strings.Repeat("e",64)
   }
   stage:=liveCertificationHistory(dir,p)
   if (stage.Collection=="collected")!=(kind=="valid"){t.Fatalf("%+v",stage)}
  })
 }
}
func TestLiveIdentityAndRebootAreReadOnly(t *testing.T) {
 p,j:=livePlanFixture(t);root:=t.TempDir();state:=filepath.Join(root,"state")
 if err:=os.Mkdir(state,0700);err!=nil{t.Fatal(err)}
 if err:=saveRegistration(registrationPath(state,p.Agent.SHA256),j);err!=nil{t.Fatal(err)}
 if err:=publishRegistration(root,j);err!=nil{t.Fatal(err)}
 if _,_,err:=liveIdentity(root,state,p);err!=nil{t.Fatal(err)}
 before,_:=os.ReadFile(registrationPath(state,p.Agent.SHA256))
 changed:=p;changed.Run.DeviceID,_=hostregistration.NewRunID()
 if _,_,err:=liveIdentity(root,state,changed);err==nil{t.Fatal("unbound identity")}
 after,_:=os.ReadFile(registrationPath(state,p.Agent.SHA256));if !bytes.Equal(before,after){t.Fatal("changed registration")}
 if needed,err:=liveRebootRequired(root);err!=nil||needed{t.Fatal("invented reboot")}
 marker:=agentDest(root,"/run/reboot-required")
 if err:=os.MkdirAll(filepath.Dir(marker),0755);err!=nil{t.Fatal(err)}
 if err:=os.WriteFile(marker,[]byte("PRIVATE_REBOOT_PACKAGES"),0644);err!=nil{t.Fatal(err)}
 if needed,err:=liveRebootRequired(root);err!=nil||!needed{t.Fatal("missed reboot")}
 if err:=os.Remove(marker);err!=nil{t.Fatal(err)}
 if err:=os.Symlink("/private",marker);err!=nil{t.Fatal(err)}
 if _,err:=liveRebootRequired(root);err==nil{t.Fatal("followed marker link")}
}
func TestHostStatusCLIReadOnlyLockAndExit(t *testing.T) {
 p,_:=livePlanFixture(t);dir:=t.TempDir();if err:=os.Chmod(dir,0700);err!=nil{t.Fatal(err)}
 unlock,err:=lockSetupState(dir);if err!=nil{t.Fatal(err)};unlock()
 path:=filepath.Join(dir,"plan.json");raw,_:=json.Marshal(p)
 if err=os.WriteFile(path,raw,0600);err!=nil{t.Fatal(err)}
 d:=liveDepsFixture(time.Now().UTC())
 var out,diagnostic bytes.Buffer
 args:=[]string{"--plan",path}
 if code:=runHostStatus(context.Background(),args,&out,&diagnostic,dir,true,&d);code!=0{t.Fatalf("%d: %s",code,diagnostic.String())}
 var report liveHostStatus;if err=json.Unmarshal(out.Bytes(),&report);err!=nil{t.Fatal(err)}
 if report.AttentionRequired||report.PlanSHA256!=p.SHA256{t.Fatal("incorrect report")}
 after,_:=os.ReadFile(path);if !bytes.Equal(raw,after){t.Fatal("changed plan")}
 entries,_:=os.ReadDir(dir);if len(entries)!=2{t.Fatal("created state")}
 unlock,err=lockSetupState(dir);if err!=nil{t.Fatal(err)}
 if runHostStatus(context.Background(),args,&out,&diagnostic,dir,true,&d)!=1{t.Fatal("read during writer")}
 unlock()
 d.Reboot=func()(bool,error){return true,nil}
 if runHostStatus(context.Background(),args,&out,&diagnostic,dir,true,&d)!=1{t.Fatal("blocker returned success")}
 if runHostStatus(context.Background(),args,&out,&diagnostic,dir,false,&d)!=1{t.Fatal("non-root accepted")}
 if runHostStatus(context.Background(),nil,&out,&diagnostic,dir,true,&d)!=2{t.Fatal("missing args")}
 if runHostStatus(context.Background(),[]string{"--help"},&out,&diagnostic,dir,false,&d)!=0{t.Fatal("help")}
 if err=os.WriteFile(path,[]byte("PRIVATE_BAD_PLAN"),0600);err!=nil{t.Fatal(err)}
 if runHostStatus(context.Background(),args,&out,&diagnostic,dir,true,&d)!=1||strings.Contains(diagnostic.String(),"PRIVATE_BAD_PLAN"){t.Fatal("bad plan handling")}
}
