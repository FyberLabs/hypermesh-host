//go:build linux

package installer

import (
 "context"
 "fmt"
 "os"
 "path/filepath"
 "testing"
 "time"
)

// Real managed files and durable checkpoints; network/systemd are deliberate
// fakes. This is the lifecycle contract, not physical hardware acceptance.
func TestLifecycleInstallDrainRepairResumeUninstall(t *testing.T) {
 ctx:=context.Background()
 p:=agentInstallFixture(t)
 files:=agentPayloads(p,[]byte("verified agent"))
 root:=t.TempDir()
 install:=agentInstallJournal{Schema:1,PlanSHA256:p.SHA256}
 if err:=installAgentFiles(ctx,root,files,false,&install,func(agentInstallJournal)error{return nil});err!=nil{t.Fatal(err)}
 identity:=agentDest(root,"/var/lib/hypermesh/device.json")
 models:=agentDest(root,"/models/model.gguf")
 for _,path:=range []string{identity,models}{
  if err:=os.MkdirAll(filepath.Dir(path),0700);err!=nil{t.Fatal(err)}
  if err:=os.WriteFile(path,[]byte("preserve"),0600);err!=nil{t.Fatal(err)}
 }
 active,held,selling:=false,false,false
 activate:=func(){
  t.Helper()
  j:=certificationJournal{State:"passed"}
  d:=activationDeps{
   Save:func(certificationJournal)error{return nil},
   Publish:func()error{return nil},
   Start:func(context.Context)error{active=true;selling=false;return nil},
   Verify:func(context.Context)error{if !active||selling{return fmt.Errorf("unsafe startup")};return nil},
   Rollback:func(context.Context)error{active=false;return nil},
  }
  if err:=startCertified(ctx,&j,time.Now().Add(time.Minute),d);err!=nil{t.Fatal(err)}
 }
 activate()
 stop:=func(){
  t.Helper()
  mp:=maintenancePlan{SHA256:p.SHA256,Operation:"stop"}
  j:=maintenanceJournal{Schema:1,PlanSHA256:mp.SHA256,State:"draining",RecordedAt:time.Now()}
  d:=maintenanceDeps{
   Hold:func(context.Context)error{held=true;selling=false;return nil},
   Idle:func(context.Context)error{if !held||selling{return fmt.Errorf("not drained")};return nil},
   Stop:func(context.Context)error{active=false;return nil},
   Stopped:func(context.Context)error{if active||!held{return fmt.Errorf("unsafe maintenance")};return nil},
   Save:func(maintenanceJournal)error{return nil},
  }
  if err:=drainMaintenance(ctx,mp,&j,d,false);err!=nil{t.Fatal(err)}
 }
 recover:=func(op string){
  t.Helper()
  rp:=recoveryPlan{Schema:1,Operation:op,Agent:p}
  for _,file:=range files{snap,err:=recoverySnapshot(root,file.Path);if err!=nil{t.Fatal(err)};rp.Before=append(rp.Before,snap)}
  rp.SHA256=recoveryDigest(rp)
  dir:=filepath.Join(root,"recovery-"+op)
  if err:=secureSetupDirectory(dir,true);err!=nil{t.Fatal(err)}
  j:=recoveryJournal{Schema:1,PlanSHA256:rp.SHA256,State:"prepared",RecordedAt:time.Now()}
  guard:=func(context.Context)error{if active||!held||selling{return fmt.Errorf("not in maintenance")};return nil}
  if err:=applyRecovery(ctx,root,dir,rp,files,&j,guard,func(v recoveryJournal)error{return saveRecovery(dir,v)});err!=nil{t.Fatal(err)}
  for _,path:=range []string{identity,models}{raw,err:=os.ReadFile(path);if err!=nil||string(raw)!="preserve"{t.Fatal("identity or model lost")}}
 }
 stop()
 if err:=os.WriteFile(agentDest(root,files[0].Path),[]byte("damaged"),0755);err!=nil{t.Fatal(err)}
 recover("repair")
 for _,file:=range files{ok,err:=agentFileMatches(agentDest(root,file.Path),file);if err!=nil||!ok{t.Fatal("repair differs",err)}}
 activate()
 held=false
 if !active||selling{t.Fatal("resume must run paused")}
 stop()
 recover("uninstall")
 for _,file:=range files{if _,err:=os.Lstat(agentDest(root,file.Path));!os.IsNotExist(err){t.Fatal("managed file remains")}}
 if active||selling{t.Fatal("uninstall activated host")}
}

func TestRecoveryEveryCheckpointResumesFromDisk(t *testing.T) {
 for _,op:=range []string{"repair","uninstall"} {
  for failure:=1;failure<=10;failure++ {
   t.Run(fmt.Sprintf("%s/%d",op,failure),func(t *testing.T){
    p,files,root,dir,j:=recoveryFixture(t,op)
    durable:=j
    count:=0
    err:=applyRecovery(context.Background(),root,dir,p,files,&j,func(context.Context)error{return nil},func(v recoveryJournal)error{
     count++
     if count==failure{return fmt.Errorf("simulated power loss before durable checkpoint")}
     durable=v
     return saveRecovery(dir,v)
    })
    if err==nil&&count<failure{return} // All actual persistence boundaries covered.
    if err==nil{t.Fatal("checkpoint failure ignored")}
    j=durable
    if err:=applyRecovery(context.Background(),root,dir,p,files,&j,func(context.Context)error{return nil},func(v recoveryJournal)error{return saveRecovery(dir,v)});err!=nil{t.Fatal(err)}
    if j.State!="complete"{t.Fatal(j.State)}
   })
  }
 }
}

func TestFirstInstallationEveryCheckpointResumes(t *testing.T) {
 for failure:=1;failure<=10;failure++ {
  t.Run(fmt.Sprint(failure),func(t *testing.T){
   p:=agentInstallFixture(t);files:=agentPayloads(p,[]byte("verified agent"));root:=t.TempDir()
   j:=agentInstallJournal{Schema:1,PlanSHA256:p.SHA256};durable:=j;count:=0
   err:=installAgentFiles(context.Background(),root,files,false,&j,func(v agentInstallJournal)error{
    count++;if count==failure{return fmt.Errorf("power loss")}
    durable=v;durable.Completed=append([]string(nil),v.Completed...);return nil
   })
   if err==nil&&count<failure{return}
   if err==nil{t.Fatal("ignored checkpoint failure")}
   j=durable
   if err:=installAgentFiles(context.Background(),root,files,j.Started,&j,func(agentInstallJournal)error{return nil});err!=nil{t.Fatal(err)}
   if j.State!="installed-awaiting-enrollment"||len(j.Completed)!=len(files){t.Fatal("incomplete install")}
  })
 }
}
