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

 "github.com/FyberLabs/hypermesh-host/internal/hardware"
)

func fixtureGuided(t *testing.T,answers string)(guidedFlow,*[]string,*string) {
 t.Helper()
 dir:=t.TempDir();if err:=os.Chmod(dir,0700);err!=nil{t.Fatal(err)}
 calls:=[]string{};failure:=""
 state:=&guidedState{Network:"test",Host:"fixture",Directory:"/models",Mode:"uma",Allocation:40,Class:"agx-large"}
 sha:=strings.Repeat("a",64)
 g:=guidedFlow{dir:dir,state:state,ui:guidedUI{bufio.NewReader(strings.NewReader(answers)),io.Discard},diagnostic:io.Discard,
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
 if mode!="uma"||budget!=40||class!="agx-large"{t.Fatal(mode,budget,class)}
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
