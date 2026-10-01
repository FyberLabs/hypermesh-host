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
func onboardingFixture()hostregistration.OnboardingSnapshot {
 yes,no:=true,false
 return hostregistration.OnboardingSnapshot{CheckedAt:time.Now().UTC(),ClassAvailable:&yes,
 PathAState:"pending",PathBState:"never-run",SellState:"paused",ScheduleHold:&no,
 ActivationAllowed:&no,SellingAllowed:&no,TunnelVerified:&no}
}
func TestOnboardingSuccessRetainsPendingGatesAndCannotActivate(t *testing.T){
 _,registration:=registrationFixture(t)
 j:=onboardingJournal{Schema:1,PlanSHA256:registration.PlanSHA256,RequestID:registration.Request.RequestID,
 DeviceID:registration.Request.RequestID,HostName:registration.Request.HostName}
 events:=[]string{}
 snapshot:=onboardingFixture()
 deps:=onboardingDeps{
 Verify:func(context.Context)error{events=append(events,"verify");return nil},
 Preflight:func(context.Context)error{events=append(events,"preflight");return nil},
 Remote:func(context.Context)(hostregistration.OnboardingSnapshot,error){events=append(events,"remote");return snapshot,nil},
 Save:func(value onboardingJournal)error{
  events=append(events,"save:"+value.State)
  if value.ActivationAllowed||value.SellingAllowed{t.Fatal("onboarding read authorized activation")}
  return nil
 },
 }
 if err:=performOnboardingCheck(context.Background(),&j,deps);err!=nil{t.Fatal(err)}
 if strings.Join(events,",")!="save:checking,verify,preflight,remote,save:checked"{t.Fatal(events)}
 if !j.ProtocolVerified||!j.LocalPreflightPassed||j.PlaneCheckedAt==nil||j.ReportedSellState!="paused"{t.Fatal("missing successful evidence")}
 gates:=map[string]onboardingGate{}
 for _,gate:=range j.Gates{gates[gate.ID]=gate}
 for _,id:=range []string{"path_a","path_b","heartbeat","aiuditor","tunnel","activation"}{
  if gates[id].State!="pending"{t.Fatal("unverified gate passed",id)}
 }
 if gates["selling"].State!="blocked"{t.Fatal("selling allowed")}
}
func TestOnboardingFailuresReplaceStaleSuccessAndStopNextSteps(t *testing.T){
 for _,failure:=range []string{"verify","preflight","remote","save","cancel"}{t.Run(failure,func(t *testing.T){
  j:=onboardingJournal{State:"checked",ProtocolVerified:true,LocalPreflightPassed:true,
   Gates:[]onboardingGate{{"old","passed","obsolete"}},ActivationAllowed:true,SellingAllowed:true}
  ctx,cancel:=context.WithCancel(context.Background());defer cancel()
  calls:=[]string{}
  var saved onboardingJournal
  deps:=onboardingDeps{
   Save:func(value onboardingJournal)error{
    if value.State=="checking"&&(value.ProtocolVerified||value.LocalPreflightPassed||len(value.Gates)>0||value.ActivationAllowed||value.SellingAllowed){t.Fatal("stale success persisted")}
    if failure=="save"{return fmt.Errorf("disk full")}
    saved=value;return nil
   },
   Verify:func(context.Context)error{calls=append(calls,"verify");if failure=="verify"{return fmt.Errorf("private-error-material")};return nil},
   Preflight:func(context.Context)error{calls=append(calls,"preflight");if failure=="preflight"{return fmt.Errorf("private-error-material")};return nil},
   Remote:func(context.Context)(hostregistration.OnboardingSnapshot,error){
    calls=append(calls,"remote")
    if failure=="remote"{return hostregistration.OnboardingSnapshot{},fmt.Errorf("private-error-material")}
    if failure=="cancel"{cancel()}
    return onboardingFixture(),nil
   },
  }
  if performOnboardingCheck(ctx,&j,deps)==nil{t.Fatal("ignored failure")}
  count:=map[string]int{"save":0,"verify":1,"preflight":2,"remote":3,"cancel":3}[failure]
  if len(calls)!=count{t.Fatal("continued after failure",calls)}
  if failure!="save"&&saved.State!="failed"{t.Fatal("did not persist failure")}
  raw,_:=json.Marshal(saved)
  if strings.Contains(string(raw),"private-error-material")||strings.Contains(string(raw),"obsolete")||
   saved.ProtocolVerified||saved.ActivationAllowed||saved.SellingAllowed{t.Fatal("leaked error or retained stale authority")}
 })}
}
func TestOnboardingSnapshotReportsHoldsMissingClassAndSuspension(t *testing.T){
 s:=onboardingFixture();no,yes:=false,true
 s.ClassAvailable=&no;s.ScheduleHold=&yes;s.PathAState="rejected";s.PathBState="suspended";s.SellState="selling"
 gates:=onboardingGates(s);blocked:=map[string]bool{}
 for _,gate:=range gates{if gate.State=="blocked"{blocked[gate.ID]=true}}
 for _,id:=range []string{"class","path_a","path_b","schedule","selling"}{if !blocked[id]{t.Fatal("missing blocker",id)}}
 s.PathAState="accepted";s.PathBState="green"
 for _,gate:=range onboardingGates(s){if (gate.ID=="activation"||gate.ID=="selling")&&gate.State=="passed"{t.Fatal("green certificate bypassed activation")}}
}
func TestOnboardingJournalPreservesIdentityFilesAndIsPrivate(t *testing.T){
 p,registration:=registrationFixture(t)
 dir:=t.TempDir();if err:=os.Chmod(dir,0o700);err!=nil{t.Fatal(err)}
 identityPath:=filepath.Join(dir,"registration-"+p.SHA256+".json")
 if err:=saveRegistration(identityPath,registration);err!=nil{t.Fatal(err)}
 before,err:=os.ReadFile(identityPath);if err!=nil{t.Fatal(err)}
 path:=onboardingPath(dir,p.SHA256)
 j:=onboardingJournal{Schema:1,PlanSHA256:p.SHA256,RequestID:registration.Request.RequestID,
 DeviceID:registration.Request.RequestID,HostName:registration.Request.HostName,State:"checked",RecordedAt:time.Now().UTC()}
 if err=saveOnboarding(path,j);err!=nil{t.Fatal(err)}
 raw,err:=readSetupRegular(path,true);if err!=nil{t.Fatal(err)}
 after,err:=os.ReadFile(identityPath);if err!=nil{t.Fatal(err)}
 if !bytes.Equal(before,after){t.Fatal("check changed registration identity")}
 for _,private:=range []string{registration.PrivateKey,registration.DeviceSecret,registration.Request.WGPublicKey,registration.Request.DeviceSecretHash}{
  if strings.Contains(string(raw),private){t.Fatal("private identity in status")}
 }
 if err=os.Chmod(path,0o644);err!=nil{t.Fatal(err)}
 if saveOnboarding(path,j)==nil{t.Fatal("accepted unsafe status file")}
 os.Remove(path)
 if err=os.Symlink(identityPath,path);err!=nil{t.Fatal(err)}
 if saveOnboarding(path,j)==nil{t.Fatal("followed symlink into private identity")}
 after,_=os.ReadFile(identityPath)
 if !bytes.Equal(before,after){t.Fatal("overwrote private identity")}
}
func TestCheckAgentArgumentValidation(t *testing.T){
 var out,diagnostic bytes.Buffer
 if RunCheckAgent(context.Background(),nil,&out,&diagnostic)!=2{t.Fatal("accepted missing plan")}
 if RunCheckAgent(context.Background(),[]string{"--help"},&out,&diagnostic)!=0{t.Fatal("help failed")}
 if RunOnboardingStatus(context.Background(),[]string{"--plan-sha","../../device.json"},&out,&diagnostic)!=2{t.Fatal("accepted unsafe status path")}
}

func TestOnboardingFinalSaveFailureNeverEmitsSuccess(t *testing.T){
 var j onboardingJournal
 var durable onboardingJournal
 saves:=0
 deps:=onboardingDeps{
  Verify:func(context.Context)error{return nil},
  Preflight:func(context.Context)error{return nil},
  Remote:func(context.Context)(hostregistration.OnboardingSnapshot,error){return onboardingFixture(),nil},
  Save:func(value onboardingJournal)error{
   saves++
   if value.State=="checked"{return fmt.Errorf("rename or fsync failed")}
   durable=value
   return nil
  },
 }
 if performOnboardingCheck(context.Background(),&j,deps)==nil{t.Fatal("ignored final save failure")}
 if saves!=2||durable.State!="checking"{t.Fatal("unexpected durable state",durable.State)}
 if j.State!="failed"||j.FailureCode!="status_persistence"||j.ProtocolVerified||j.LocalPreflightPassed||
  j.PlaneCheckedAt!=nil||len(j.Gates)!=0||j.ActivationAllowed||j.SellingAllowed{t.Fatal("success exposed after failed persistence")}
 raw,_:=json.Marshal(j)
 if strings.Contains(string(raw),"\"state\":\"checked\"")||strings.Contains(string(raw),"\"protocol_verified\":true"){t.Fatal("success on stdout")}
}
