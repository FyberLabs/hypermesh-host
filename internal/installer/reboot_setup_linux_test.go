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

func rebootFixture(t *testing.T)(setupRebootPlan,*applyFixture){
 t.Helper()
 f:=newApplyFixture(t,false,true)
 if err:=f.apply();err!=nil{t.Fatal(err)}
 boot,err:=hostregistration.NewRunID();if err!=nil{t.Fatal(err)}
 p:=setupRebootPlan{Schema:1,Scope:"pre-enrollment",Setup:f.plan,MachineSHA256:strings.Repeat("a",64),BootID:boot,Headless:true}
 p.SHA256=setupRebootDigest(p)
 if err=validateSetupRebootPlan(p,p.SHA256);err!=nil{t.Fatal(err)}
 return p,f
}
func rebootDepsFixture(p setupRebootPlan)setupRebootDeps{
 return setupRebootDeps{
  Identity:func()(string,string,error){return p.MachineSHA256,p.BootID,nil},
  Check:func(context.Context,bool)error{return nil},
  Save:func(setupRebootJournal)error{return nil},
  Request:func(context.Context)error{return nil},
  Now:time.Now,
 }
}
func TestRebootRequiresDurableIntentAndExplicitRetry(t *testing.T){
 p,_:=rebootFixture(t);d:=rebootDepsFixture(p)
 var j,durable setupRebootJournal
 calls,checks:=0,0
 d.Save=func(v setupRebootJournal)error{if !validSetupRebootJournal(v){t.Fatal("invalid journal")};durable=v;return nil}
 d.Check=func(context.Context,bool)error{checks++;return nil}
 d.Request=func(context.Context)error{
  calls++
  if durable.State!="requested"||durable.BootID!=p.BootID||checks!=calls*2{t.Fatal("request before durable checkpoint and checks")}
  return nil
 }
 if err:=requestSetupReboot(context.Background(),p,&j,false,d);err!=nil{t.Fatal(err)}
 if calls!=1||j.State!="requested"{t.Fatal("request not recorded")}
 if requestSetupReboot(context.Background(),p,&j,false,d)==nil||calls!=1{t.Fatal("automatic repeated reboot")}
 if err:=requestSetupReboot(context.Background(),p,&j,true,d);err!=nil||calls!=2{t.Fatal(err)}
 newBoot,_:=hostregistration.NewRunID()
 d.Identity=func()(string,string,error){return p.MachineSHA256,newBoot,nil}
 if requestSetupReboot(context.Background(),p,&j,true,d)==nil||calls!=2{t.Fatal("old approval rebooted new boot")}
}
func TestRebootRequestFailuresNeverClaimCompletion(t *testing.T){
 for _,kind:=range []string{"approval","machine","identity","preflight","second-check","save","request","cancel","cancel-after-save","boot-race","save-failure"}{
  t.Run(kind,func(t *testing.T){
   p,_:=rebootFixture(t);d:=rebootDepsFixture(p);var j setupRebootJournal
   requests,checks,saves,identities:=0,0,0,0
   ctx,cancel:=context.WithCancel(context.Background());defer cancel()
   d.Identity=func()(string,string,error){
    identities++
    if kind=="identity"{return "","",fmt.Errorf("private")}
    if kind=="machine"{return strings.Repeat("b",64),p.BootID,nil}
    if kind=="boot-race"&&identities>1{b,_:=hostregistration.NewRunID();return p.MachineSHA256,b,nil}
    return p.MachineSHA256,p.BootID,nil
   }
   d.Check=func(context.Context,bool)error{checks++;if kind=="preflight"||(kind=="second-check"&&checks==2){return fmt.Errorf("private")};return nil}
   d.Save=func(v setupRebootJournal)error{saves++;if kind=="save"||(kind=="save-failure"&&saves>1){return fmt.Errorf("disk")};if kind=="cancel-after-save"{cancel()};return nil}
   d.Request=func(context.Context)error{requests++;if kind=="request"||kind=="save-failure"{return fmt.Errorf("lost reply")};return nil}
   if kind=="approval"{p.Scope="active-host"}
   if kind=="cancel"{cancel()}
   if requestSetupReboot(ctx,p,&j,false,d)==nil||j.State=="verified"{t.Fatal("failure accepted")}
   want:=0;if kind=="request"||kind=="save-failure"{want=1}
   if requests!=want{t.Fatalf("unexpected reboot request: %d",requests)}
  })
 }
}
func TestResumeRechecksNewBootAndPersistsOnlyVerifiedSuccess(t *testing.T){
 for _,kind:=range []string{"pass","same-boot","machine","checkpoint","check","save-intent","save-final","cancel","boot-race"}{
  t.Run(kind,func(t *testing.T){
   p,_:=rebootFixture(t);d:=rebootDepsFixture(p);boot,_:=hostregistration.NewRunID()
   j:=setupRebootJournal{Schema:1,PlanSHA256:p.SHA256,BootID:p.BootID,State:"requested",RecordedAt:time.Now()}
   durable:=j;checks,identities:=0,0
   d.Identity=func()(string,string,error){
    identities++
    if kind=="same-boot"{return p.MachineSHA256,p.BootID,nil}
    if kind=="machine"{return strings.Repeat("b",64),boot,nil}
    if kind=="boot-race"&&identities>1{other,_:=hostregistration.NewRunID();return p.MachineSHA256,other,nil}
    return p.MachineSHA256,boot,nil
   }
   d.Check=func(_ context.Context,post bool)error{checks++;if !post||durable.State!="verifying"{t.Fatal("missing postflight intent")};if kind=="check"{return fmt.Errorf("kernel changed")};return nil}
   d.Save=func(v setupRebootJournal)error{
    if kind=="save-intent"||(kind=="save-final"&&v.State=="verified"){return fmt.Errorf("disk")}
    if !validSetupRebootJournal(v){t.Fatal("invalid checkpoint")}
    durable=v;return nil
   }
   d.Request=func(context.Context)error{t.Fatal("resume submitted reboot");return nil}
   if kind=="checkpoint"{j.PlanSHA256=strings.Repeat("e",64)}
   ctx,cancel:=context.WithCancel(context.Background());defer cancel();if kind=="cancel"{cancel()}
   err:=resumeSetupReboot(ctx,p,&j,d)
   if (err==nil)!=(kind=="pass"){t.Fatalf("%s: %v",kind,err)}
   if kind=="pass"{
    if j.State!="verified"||j.ObservedBootID!=boot{t.Fatal("missing new boot")}
    if err=resumeSetupReboot(ctx,p,&j,d);err!=nil||checks!=2{t.Fatal("resume skipped fresh checks",err)}
   }else if durable.State=="verified"{t.Fatal("failed check recorded success")}
   if kind=="save-final"&&durable.State!="verifying"{t.Fatal("lost uncertain outcome")}
  })
 }
}
func rebootSource(f *applyFixture)setupSource{
 source:=f.deps.Source;original:=source.Output
 source.Output=func(ctx context.Context,name string,args ...string)([]byte,error){
  joined:=strings.Join(args," ")
  if name=="systemd-analyze"{return []byte("/etc/systemd/system\n/run/systemd/system\n/usr/lib/systemd/system"),nil}
  if name=="systemctl"&&strings.Contains(joined,"UnitPath"){return []byte("/etc/systemd/system /run/systemd/system /usr/lib/systemd/system"),nil}
  if name=="systemctl"&&strings.Contains(joined,"hypermesh-"){return []byte("LoadState=not-found\nActiveState=inactive\nMainPID=0"),nil}
  if name=="systemctl"&&strings.Contains(joined,"graphical.target"){return []byte("inactive"),nil}
  return original(ctx,name,args...)
 }
 return source
}
func rebootHostFixture(t *testing.T)(setupRebootPlan,*applyFixture,string,string,setupSource){
 t.Helper()
 p,f:=rebootFixture(t);root:=t.TempDir();state:=agentDest(root,defaultSetupState)
 if err:=secureSetupDirectory(state,true);err!=nil{t.Fatal(err)}
 if err:=writeSetupJournal(filepath.Join(state,p.Setup.PlanSHA256+".json"),f.journal);err!=nil{t.Fatal(err)}
 return p,f,root,state,rebootSource(f)
}
func TestRebootGuardRejectsInstalledEnrolledAndLoadedHosts(t *testing.T){
 for _,kind:=range []string{"clean","device","lease","marker","keydir","binary","env","journal","unit","dropin","loaded","running","probe-error","duplicate","symlink"}{
  t.Run(kind,func(t *testing.T){
   _,_,root,state,source:=rebootHostFixture(t)
   paths:=map[string]string{"device":"/var/lib/hypermesh/device.json","lease":"/var/lib/hypermesh/lease.json",
    "marker":"/var/lib/hypermesh/activation-approved","keydir":"/var/lib/hypermesh/registration-keys",
    "binary":"/usr/local/bin/hypermesh-agent","env":"/etc/hypermesh/agent.env",
    "unit":"/usr/lib/systemd/system/hypermesh-agent.service","dropin":"/usr/lib/systemd/system/hypermesh-agent.service.d/override.conf"}
   if path:=paths[kind];path!=""{
    full:=agentDest(root,path);if err:=os.MkdirAll(filepath.Dir(full),0700);err!=nil{t.Fatal(err)}
    if err:=os.WriteFile(full,[]byte("private"),0600);err!=nil{t.Fatal(err)}
   }
   if kind=="journal"{if err:=os.WriteFile(filepath.Join(state,"registration-old.json"),[]byte("private"),0600);err!=nil{t.Fatal(err)}}
   if kind=="symlink"{if err:=os.Symlink("/private",agentDest(root,"/var/lib/hypermesh/device.json"));err!=nil{t.Fatal(err)}}
   base:=source.Output
   source.Output=func(ctx context.Context,name string,args ...string)([]byte,error){
    raw,err:=base(ctx,name,args...)
    if strings.Contains(strings.Join(args," "),"hypermesh-"){
     switch kind{
     case "loaded":raw=bytes.ReplaceAll(raw,[]byte("not-found"),[]byte("loaded"))
     case "running":raw=bytes.ReplaceAll(raw,[]byte("inactive"),[]byte("active"))
     case "probe-error":err=fmt.Errorf("private")
     case "duplicate":raw=append(raw,[]byte("\nMainPID=0")...)
     }
    }
    return raw,err
   }
   err:=guardRebootHost(context.Background(),root,state,source)
   if (err==nil)!=(kind=="clean"){t.Fatalf("%s: %v",kind,err)}
  })
 }
}
func TestRebootPostflightRequiresFreshPrerequisites(t *testing.T){
 for _,kind:=range []string{"pass","incomplete","kernel","package","headless","reboot-required","resource"}{
  t.Run(kind,func(t *testing.T){
   p,f,root,state,source:=rebootHostFixture(t)
   preflight:=f.deps.Preflight
   switch kind{
   case "incomplete":j:=f.journal;j.Stage="headless";if err:=writeSetupJournal(filepath.Join(state,p.Setup.PlanSHA256+".json"),j);err!=nil{t.Fatal(err)}
   case "kernel":
    base:=source.ReadFile;source.ReadFile=func(path string)([]byte,error){if path=="/proc/sys/kernel/osrelease"{return []byte("new kernel"),nil};return base(path)}
   case "package":f.installed["unexpected"]="1.0"
   case "headless":
    base:=source.Output;source.Output=func(ctx context.Context,name string,args ...string)([]byte,error){if strings.Contains(strings.Join(args," "),"graphical.target"){return []byte("active"),nil};return base(ctx,name,args...)}
   case "reboot-required":
    path:=agentDest(root,"/run/reboot-required");if err:=os.MkdirAll(filepath.Dir(path),0755);err!=nil{t.Fatal(err)};if err:=os.WriteFile(path,nil,0644);err!=nil{t.Fatal(err)}
   case "resource":preflight=func(context.Context,SetupPlan)(Plan,error){return Plan{},fmt.Errorf("no memory")}
   }
   err:=checkSetupReboot(context.Background(),p,root,state,source,preflight,true)
   if (err==nil)!=(kind=="pass"){t.Fatalf("%s: %v",kind,err)}
  })
 }
}
func TestRebootPrivateJournalStatusAndCLI(t *testing.T){
 p,_:=rebootFixture(t);dir:=t.TempDir();if err:=os.Chmod(dir,0700);err!=nil{t.Fatal(err)}
 unlock,err:=lockSetupState(dir);if err!=nil{t.Fatal(err)};unlock()
 path:=filepath.Join(dir,"plan.json");raw,_:=json.Marshal(p)
 if err=os.WriteFile(path,raw,0600);err!=nil{t.Fatal(err)}
 d:=rebootDepsFixture(p);requests:=0;d.Request=func(context.Context)error{requests++;return nil}
 var out,diagnostic bytes.Buffer
 args:=[]string{"--plan",path,"--approve-plan",p.SHA256,"--reboot"}
 if runRebootSetup(context.Background(),args[:4],&out,&diagnostic,false,dir,true,&d)!=2||requests!=0{t.Fatal("missing explicit reboot accepted")}
 if code:=runRebootSetup(context.Background(),args,&out,&diagnostic,false,dir,true,&d);code!=0||requests!=1{t.Fatalf("%d %s",code,diagnostic.String())}
 out.Reset()
 if runRebootSetup(context.Background(),args,&out,&diagnostic,false,dir,true,&d)!=1||requests!=1{t.Fatal("repeated reboot")}
 if !bytes.Contains(out.Bytes(),[]byte(`"attempt_succeeded":false`)){t.Fatal("failed attempt not explicit")}
 if runRebootSetup(context.Background(),args[:4],&out,&diagnostic,true,dir,true,&d)!=1{t.Fatal("same boot resumed")}
 next,_:=hostregistration.NewRunID();d.Identity=func()(string,string,error){return p.MachineSHA256,next,nil}
 if code:=runRebootSetup(context.Background(),args[:4],&out,&diagnostic,true,dir,true,&d);code!=0{t.Fatalf("%d %s",code,diagnostic.String())}
 if requests!=1{t.Fatal("resume rebooted")}
 bundle,err:=collectSupportWithReboot(context.Background(),dir,"","","","",p.SHA256);if err!=nil{t.Fatal(err)}
 if !bundle.CollectionComplete||bundle.Stages[7].State!="verified"{t.Fatal("missing reboot evidence")}
 exported,_:=json.Marshal(bundle)
 for _,private:=range []string{p.BootID,next,p.MachineSHA256,p.Setup.ReadinessURL}{if bytes.Contains(exported,[]byte(private)){t.Fatal("private identity exported")}}
 info,err:=os.Stat(setupRebootPath(dir,p.SHA256));if err!=nil||!privateSetupFile(info){t.Fatal("unsafe checkpoint")}
 saved,_:=os.ReadFile(setupRebootPath(dir,p.SHA256))
 if err=os.Chmod(setupRebootPath(dir,p.SHA256),0644);err!=nil{t.Fatal(err)}
 if saveSetupReboot(setupRebootPath(dir,p.SHA256),setupRebootJournal{})==nil{t.Fatal("invalid checkpoint accepted")}
 if runRebootSetup(context.Background(),args[:4],&out,&diagnostic,true,dir,true,&d)!=1{t.Fatal("unsafe checkpoint accepted")}
 after,_:=os.ReadFile(setupRebootPath(dir,p.SHA256));if !bytes.Equal(saved,after){t.Fatal("unsafe checkpoint changed")}
}
func TestRebootPlanAndJournalRejectInconsistentAuthority(t *testing.T){
 p,_:=rebootFixture(t)
 for _,kind:=range []string{"digest","scope","reason","machine","boot","setup","headless"}{
  changed:=p
  switch kind{
  case "digest":changed.SHA256=strings.Repeat("e",64)
  case "scope":changed.Scope="enrolled"
  case "reason":changed.OSRequired=false;changed.Headless=false
  case "machine":changed.MachineSHA256="private"
  case "boot":changed.BootID="private"
  case "setup":changed.Setup.InstallAuthorized=true
  case "headless":changed.Setup.HeadlessRequested=false;changed.Setup.PlanSHA256=setupPlanDigest(changed.Setup)
  }
  if kind!="digest"{changed.SHA256=setupRebootDigest(changed)}
  if validateSetupRebootPlan(changed,changed.SHA256)==nil{t.Fatal("invalid plan accepted",kind)}
 }
 next,_:=hostregistration.NewRunID()
 j:=setupRebootJournal{Schema:1,PlanSHA256:p.SHA256,BootID:p.BootID,ObservedBootID:next,State:"verified",RecordedAt:time.Now()}
 for _,kind:=range []string{"same-boot","missing-boot","requested-with-observation","unknown-state","error","zero-time"}{
  v:=j
  switch kind{
  case "same-boot":v.ObservedBootID=v.BootID
  case "missing-boot":v.ObservedBootID=""
  case "requested-with-observation":v.State="requested"
  case "unknown-state":v.State="private"
  case "error":v.FailureCode="private"
  case "zero-time":v.RecordedAt=time.Time{}
  }
  if validSetupRebootJournal(v){t.Fatal("invalid completion accepted",kind)}
 }
}
func TestRebootCommandsNeverInheritOverridesOrBypassInhibitors(t *testing.T){
 for _,key:=range []string{"PATH","LD_PRELOAD","SYSTEMD_HOST","DBUS_SYSTEM_BUS_ADDRESS"}{t.Setenv(key,"PRIVATE_OVERRIDE")}
 cmd,err:=setupRebootCommand(context.Background());if err!=nil{t.Fatal(err)}
 want:=[]string{"/usr/bin/systemctl","--system","--no-ask-password","--check-inhibitors=yes","reboot"}
 if !sameSetupJSON(cmd.Args,want)||strings.Contains(strings.Join(cmd.Environ(),"\n"),"PRIVATE_OVERRIDE"){t.Fatal("unsafe reboot command")}
 inventory:=rebootInventoryCommand(context.Background(),"-W")
 if inventory.Path!="/usr/bin/dpkg-query"||strings.Contains(strings.Join(inventory.Environ(),"\n"),"PRIVATE_OVERRIDE"){t.Fatal("unsafe inventory")}
}

func TestRebootIdentityBindsMachineAndBoot(t *testing.T){
 root:=t.TempDir()
 machine:=agentDest(root,"/etc/machine-id");bootPath:=agentDest(root,"/proc/sys/kernel/random/boot_id")
 for _,path:=range []string{machine,bootPath}{if err:=os.MkdirAll(filepath.Dir(path),0755);err!=nil{t.Fatal(err)}}
 boot,_:=hostregistration.NewRunID()
 if err:=os.WriteFile(machine,[]byte(strings.Repeat("a",32)+"\n"),0644);err!=nil{t.Fatal(err)}
 if err:=os.WriteFile(bootPath,[]byte(boot+"\n"),0444);err!=nil{t.Fatal(err)}
 hash,got,err:=rebootIdentity(root)
 if err!=nil||got!=boot||hash!=digestSetupBytes([]byte(strings.Repeat("a",32))){t.Fatal("identity mismatch",err)}
 if err=os.WriteFile(machine,[]byte(strings.Repeat("0",32)),0644);err!=nil{t.Fatal(err)}
 if _,_,err=rebootIdentity(root);err==nil{t.Fatal("uninitialized machine accepted")}
 if err=os.Remove(machine);err!=nil{t.Fatal(err)}
 if err=os.Symlink(bootPath,machine);err!=nil{t.Fatal(err)}
 if _,_,err=rebootIdentity(root);err==nil{t.Fatal("linked identity accepted")}
}
func TestRebootSupportRefusesIncompleteAndUnsafeCheckpoints(t *testing.T){
 for _,kind:=range []string{"missing","corrupt","same-boot","unknown-code","wrong-plan","symlink"}{
  t.Run(kind,func(t *testing.T){
   p,_:=rebootFixture(t);dir:=t.TempDir();if err:=os.Chmod(dir,0700);err!=nil{t.Fatal(err)}
   unlock,err:=lockSetupState(dir);if err!=nil{t.Fatal(err)};unlock()
   boot,_:=hostregistration.NewRunID()
   j:=setupRebootJournal{Schema:1,PlanSHA256:p.SHA256,BootID:p.BootID,ObservedBootID:boot,State:"verified",RecordedAt:time.Now()}
   switch kind{case "same-boot":j.ObservedBootID=j.BootID;case "unknown-code":j.FailureCode="PRIVATE_CODE";case "wrong-plan":j.PlanSHA256=strings.Repeat("f",64)}
   path:=setupRebootPath(dir,p.SHA256)
   raw,_:=json.Marshal(j);if kind=="corrupt"{raw=[]byte("PRIVATE_CORRUPT")}
   if kind!="missing"&&kind!="symlink"{if err=os.WriteFile(path,raw,0600);err!=nil{t.Fatal(err)}}
   if kind=="symlink"{if err=os.Symlink(filepath.Join(dir,"absent"),path);err!=nil{t.Fatal(err)}}
   bundle,err:=collectSupportWithReboot(context.Background(),dir,"","","","",p.SHA256);if err!=nil{t.Fatal(err)}
   if bundle.CollectionComplete||bundle.Stages[7].Collection=="collected"{t.Fatal("invalid checkpoint accepted")}
   raw,_=json.Marshal(bundle)
   if bytes.Contains(raw,[]byte("PRIVATE_")){t.Fatal("private checkpoint leaked")}
  })
 }
}

func TestHeadlessRebootReasonDoesNotRepeatCompletedTransition(t *testing.T){
 p,f:=rebootFixture(t)
 source:=rebootSource(f)
 if needed,err:=headlessRebootNeeded(context.Background(),p.Setup,source);err!=nil||needed{t.Fatal("already headless boot requested another reboot")}
 base:=source.Output
 source.Output=func(ctx context.Context,name string,args ...string)([]byte,error){
  if strings.Contains(strings.Join(args," "),"graphical.target"){return []byte("active"),nil}
  return base(ctx,name,args...)
 }
 if needed,err:=headlessRebootNeeded(context.Background(),p.Setup,source);err!=nil||!needed{t.Fatal("missed pending headless transition")}
 source.Output=func(context.Context,string,...string)([]byte,error){return []byte("activating"),nil}
 if _,err:=headlessRebootNeeded(context.Background(),p.Setup,source);err==nil{t.Fatal("transient target accepted")}
}
