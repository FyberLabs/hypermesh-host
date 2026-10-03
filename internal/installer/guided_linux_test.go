//go:build linux

package installer

import (
 "bufio"
 "context"
 "encoding/json"
 "io"
 "os"
 "path/filepath"
 "reflect"
 "strings"
 "testing"
 "time"
 "fmt"
 "github.com/FyberLabs/hypermesh-host/internal/buildinfo"

 "github.com/FyberLabs/hypermesh-host/internal/hardware"
)

func fixtureGuided(t *testing.T,answers string)(guidedFlow,*[]string,*string) {
 t.Helper()
 dir:=t.TempDir();if err:=os.Chmod(dir,0700);err!=nil{t.Fatal(err)}
 calls:=[]string{};failure:=""
 state:=&guidedState{Network:"test",Host:"fixture",Directory:"/models",Mode:"uma",Allocation:40,Class:"agx-large"}
 sha:=strings.Repeat("a",64)
 g:=guidedFlow{dir:dir,state:state,ui:guidedUI{in:bufio.NewReader(strings.NewReader(answers)),out:io.Discard},diagnostic:io.Discard,
 save:func()error{return nil},rebootNeeded:func(SetupPlan)(bool,error){return false,nil},
 boot:func()(string,string,error){return "machine","boot",nil}}
 g.run=func(name string)guidedRunner{return func(_ context.Context,args []string,out,diagnostic io.Writer)int {
  calls=append(calls,name)
  if name==failure{return 1}
  var value any=map[string]any{}
  switch name {
  case "plan-setup":value=SetupPlan{PlanSHA256:sha}
  case "plan-agent":value=agentInstallPlan{SHA256:sha}
  case "plan-certification":value=certificationPlan{SHA256:sha}
  case "plan-reboot":value=setupRebootPlan{SHA256:sha,BootID:"boot"}
  }
  if err:=json.NewEncoder(out).Encode(value);err!=nil{t.Fatal(err)}
  return 0
 }}
 return g,&calls,&failure
}
func TestGuidedCompleteAndRerun(t *testing.T) {
 g,calls,_:=fixtureGuided(t,"y\ny\ny\ny\n")
 if err:=g.finish(context.Background());err!=nil{t.Fatal(err)}
 want:=[]string{"plan-setup","apply-setup","plan-agent","install-agent","register-agent","check-agent","plan-certification","certify-agent","activate-agent","host-status"}
 if !reflect.DeepEqual(*calls,want)||g.state.Stage!=7{t.Fatalf("calls=%v stage=%d",*calls,g.state.Stage)}
 *calls=nil
 if err:=g.finish(context.Background());err!=nil{t.Fatal(err)}
 if !reflect.DeepEqual(*calls,[]string{"host-status"}){t.Fatalf("repeated mutations: %v",*calls)}
}
func TestGuidedDeclineDoesNotApply(t *testing.T) {
 for _,answer:=range []string{"no\n","\n",""}{
  g,calls,_:=fixtureGuided(t,answer)
  if err:=g.finish(context.Background());err==nil{t.Fatal("decline must pause")}
  if !reflect.DeepEqual(*calls,[]string{"plan-setup"})||g.state.Stage!=0{t.Fatal(*calls)}
 }
}
func TestGuidedInterruptedInstallReusesPlan(t *testing.T) {
 g,calls,failure:=fixtureGuided(t,"y\ny\n")
 *failure="install-agent"
 if err:=g.finish(context.Background());err==nil||g.state.Stage!=2{t.Fatalf("stage=%d err=%v",g.state.Stage,err)}
 raw,err:=os.ReadFile(filepath.Join(g.dir,"agent.json"));if err!=nil{t.Fatal(err)}
 *failure="";*calls=nil;g.ui.in=bufio.NewReader(strings.NewReader("y\ny\ny\n"))
 if err=g.finish(context.Background());err!=nil{t.Fatal(err)}
 if (*calls)[0]!="install-agent"{t.Fatal("did not resume existing installation",*calls)}
 after,_:=os.ReadFile(filepath.Join(g.dir,"agent.json"));if string(raw)!=string(after){t.Fatal("replaced approved plan")}
}
func TestGuidedRebootContinuesAfterNewBoot(t *testing.T) {
 g,calls,_:=fixtureGuided(t,"y\ny\n")
 g.rebootNeeded=func(SetupPlan)(bool,error){return true,nil}
 if err:=g.finish(context.Background());err!=nil{t.Fatal(err)}
 if g.state.Stage!=1||(*calls)[len(*calls)-1]!="reboot-setup"{t.Fatal(*calls)}
 *calls=nil;g.boot=func()(string,string,error){return "machine","new-boot",nil}
 g.ui.in=bufio.NewReader(strings.NewReader("y\ny\ny\n"))
 if err:=g.finish(context.Background());err!=nil{t.Fatal(err)}
 if (*calls)[0]!="resume-setup"||g.state.Stage!=7{t.Fatal(*calls)}
}
func TestGuidedSavedPlanRejectsSymlink(t *testing.T){
 g,_,_:=fixtureGuided(t,"")
 if err:=os.Symlink("/etc/passwd",filepath.Join(g.dir,"setup.json"));err!=nil{t.Fatal(err)}
 if _,err:=g.plan(context.Background(),"plan-setup","setup.json");err==nil{t.Fatal("accepted symlink")}
}
func TestGuidedDefaultsDoNotChangeSoftwareChannel(t *testing.T){
 production,err:=installationNetwork("production");if err!=nil{t.Fatal(err)}
 test,_:=installationNetwork("test")
 if production.Plane==test.Plane{t.Fatal("networks collapsed")}
 if _,err=installationNetwork("canary");err==nil{t.Fatal("unknown network")}
 profile:=hardware.Profile{ModelBudgetBytes:40*GiB,Facts:hardware.Facts{Tegra:true,Model:"NVIDIA Jetson AGX Orin"}}
 mode,budget,class:=guidedDefaults(profile)
 if mode!="uma"||budget!=0||class!="agx-large"{t.Fatal(mode,budget,class)}
}

func TestGuidedPlansPreserveIntegerPrecision(t *testing.T){
 g,_,_:=fixtureGuided(t,"")
 g.run=func(string)guidedRunner{return func(_ context.Context,_ []string,out,diagnostic io.Writer)int{
  io.WriteString(out,`{"plan_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","build":9007199254740993}`)
  return 0
 }}
 if _,err:=g.plan(context.Background(),"plan-agent","precise.json");err!=nil{t.Fatal(err)}
 raw,err:=os.ReadFile(filepath.Join(g.dir,"precise.json"));if err!=nil{t.Fatal(err)}
 if !strings.Contains(string(raw),"9007199254740993"){t.Fatal("integer rounded",string(raw))}
}

func TestGuidedPromptCancellation(t *testing.T){
 reader,writer:=io.Pipe();defer reader.Close();defer writer.Close()
 ctx,cancel:=context.WithCancel(context.Background());cancel()
 ui:=guidedUI{in:bufio.NewReader(reader),out:io.Discard,ctx:ctx}
 if _,err:=ui.ask("Continue?","");err==nil{t.Fatal("cancelled prompt accepted")}
}

func TestGuidedRebootRetryValidatesCheckpointAndRequiresNewApproval(t *testing.T){
 p,_:=rebootFixture(t)
 for _,kind:=range []string{"missing","requested","decline","corrupt","other-plan","symlink"}{
  t.Run(kind,func(t *testing.T){
   dir:=t.TempDir();path:=filepath.Join(dir,"reboot.json")
   j:=setupRebootJournal{Schema:1,PlanSHA256:p.SHA256,SetupPlanSHA256:p.Setup.PlanSHA256,MachineSHA256:p.MachineSHA256,BootID:p.BootID,State:"requested",RecordedAt:time.Now(),FailureCode:"request_unconfirmed"}
   if kind=="other-plan"{j.MachineSHA256=strings.Repeat("b",64)}
   if kind=="symlink"{if err:=os.Symlink("/etc/passwd",path);err!=nil{t.Fatal(err)}
   }else if kind!="missing"{
    raw,_:=json.Marshal(j);if kind=="corrupt"{raw=[]byte("{}")}
    if err:=os.WriteFile(path,raw,0600);err!=nil{t.Fatal(err)}
   }
   args,prompt,err:=guidedRebootRetry(path,p)
   if kind=="corrupt"||kind=="other-plan"||kind=="symlink"{if err==nil{t.Fatal("invalid checkpoint accepted")};return}
   if err!=nil{t.Fatal(err)}
   if (len(args)==2)!=(kind!="missing"){t.Fatal(args)}
   answer:="y\n";if kind=="decline"{answer="n\n"}
   g,calls,_:=fixtureGuided(t,answer)
   if err:=writeGuided(filepath.Join(g.dir,"reboot.json"),p);err!=nil{t.Fatal(err)}
   err=g.apply(context.Background(),"reboot-setup","reboot.json",prompt,args...)
   if kind=="decline"{if err==nil||len(*calls)!=0{t.Fatal("reboot without renewed approval")}}else if err!=nil||!reflect.DeepEqual(*calls,[]string{"reboot-setup"}){t.Fatal(err,*calls)}
  })
 }
}
func TestGuidedNewBuildRecoversOnlyBeforeInstallation(t *testing.T){
 oldBuild,oldCommit:=buildinfo.Build,buildinfo.Commit
 buildinfo.Build="102";buildinfo.Commit=strings.Repeat("c",40)
 defer func(){buildinfo.Build=oldBuild;buildinfo.Commit=oldCommit}()
 for _,kind:=range []string{"recover","decline","installed","guard","downgrade","wrong-channel","bad-receipt"}{
  t.Run(kind,func(t *testing.T){
   g,_,_:=fixtureGuided(t,"y\n")
   g.state.Stage=1;g.state.Download=guidedDownload{Build:101,Channel:"test"}
   key:=filepath.Join(g.dir,"download.pub");if err:=os.WriteFile(key,[]byte("public key fixture"),0600);err!=nil{t.Fatal(err)}
   download:=guidedDownload{Schema:1,ReleaseURL:"https://example.test/test/102-cccccccccccc",Channel:"test",Build:102,Commit:buildinfo.Commit,TrustedKey:key}
   if kind=="downgrade"{g.state.Download.Build=103}
   if kind=="wrong-channel"{download.Channel="prod"}
   if kind=="bad-receipt"{download.Commit=strings.Repeat("d",40)}
   if kind=="installed"{g.state.Stage=2}
   if kind=="decline"{g.ui.in=bufio.NewReader(strings.NewReader("n\n"))}
   receipt:=filepath.Join(g.dir,"receipt.json");if err:=writeGuided(receipt,download);err!=nil{t.Fatal(err)}
   statePath:=filepath.Join(g.dir,"session.json");if err:=writeGuided(statePath,g.state);err!=nil{t.Fatal(err)}
   before,_:=os.ReadFile(statePath)
   oldPlan:=filepath.Join(g.dir,"setup.json");if err:=os.WriteFile(oldPlan,[]byte("old approval"),0600);err!=nil{t.Fatal(err)}
   guard:=func()error{if kind=="guard"{return fmt.Errorf("existing service")};return nil}
   err:=upgradeGuidedPrerequisites(context.Background(),g.dir,statePath,receipt,g.state,g.ui,guard)
   after,_:=os.ReadFile(statePath)
   if kind!="recover"{if err==nil||string(before)!=string(after){t.Fatal("invalid upgrade changed state",err)};return}
   if err!=nil||g.state.Stage!=0{t.Fatal(err)}
   planDir,err:=guidedPlanDirectory(g.dir,*g.state);if err!=nil||planDir==g.dir{t.Fatal("reused old approvals",err)}
   if _,err=os.Stat(filepath.Join(planDir,"setup.json"));!os.IsNotExist(err){t.Fatal("old approval copied into new build")}
   old,_:=os.ReadFile(oldPlan);if string(old)!="old approval"{t.Fatal("old approval lost")}
   // Reopening the session after an interruption selects the new generation.
   var saved guidedState
   if err:=guidedRead(statePath,&saved,true);err!=nil||saved.Download.Build!=102||saved.PlanDirectory!=g.state.PlanDirectory{t.Fatal("upgrade not durable",err)}
  })
 }
}

func TestGuidedSetupDoesNotPassLegacyMemoryBudget(t *testing.T) {
 g,_,_:=fixtureGuided(t,"y\ny\ny\ny\n")
 original:=g.run;checked:=false
 g.run=func(name string)guidedRunner{
  run:=original(name)
  return func(ctx context.Context,args []string,out,diagnostic io.Writer)int{
   if name=="plan-setup" {
    checked=true
    for _,arg:=range args {if arg=="--uma-gib"{t.Fatal("legacy budget carried into installation")}}
   }
   return run(ctx,args,out,diagnostic)
  }
 }
 if err:=g.finish(context.Background());err!=nil{t.Fatal(err)}
 if !checked{t.Fatal("setup not checked")}
}
