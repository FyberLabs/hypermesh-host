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

 "github.com/FyberLabs/hypermesh-host/internal/hardware"
 "github.com/FyberLabs/hypermesh-host/internal/hostregistration"
)
func registrationFixture(t *testing.T)(agentInstallPlan,registrationJournal) {
 t.Helper()
 p:=agentInstallFixture(t)
 fresh:=p.Setup.Preflight
 fresh.Hardware=hardware.Recommend(hardware.Facts{Arch:"arm64",Model:"NVIDIA Jetson AGX Orin Developer Kit",Processor:"ARM",GPU:"Tegra integrated GPU",MemoryBytes:64*GiB,AvailableBytes:50*GiB,Tegra:true,CUDA:true})
 fresh.Resources.Resources.SystemBytes=64*GiB;fresh.Resources.Admission.AcceleratorBytes=32*GiB
 j,err:=newRegistration(p,"https://auth.example/realms/hypermesh","https://test.hyperme.sh","agx-large",fresh)
 if err!=nil{t.Fatal(err)}
 return p,j
}
func TestRegistrationKeysBindingAndRedaction(t *testing.T) {
 p,j:=registrationFixture(t)
 if err:=validateRegistration(j,p,j.Issuer,j.Dashboard,j.Request.ClassID);err!=nil{t.Fatal(err)}
 _,second:=registrationFixture(t)
 if j.PrivateKey==second.PrivateKey||j.DeviceSecret==second.DeviceSecret||j.Request.RequestID==second.Request.RequestID{t.Fatal("reused registration identity")}
 for _,kind:=range []string{"plan","issuer","dashboard","class","secret","key","hash","prefix","device","state"} {
  changed:=j
  switch kind{case "plan":changed.PlanSHA256="other";case "issuer":changed.Issuer="https://other.example";case "dashboard":changed.Dashboard="https://other.example";case "class":changed.Request.ClassID="other";case "secret":changed.DeviceSecret=second.DeviceSecret;case "key":changed.PrivateKey=second.PrivateKey;case "hash":changed.Request.DeviceSecretHash=strings.Repeat("0",64);case "prefix":changed.Request.CredentialPrefix="different";case "device":changed.DeviceID=second.Request.RequestID;case "state":changed.State="selling"}
  if validateRegistration(changed,p,j.Issuer,j.Dashboard,j.Request.ClassID)==nil{t.Fatal("accepted changed",kind)}
 }
 raw,_:=json.Marshal(publicRegistration(j))
 for _,secret:=range []string{j.PrivateKey,j.DeviceSecret,j.Request.DeviceSecretHash,j.Request.WGPublicKey}{
  if strings.Contains(string(raw)+fmt.Sprintf("%v %#v",j,j),secret){t.Fatal("private identity leaked")}
 }
}
func TestRegistrationClassRequiresMeasuredMatch(t *testing.T){
 p:=Plan{Hardware:hardware.Recommend(hardware.Facts{Arch:"arm64",Model:"NVIDIA Jetson AGX Orin",Tegra:true,CUDA:true})}
 p.Resources.Resources.SystemBytes=64*GiB
 if err:=registrationClass("agx-large",p);err!=nil{t.Fatal(err)}
 for _,kind:=range []string{"memory","model","runtime","tegra","class"}{
  copy:=p;class:="agx-large"
  switch kind{case "memory":copy.Resources.Resources.SystemBytes=32*GiB;case "model":copy.Hardware.Facts.Model="AMD";case "runtime":copy.Hardware.Backend="cpu";case "tegra":copy.Hardware.Facts.Tegra=false;case "class":class="nx-volume"}
  if registrationClass(class,copy)==nil{t.Fatal("accepted",kind)}
 }
}
type fakeRegistrationRemote struct {
 states []string
 deviceID string
 creates,completes,gets int
 seen []hostregistration.Request
}
func (f *fakeRegistrationRemote) next(r hostregistration.Request)(hostregistration.Status,error){
 f.seen=append(f.seen,r)
 if len(f.states)==0{return hostregistration.Status{},fmt.Errorf("lost response")}
 state:=f.states[0];f.states=f.states[1:]
 s:=hostregistration.Status{State:state,ApprovalPath:"/dashboard/hypermesh/host/approve/"+r.RequestID}
 if state=="completed"||state=="retired"{s.DeviceID=&f.deviceID}
 return s,nil
}
func(f *fakeRegistrationRemote)Create(_ context.Context,r hostregistration.Request)(hostregistration.Status,error){f.creates++;return f.next(r)}
func(f *fakeRegistrationRemote)Get(_ context.Context,r hostregistration.Request)(hostregistration.Status,error){f.gets++;return f.next(r)}
func(f *fakeRegistrationRemote)Complete(_ context.Context,r hostregistration.Request)(hostregistration.Status,error){f.completes++;return f.next(r)}
func noRegistrationWait(context.Context,time.Duration)error{return nil}
func TestRegistrationApprovalAndLostCompletionRecovery(t *testing.T){
 _,j:=registrationFixture(t);j.Owner="verified-owner"
 original:=j
 remote:=&fakeRegistrationRemote{states:[]string{"pending","approved"},deviceID:"12345678-1234-4234-8234-123456789aaa"}
 var persisted registrationJournal
 save:=func(value registrationJournal)error{persisted=value;return nil}
 published:=0
 publish:=func(value registrationJournal)error{published++;if persisted.DeviceID!=value.DeviceID||persisted.State!="completed"{t.Fatal("publish before durable completion")};return nil}
 var display bytes.Buffer
 if finishRegistration(context.Background(),&j,remote,save,publish,&display,noRegistrationWait)==nil{t.Fatal("lost response ignored")}
 if published!=0||remote.completes!=1||persisted.State!="approved"{t.Fatal("published early")}
 // Reauthentication occurs outside the engine; retry the immutable request.
 j=persisted;remote.states=[]string{"completed"}
 if err:=finishRegistration(context.Background(),&j,remote,save,publish,&display,noRegistrationWait);err!=nil{t.Fatal(err)}
 if j.State!="registered-inactive"||published!=1||remote.completes!=1{t.Fatal("completion repeated or not published")}
 for _,request:=range remote.seen{if request!=original.Request{t.Fatal("retry changed request")}}
 if j.PrivateKey!=original.PrivateKey||j.DeviceSecret!=original.DeviceSecret{t.Fatal("retry changed secrets")}
 if !strings.Contains(display.String(),j.Dashboard+"/dashboard/hypermesh/host/approve/"){t.Fatal("approval URL missing")}
}
func TestRegistrationTerminalAndJournalFailures(t *testing.T){
 for _,state:=range []string{"denied","expired","retired","pending","completed"}{t.Run(state,func(t *testing.T){
  _,j:=registrationFixture(t);j.Owner="owner"
  remote:=&fakeRegistrationRemote{states:[]string{state},deviceID:"12345678-1234-4234-8234-123456789aaa"}
  published:=false
  save:=func(registrationJournal)error{if state=="completed"{return fmt.Errorf("disk full")};return nil}
  publish:=func(registrationJournal)error{published=true;return nil}
  wait:=func(context.Context,time.Duration)error{return fmt.Errorf("cancelled")}
  if finishRegistration(context.Background(),&j,remote,save,publish,&bytes.Buffer{},wait)==nil||published||remote.completes!=0{t.Fatal("unsafe publication")}
 })}
}
func TestRegistrationPrivatePersistenceAndIdentityRecovery(t *testing.T){
 p,j:=registrationFixture(t);j.Owner="owner";j.State="completed";j.DeviceID="12345678-1234-4234-8234-123456789aaa"
 root:=t.TempDir()
 state:=agentDest(root,defaultSetupState)
 if err:=secureSetupDirectory(state,true);err!=nil{t.Fatal(err)}
 path:=registrationPath(state,p.SHA256)
 if err:=saveRegistration(path,j);err!=nil{t.Fatal(err)}
 raw,err:=readSetupRegular(path,true);if err!=nil{t.Fatal(err)}
 var loaded registrationJournal
 if err=decodeSetupJSON(raw,&loaded);err!=nil{t.Fatal(err)}
 if err=validateRegistration(loaded,p,j.Issuer,j.Dashboard,j.Request.ClassID);err!=nil{t.Fatal(err)}
 // Simulate interruption after the first identity file is published.
 if err=secureSetupDirectory(agentDest(root,registrationKeyDir(j)),true);err!=nil{t.Fatal(err)}
 first:=registrationIdentityFiles(j)[0]
 if err=writeAgentFile(agentDest(root,first.Path),first);err!=nil{t.Fatal(err)}
 if err=publishRegistration(root,loaded);err!=nil{t.Fatal(err)}
 if err=publishRegistration(root,loaded);err!=nil{t.Fatal(err)}
 if err=checkRegistrationIdentity(root,loaded,true);err!=nil{t.Fatal(err)}
 if _,err=os.Stat(agentDest(root,"/var/lib/hypermesh/activation-approved"));!os.IsNotExist(err){t.Fatal("activated service")}
 device:=agentDest(root,"/var/lib/hypermesh/device.json")
 if err=os.Remove(device);err!=nil{t.Fatal(err)}
 if checkRegistrationIdentity(root,loaded,true)==nil{t.Fatal("missing completed identity silently repaired")}
 if err=os.WriteFile(device,[]byte("operator identity"),0o600);err!=nil{t.Fatal(err)}
 if publishRegistration(root,loaded)==nil{t.Fatal("overwrote unrelated identity")}
}
func TestRegistrationRefusesUnsafePrivateFiles(t *testing.T){
 p,j:=registrationFixture(t)
 for _,kind:=range []string{"mode","symlink","hardlink"}{t.Run(kind,func(t *testing.T){
  dir:=t.TempDir();if err:=os.Chmod(dir,0o700);err!=nil{t.Fatal(err)}
  path:=registrationPath(dir,p.SHA256)
  if err:=saveRegistration(path,j);err!=nil{t.Fatal(err)}
  switch kind{
  case "mode":os.Chmod(path,0o644)
  case "hardlink":os.Link(path,filepath.Join(dir,"copy"))
  case "symlink":os.Rename(path,filepath.Join(dir,"original"));os.Symlink(filepath.Join(dir,"original"),path)
  }
  if saveRegistration(path,j)==nil{t.Fatal("accepted unsafe journal")}
 })}
}
func TestRegistrationInstalledManifest(t *testing.T){
 p,_:=registrationFixture(t)
 root:=t.TempDir()
 state:=agentDest(root,defaultSetupState)
 if err:=secureSetupDirectory(state,true);err!=nil{t.Fatal(err)}
 files:=agentPayloads(p,[]byte("verified agent"))
 j:=agentInstallJournal{Schema:1,PlanSHA256:p.SHA256}
 save:=func(value agentInstallJournal)error{return saveAgentJournal(filepath.Join(state,"agent-"+p.SHA256+".json"),value)}
 if err:=installAgentFiles(context.Background(),root,files,false,&j,save);err!=nil{t.Fatal(err)}
 if err:=verifyRegistrationInstallation(context.Background(),root,p);err!=nil{t.Fatal(err)}
 binary:=agentDest(root,"/usr/local/bin/hypermesh-agent")
 if err:=os.WriteFile(binary,[]byte("changed executable"),0o755);err!=nil{t.Fatal(err)}
 if verifyRegistrationInstallation(context.Background(),root,p)==nil{t.Fatal("changed executable accepted")}
}

func TestRegistrationPreflightBlocksCompletion(t *testing.T){
 _,j:=registrationFixture(t)
 remote:=&fakeRegistrationRemote{states:[]string{"completed"},deviceID:"12345678-1234-4234-8234-123456789aaa"}
 checked:=preflightRegistrationRemote{remote,func(context.Context)error{return fmt.Errorf("hardware changed")}}
 if _,err:=checked.Complete(context.Background(),j.Request);err==nil||remote.completes!=0{t.Fatal("completion bypassed preflight")}
}

func TestRestartApprovalRequiresTerminalDecisionAndPreservesHistory(t *testing.T){
 p,j:=registrationFixture(t);j.Owner="owner"
 fresh:=p.Setup.Preflight
 fresh.Hardware=hardware.Recommend(hardware.Facts{Arch:"arm64",Model:"NVIDIA Jetson AGX Orin",Tegra:true,CUDA:true})
 fresh.Resources.Resources.SystemBytes=64*GiB;fresh.Resources.Admission.AcceleratorBytes=32*GiB
 for _,state:=range []string{"pending","approved","completed","retired"}{
  called:=false
  sideEffect:=func(registrationJournal)error{called=true;return nil}
  if _,err:=restartRegistration(j,hostregistration.Status{State:state},p,fresh,sideEffect,sideEffect);err==nil||called{t.Fatal("restarted nonterminal request",state)}
 }
 archived:=false
 next,err:=restartRegistration(j,hostregistration.Status{State:"expired"},p,fresh,func(old registrationJournal)error{
  if old.Request!=j.Request||old.PrivateKey!=j.PrivateKey||old.State!="expired"{t.Fatal("archive lost old identity")}
  archived=true;return nil
 },func(next registrationJournal)error{
  if !archived{t.Fatal("replacement before archive")}
  return nil
 })
 if err!=nil{t.Fatal(err)}
 if next.Owner!=j.Owner||next.Request.RequestID==j.Request.RequestID||next.PrivateKey==j.PrivateKey||next.DeviceSecret==j.DeviceSecret||next.State!="prepared"{t.Fatal("restart did not preserve owner and rotate request/keys")}
 called:=false
 old,err:=restartRegistration(j,hostregistration.Status{State:"denied"},p,fresh,func(registrationJournal)error{return fmt.Errorf("disk full")},func(registrationJournal)error{called=true;return nil})
 if err==nil||called||old.Request!=j.Request{t.Fatal("archive failure replaced active journal")}
}

func TestRegistrationFromMinimumOnlyInstallation(t *testing.T) {
 p,base:=registrationFixture(t)
 fresh:=p.Setup.Preflight
 fresh.Hardware=hardware.Recommend(hardware.Facts{Arch:"arm64",Model:"NVIDIA Jetson AGX Orin Developer Kit",Tegra:true,CUDA:true})
 fresh.Resources.Resources.SystemBytes=64*GiB
 fresh.Resources.Resources.UMA=true
 fresh.Resources.Resources.UMAAllocationBytes=0
 fresh.Resources.Resources.AvailableSystemBytes=48*GiB
 fresh.Resources.Resources.DiskFreeBytes=100*GiB
 fresh.Resources.Resources.UMARuntime=&UMARuntime{Backend:"cuda",Device:0,FreeBytes:40*GiB,TotalBytes:64*GiB}
 fresh.Resources.Admission=CheckResources(fresh.Resources.Resources)
 before,_:=json.Marshal(fresh)
 j,err:=newRegistration(p,base.Issuer,base.Dashboard,"agx-large",fresh)
 if err!=nil {t.Fatal(err)}
 if err=j.Request.Validate();err!=nil{t.Fatal(err)}
 if j.Request.Hardware.ModelMemoryBytes!=40*GiB {t.Fatal("not current minimum of available system/runtime memory")}
 if err=validateRegistration(j,p,base.Issuer,base.Dashboard,"agx-large");err!=nil{t.Fatal(err)}
 after,_:=json.Marshal(fresh)
 if string(before)!=string(after)||fresh.Resources.Resources.UMAAllocationBytes!=0{t.Fatal("registration changed installation budget")}
 // Persist and reload exactly as the pre-login path does; no private output.
 path:=filepath.Join(t.TempDir(),"registration.json")
 if err=os.Chmod(filepath.Dir(path),0700);err!=nil{t.Fatal(err)}
 if err=saveRegistration(path,j);err!=nil{t.Fatal(err)}
 raw,err:=readSetupRegular(path,true);if err!=nil{t.Fatal(err)}
 var saved registrationJournal
 if err=decodeSetupJSON(raw,&saved);err!=nil{t.Fatal(err)}
 if err=validateRegistration(saved,p,base.Issuer,base.Dashboard,"agx-large");err!=nil{t.Fatal(err)}
 fresh.Resources.Resources.AvailableSystemBytes=12*GiB
 if _,err=newRegistration(p,base.Issuer,base.Dashboard,"agx-large",fresh);err==nil{t.Fatal("stale admission allowed insufficient current memory")}
}
