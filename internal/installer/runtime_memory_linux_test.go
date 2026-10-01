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
)

func memoryFixture(t *testing.T)(SetupPlan,runtimeMemoryRequest,runtimeMemoryReceipt){
 t.Helper()
 p:=newApplyFixture(t,false,false).plan
 r,err:=memoryRequest(p);if err!=nil{t.Fatal(err)}
 v:=runtimeMemoryReceipt{1,r.Backend,r.Device,r.AllocationBytes,r.AllocationBytes,12288,true}
 return p,r,v
}
func TestRuntimeMemoryIntentFailureAndRetry(t *testing.T) {
 for _,kind:=range []string{"pass","preflight","runtime","mismatch","cleanup","cancel","save-intent","save-final","postflight"} {
  t.Run(kind,func(t *testing.T){
   p,_,receipt:=memoryFixture(t)
   var j,durable runtimeMemoryJournal
   calls,preflights:=0,0
   ctx,cancel:=context.WithCancel(context.Background());defer cancel()
   deps:=memoryCheckDeps{
    Preflight:func(context.Context)error{preflights++;if kind=="preflight"||(kind=="postflight"&&preflights==2){return fmt.Errorf("PRIVATE_PREFLIGHT")};return nil},
    Probe:func(context.Context,runtimeMemoryRequest)(runtimeMemoryReceipt,error){
     calls++
     if durable.State!="checking"||durable.Receipt!=nil{t.Fatal("allocation before durable intent")}
     if kind=="runtime"{return runtimeMemoryReceipt{},fmt.Errorf("PRIVATE_DRIVER")}
     if kind=="mismatch"{receipt.Device++}
     if kind=="cleanup"{receipt.CleanupComplete=false}
     if kind=="cancel"{cancel()}
     return receipt,nil
    },
    Save:func(value runtimeMemoryJournal)error{
     if !validMemoryJournal(value){t.Fatal("invalid journal")}
     if kind=="save-intent"||(kind=="save-final"&&value.State=="verified"){return fmt.Errorf("PRIVATE_STORAGE")}
     durable=value;return nil
    },
   }
   err:=checkRuntimeMemory(ctx,p,p.PlanSHA256,&j,deps)
   if (err==nil)!=(kind=="pass"){t.Fatalf("%s %v",kind,err)}
   if (kind=="preflight"||kind=="save-intent")&&calls!=0{t.Fatal("unexpected allocation")}
   if kind=="pass" {
    if j.State!="verified"||j.Receipt==nil||preflights!=2{t.Fatal("missing verified evidence")}
    deps.Probe=func(context.Context,runtimeMemoryRequest)(runtimeMemoryReceipt,error){return runtimeMemoryReceipt{},fmt.Errorf("failed retry")}
    if checkRuntimeMemory(context.Background(),p,p.PlanSHA256,&j,deps)==nil{t.Fatal("retry ignored failure")}
    if j.State=="verified"||durable.Receipt!=nil{t.Fatal("old success retained")}
   } else if j.Receipt!=nil||j.State=="verified"{t.Fatal("failure presented as success")}
   raw,_:=json.Marshal(j)
   if strings.Contains(string(raw),"PRIVATE")||(err!=nil&&strings.Contains(err.Error(),"PRIVATE")){t.Fatal("raw diagnostics leaked")}
  })
 }
}
func TestRuntimeMemoryApprovalAndEligibilityBeforeWork(t *testing.T){
 for _,kind:=range []string{"approval","changed-budget","discrete","unsafe-memory"}{
  t.Run(kind,func(t *testing.T){
   p,_,_:=memoryFixture(t);approved:=p.PlanSHA256
   switch kind {
   case "approval":approved=strings.Repeat("0",64)
   case "changed-budget":p.Preflight.Resources.Resources.UMAAllocationBytes++
   case "discrete":p.Preflight.Resources.MemoryMode="discrete";p.PlanSHA256=setupPlanDigest(p);approved=p.PlanSHA256
   case "unsafe-memory":p.Preflight.Resources.Resources.UMAAllocationBytes=64*GiB;p.PlanSHA256=setupPlanDigest(p);approved=p.PlanSHA256
   }
   deps:=memoryCheckDeps{Save:func(runtimeMemoryJournal)error{t.Fatal("wrote state before valid approval");return nil}}
   var j runtimeMemoryJournal
   if checkRuntimeMemory(context.Background(),p,approved,&j,deps)==nil{t.Fatal("accepted unsafe request")}
  })
 }
}
func TestRuntimeMemoryChildBoundAndEvidence(t *testing.T){
 _,r,v:=memoryFixture(t)
 raw,_:=json.Marshal(v)
 success:="print("+fmt.Sprintf("%q",string(raw))+")"
 for _,kind:=range []string{"pass","invalid","missing-cleanup","timeout","cancel"}{
  t.Run(kind,func(t *testing.T){
   script:=success
   if kind=="invalid"{script="print('PRIVATE_INVALID')"}
   if kind=="missing-cleanup"{script=strings.ReplaceAll(success,"true","false")}
   if kind=="timeout"{script=success+"\nimport time\ntime.sleep(60)"}
   ctx,cancel:=context.WithCancel(context.Background());defer cancel()
   if kind=="cancel"{cancel()}
   start:=time.Now()
   budget:=2*time.Second;if kind=="timeout"{budget=200*time.Millisecond}
   got,err:=executeMemoryProbe(ctx,r,budget,script)
   if time.Since(start)>3*time.Second{t.Fatal("child exceeded deadline")}
   if (err==nil)!=(kind=="pass"){t.Fatalf("%s %+v %v",kind,got,err)}
   if err!=nil&&strings.Contains(err.Error(),"PRIVATE"){t.Fatal("raw output leaked")}
  })
 }
 cmd:=memoryProbeCommand(context.Background(),r)
 if cmd.Path!="/usr/bin/python3"||cmd.Args[1]!="-I"||cmd.WaitDelay!=time.Second||!cmd.SysProcAttr.Setpgid{t.Fatal("unbounded/inherited child configuration")}
 if strings.Join(cmd.Env,"|")!="PATH=/usr/sbin:/usr/bin:/sbin:/bin|HOME=/root|LC_ALL=C"{t.Fatal("inherited environment")}
}
func TestMemoryServicesMustBeInactive(t *testing.T){
 for _,state:=range []string{"inactive","failed","active","activating","deactivating","unknown",""}{
  calls:=0
  source:=setupSource{Output:func(_ context.Context,name string,args ...string)([]byte,error){
   calls++;if name!="systemctl"||!strings.Contains(strings.Join(args," "),"ActiveState"){t.Fatal("unexpected mutation")}
   return []byte(state+"\n"),nil
  }}
  err:=inactiveMemoryServices(context.Background(),source)
  if (err==nil)!=(state=="inactive"||state=="failed"){t.Fatal(state,err)}
  if err==nil&&calls!=2{t.Fatal("did not check both services")}
 }
}
func TestRuntimeMemoryPrivateJournalAndCLI(t *testing.T){
 p,_,receipt:=memoryFixture(t)
 dir:=setupStateDir(t)
 path:=filepath.Join(dir,"setup-plan.json")
 raw,_:=json.Marshal(p)
 if err:=os.WriteFile(path,raw,0600);err!=nil{t.Fatal(err)}
 deps:=memoryCheckDeps{Preflight:func(context.Context)error{return nil},
  Probe:func(context.Context,runtimeMemoryRequest)(runtimeMemoryReceipt,error){return receipt,nil}}
 var out,diagnostic bytes.Buffer
 args:=[]string{"--setup-plan",path,"--approve-plan",p.PlanSHA256,"--allocate"}
 // Missing explicit allocation approval does not create the state directory.
 missing:=filepath.Join(dir,"missing")
 if runRuntimeMemory(context.Background(),args[:4],&out,&diagnostic,missing,true,&deps)!=2{t.Fatal("implicit allocation")}
 if _,err:=os.Stat(missing);!os.IsNotExist(err){t.Fatal("created state without opt-in")}
 if runRuntimeMemory(context.Background(),args,&out,&diagnostic,dir,false,&deps)!=1{t.Fatal("non-root accepted")}
 out.Reset();diagnostic.Reset()
 if runRuntimeMemory(context.Background(),args,&out,&diagnostic,dir,true,&deps)!=0{t.Fatal(diagnostic.String())}
 journalPath:=memoryJournalPath(dir,p.PlanSHA256)
 data,err:=readSetupRegular(journalPath,true);if err!=nil{t.Fatal(err)}
 var j runtimeMemoryJournal
 if decodeSetupJSON(data,&j)!=nil||!validMemoryJournal(j)||j.State!="verified"{t.Fatal("invalid persisted evidence")}
 if err:=os.Chmod(journalPath,0644);err!=nil{t.Fatal(err)}
 if saveMemoryJournal(journalPath,j)==nil{t.Fatal("overwrote unsafe journal")}
}
func TestRuntimeMemoryJournalRejectsIncompleteEvidence(t *testing.T){
 p,r,v:=memoryFixture(t);now:=time.Now().UTC()
 j:=runtimeMemoryJournal{Schema:1,SetupPlanSHA256:p.PlanSHA256,State:"verified",StartedAt:now,RecordedAt:now,Request:r,Receipt:&v}
 if !validMemoryJournal(j){t.Fatal("valid evidence refused")}
 changes:=[]func(*runtimeMemoryJournal){
  func(j *runtimeMemoryJournal){j.Receipt=nil},
  func(j *runtimeMemoryJournal){j.State="checking"},
  func(j *runtimeMemoryJournal){j.RecordedAt=j.StartedAt.Add(-time.Second)},
  func(j *runtimeMemoryJournal){j.FailureCode="runtime"},
  func(j *runtimeMemoryJournal){j.Request.AllocationBytes++},
 }
 for _,change:=range changes{copy:=j;change(&copy);if validMemoryJournal(copy){t.Fatal("incomplete evidence accepted")}}
 dir:=setupStateDir(t)
 j.NextAction="PRIVATE_RAW_NOTE"
 if err:=saveMemoryJournal(memoryJournalPath(dir,p.PlanSHA256),j);err!=nil{t.Fatal(err)}
 summary:=supportRuntimeMemory(dir,p.PlanSHA256)
 raw,_:=json.Marshal(summary)
 if !summary.MemoryVerified||strings.Contains(string(raw),"PRIVATE"){t.Fatal("invalid/redaction-losing summary")}
 j.Receipt=nil
 data,_:=json.Marshal(j)
 if err:=os.WriteFile(memoryJournalPath(dir,p.PlanSHA256),data,0600);err!=nil{t.Fatal(err)}
 if supportRuntimeMemory(dir,p.PlanSHA256).Collection!="invalid"{t.Fatal("corrupt memory proof accepted")}
}
