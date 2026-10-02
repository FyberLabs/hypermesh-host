//go:build linux

package installer

import (
 "bufio"
 "bytes"
 "context"
 "encoding/json"
 "flag"
 "fmt"
 "io"
 "net/http"
 "os"
 "path/filepath"
 "strconv"
 "strings"
 "time"

 "github.com/FyberLabs/hypermesh-host/internal/buildinfo"
 "github.com/FyberLabs/hypermesh-host/internal/hardware"
 "github.com/FyberLabs/hypermesh-host/internal/hostregistration"
)

// The wizard records navigation only. Existing stage implementations retain
// authority over approvals, installed payloads, hardware and remote identity.
type guidedDownload struct {
 Schema int `json:"schema"`
 ReleaseURL string `json:"release_url"`
 Channel string `json:"channel"`
 Build int `json:"build"`
 Commit string `json:"commit"`
 TrustedKey string `json:"trusted_key"`
 Fingerprint string `json:"fingerprint"`
}
type guidedState struct {
 Schema int `json:"schema"`
 Machine string `json:"machine"`
 Download guidedDownload `json:"download"`
 Network string `json:"network"`
 Tenant string `json:"tenant"`
 Host string `json:"host"`
 Directory string `json:"model_directory"`
 Mode string `json:"memory_mode"`
 Allocation int64 `json:"uma_gib"`
 Headless bool `json:"headless"`
 Class string `json:"class"`
 Stage int `json:"stage"`
}
type guidedNetwork struct { Plane, Issuer, Dashboard string }
func installationNetwork(name string)(guidedNetwork,error) {
 suffix:=""
 switch name {case "production":case "test":suffix="test.";default:return guidedNetwork{},fmt.Errorf("choose production or test")}
 return guidedNetwork{"https://api."+suffix+"hyperme.sh","https://auth."+suffix+"hyperme.sh/realms/controlplane","https://portal."+suffix+"hyperme.sh"},nil
}
type guidedUI struct { in *bufio.Reader; out io.Writer }
func (u guidedUI) ask(label,def string)(string,error) {
 if def!="" {fmt.Fprintf(u.out,"%s [%s]: ",label,def)} else {fmt.Fprintf(u.out,"%s: ",label)}
 line,err:=u.in.ReadString('\n')
 if err!=nil{return "",fmt.Errorf("setup paused: interactive input ended; run the same installer again to resume")}
 answer:=strings.TrimSpace(line);if answer==""{answer=def}
 for _,r:=range answer {if r<32||r==127{return "",fmt.Errorf("invalid control character in answer")}}
 return answer,nil
}
func (u guidedUI) confirm(label string)error {
 answer,err:=u.ask(label+" (y/N)","");if err!=nil{return err}
 if !strings.EqualFold(answer,"y")&&!strings.EqualFold(answer,"yes"){return fmt.Errorf("setup paused; nothing further changed. Run the same installer again to resume")}
 return nil
}
func writeGuided(path string,value any)error {
 if err:=secureSetupDirectory(filepath.Dir(path),false);err!=nil{return err}
 if _,err:=os.Lstat(path);err==nil {if _,err=readSetupRegular(path,true);err!=nil{return err}} else if !os.IsNotExist(err){return err}
 raw,err:=json.MarshalIndent(value,"","  ");if err!=nil{return err}
 f,err:=os.CreateTemp(filepath.Dir(path),".guided-");if err!=nil{return err}
 defer os.Remove(f.Name())
 if _,err=f.Write(raw);err!=nil{f.Close();return err}
 if err=f.Sync();err!=nil{f.Close();return err};if err=f.Close();err!=nil{return err}
 if err=os.Rename(f.Name(),path);err!=nil{return err}
 d,err:=os.Open(filepath.Dir(path));if err!=nil{return err};defer d.Close();return d.Sync()
}
func guidedRead(path string,dest any,private bool)error {
 raw,err:=readSetupRegular(path,private);if err!=nil{return err}
 return decodeSetupJSON(raw,dest)
}
func validateGuidedDownload(d guidedDownload)error {
 if d.Schema!=1||d.Build<=0||d.Build!=buildinfo.CurrentBuild()||d.Commit!=buildinfo.Current()||
 (d.Channel!="test"&&d.Channel!="prod")||validateEndpoint(d.ReleaseURL)!=nil||
 !filepath.IsAbs(d.TrustedKey)||filepath.Clean(d.TrustedKey)!=d.TrustedKey {
 return fmt.Errorf("download receipt does not match this installer; keep the verified executable and receipt together")}
 return nil
}
func guidedTenant(ctx context.Context,n guidedNetwork)(string,error) {
 ctx,cancel:=context.WithTimeout(ctx,15*time.Second);defer cancel()
 req,err:=http.NewRequestWithContext(ctx,"GET",n.Plane+"/api/v1/hypermesh/public/host-installation",nil);if err!=nil{return "",err}
 client:=http.Client{CheckRedirect:func(*http.Request,[]*http.Request)error{return http.ErrUseLastResponse}}
 response,err:=client.Do(req);if err!=nil{return "",fmt.Errorf("cannot reach the selected network; check internet access and try again")}
 defer response.Body.Close()
 if response.StatusCode!=200{return "",fmt.Errorf("network setup information unavailable (HTTP %d); the control plane must support guided installation",response.StatusCode)}
 raw,err:=io.ReadAll(io.LimitReader(response.Body,4097));if err!=nil||len(raw)>4096{return "",fmt.Errorf("invalid network setup response")}
 var body struct {Schema int `json:"schema"`;Tenant string `json:"tenant_id"`;Issuer string `json:"issuer"`}
 if json.Unmarshal(raw,&body)!=nil||body.Schema!=1||!hostregistration.UUID.MatchString(body.Tenant)||body.Issuer!=n.Issuer{return "",fmt.Errorf("network setup identity does not match the selected network")}
 return body.Tenant,nil
}
func guidedDefaults(p hardware.Profile)(string,int64,string) {
 mode:="discrete";allocation:=int64(0);class:=""
 if p.Facts.Tegra||p.Backend=="hip" {mode="uma";allocation=p.ModelBudgetBytes/GiB}
 model:=strings.ToLower(p.Facts.Model)
 if p.Facts.Tegra&&strings.Contains(model,"agx orin"){class="agx-large"}
 if p.Facts.Tegra&&strings.Contains(model,"orin nx"){class="nx-volume"}
 return mode,allocation,class
}

type guidedRunner func(context.Context,[]string,io.Writer,io.Writer)int
type guidedFlow struct {
 dir string
 state *guidedState
 ui guidedUI
 diagnostic io.Writer
 run func(string)guidedRunner
 save func()error
 rebootNeeded func(SetupPlan)(bool,error)
 boot func()(string,string,error)
}
func guidedCommand(name string)guidedRunner {
 switch name {
 case "plan-setup":return RunSetupPlan
 case "apply-setup":return RunApplySetup
 case "plan-reboot":return RunPlanReboot
 case "reboot-setup":return RunRebootSetup
 case "resume-setup":return RunResumeSetup
 case "plan-agent":return RunPlanAgent
 case "install-agent":return RunInstallAgent
 case "register-agent":return RunRegisterAgent
 case "check-agent":return RunCheckAgent
 case "plan-certification":return RunPlanCertification
 case "certify-agent":return RunCertifyAgent
 case "activate-agent":return RunActivateAgent
 case "host-status":return RunHostStatus
 }
 panic("unknown guided operation")
}
func (g guidedFlow) execute(ctx context.Context,name string,args ...string)error {
 var output bytes.Buffer
 code:=g.run(name)(ctx,args,&output,g.diagnostic)
 if name=="host-status"||name=="check-agent" {
  var report struct {Gates []onboardingGate `json:"gates"`;PlaneSellState string `json:"plane_sell_state"`}
  if json.Unmarshal(output.Bytes(),&report)==nil {
   for _,gate:=range report.Gates {fmt.Fprintf(g.ui.out," - %s: %s\n",gate.State,gate.NextAction)}
   if report.PlaneSellState!=""{fmt.Fprintln(g.ui.out,"Dashboard selling state:",report.PlaneSellState)}
  }
 }
 if code!=0{return fmt.Errorf("%s could not finish; resolve the message above and run the same installer again",name)}
 return nil
}
func (g guidedFlow) plan(ctx context.Context,name,file string,args ...string)(map[string]any,error) {
 path:=filepath.Join(g.dir,file)
 var p map[string]any
 if _,err:=os.Lstat(path);err==nil {err=guidedRead(path,&p,true);return p,err} else if !os.IsNotExist(err){return nil,err}
 var raw bytes.Buffer
 if g.run(name)(ctx,args,&raw,g.diagnostic)!=0 {
  // Surface preflight blockers, never bury them in a JSON plan.
  var failed struct{Blockers []string `json:"blockers"`};_ = json.Unmarshal(raw.Bytes(),&failed)
  for _,reason:=range failed.Blockers {fmt.Fprintln(g.ui.out," -",reason)}
  return nil,fmt.Errorf("%s is blocked; fix the listed prerequisites and rerun the installer",name)
 }
 if json.Unmarshal(raw.Bytes(),&p)!=nil{return nil,fmt.Errorf("cannot read generated installation plan")}
 if err:=writeGuided(path,p);err!=nil{return nil,err}
 return p,nil
}
func guidedDigest(p map[string]any)(string,error) {
 sha,_:=p["plan_sha256"].(string);if !setupDigestRE.MatchString(sha){return "",fmt.Errorf("saved plan has no valid approval digest")}
 return sha,nil
}
func (g guidedFlow) apply(ctx context.Context,name,file,prompt string,extra ...string)error {
 var p map[string]any
 if err:=guidedRead(filepath.Join(g.dir,file),&p,true);err!=nil{return err}
 sha,err:=guidedDigest(p);if err!=nil{return err}
 fmt.Fprintln(g.ui.out,"Detailed plan saved at",filepath.Join(g.dir,file))
 if err=g.ui.confirm(prompt);err!=nil{return err}
 args:=append([]string{"--plan",filepath.Join(g.dir,file),"--approve-plan",sha},extra...)
 return g.execute(ctx,name,args...)
}
func (g guidedFlow) advance()error {g.state.Stage++;return g.save()}
func (g guidedFlow) finish(ctx context.Context)error {
 s:=g.state;n,err:=installationNetwork(s.Network);if err!=nil{return err}
 setupPath:=filepath.Join(g.dir,"setup.json")
 agentPath:=filepath.Join(g.dir,"agent.json")
 certPath:=filepath.Join(g.dir,"certification.json")
 if s.Stage==0 {
  fmt.Fprintln(g.ui.out,"Checking hardware, memory, model storage and required software…")
  args:=[]string{"--memory-mode",s.Mode,"--uma-gib",strconv.FormatInt(s.Allocation,10),"--model-dir",s.Directory,
   "--aiuditor-check-url",n.Plane+"/api/v1/hypermesh/public/host-installation","--profile","auto"}
  if s.Headless{args=append(args,"--headless")}
  if _,err=g.plan(ctx,"plan-setup","setup.json",args...);err!=nil{return err}
  var p SetupPlan;if err=guidedRead(setupPath,&p,true);err!=nil{return err}
  fmt.Fprintf(g.ui.out,"Model storage: %s\n",s.Directory)
  for _,change:=range p.Changes {fmt.Fprintf(g.ui.out," - %s: %s\n",change.Kind,strings.Join(change.Argv," "))}
  fmt.Fprintln(g.ui.out,"Existing vendor drivers and kernel are preserved.")
  if err=g.apply(ctx,"apply-setup","setup.json","Apply these prerequisite and headless changes?");err!=nil{return err}
  if err=g.advance();err!=nil{return err}
 }
 if s.Stage==1 {
  var p SetupPlan;if err=guidedRead(setupPath,&p,true);err!=nil{return err}
  rebootPath:=filepath.Join(g.dir,"reboot.json")
  _,statErr:=os.Lstat(rebootPath)
  needed:=statErr==nil
  if os.IsNotExist(statErr){needed,err=g.rebootNeeded(p);if err!=nil{return err}}else if statErr!=nil{return statErr}
  if needed {
   if _,err=g.plan(ctx,"plan-reboot","reboot.json","--setup-plan",setupPath);err!=nil{return err}
   var rp setupRebootPlan;if err=guidedRead(rebootPath,&rp,true);err!=nil{return err}
   _,boot,e:=g.boot();if e!=nil{return e}
   if boot==rp.BootID {
    // Never automatically repeat an already requested or uncertain reboot.
    if _,e=os.Lstat(setupRebootPath(defaultSetupState,rp.SHA256));!os.IsNotExist(e){
     return fmt.Errorf("a reboot was already requested; restart the machine when safe, then run this installer again")}
    fmt.Fprintln(g.ui.out,"A reboot is required. Reconnect afterwards and run the same installer; it will continue here.")
    if err=g.apply(ctx,"reboot-setup","reboot.json","Reboot this machine now?","--reboot");err!=nil{return err}
    return nil
   }
   if err=g.execute(ctx,"resume-setup","--plan",rebootPath,"--approve-plan",rp.SHA256);err!=nil{return err}
  }
  if err=g.advance();err!=nil{return err}
 }
 if s.Stage==2 {
  fmt.Fprintln(g.ui.out,"Downloading and verifying the host service…")
  if _,err=g.plan(ctx,"plan-agent","agent.json","--setup-plan",setupPath,"--release-url",s.Download.ReleaseURL,
   "--channel",s.Download.Channel,"--trusted-key",s.Download.TrustedKey,"--fingerprints",s.Download.Fingerprint,
   "--control-plane-url",n.Plane,"--tenant-id",s.Tenant,"--host-name",s.Host);err!=nil{return err}
  var p agentInstallPlan;if err=guidedRead(agentPath,&p,true);err!=nil{return err}
  for _,file:=range p.Files{fmt.Fprintln(g.ui.out," - Install",file.Path)}
  if err=g.apply(ctx,"install-agent","agent.json","Install these verified files and the inactive systemd service?");err!=nil{return err}
  if err=g.advance();err!=nil{return err}
 }
 var ap agentInstallPlan
 if err=guidedRead(agentPath,&ap,true);err!=nil{return err}
 if s.Stage==3 {
  fmt.Fprintln(g.ui.out,"Connect this host to your account. Open the sign-in link on any device, then approve this host in your dashboard.")
  args:=[]string{"--plan",agentPath,"--approve-plan",ap.SHA256,"--issuer",n.Issuer,"--dashboard-url",n.Dashboard,"--class-id",s.Class}
  var registration registrationJournal
  if guidedRead(registrationPath(defaultSetupState,ap.SHA256),&registration,true)==nil && (registration.State=="denied"||registration.State=="expired") {
   if err=g.ui.confirm("The previous browser approval ended. Start a new approval with fresh keys?");err!=nil{return err}
   args=append(args,"--restart-approval")
  }
  if err=g.execute(ctx,"register-agent",args...);err!=nil{return err}
  if err=g.advance();err!=nil{return err}
 }
 if s.Stage==4 {
  fmt.Fprintln(g.ui.out,"Checking registration, WireGuard connectivity and heartbeat…")
  if err=g.execute(ctx,"check-agent","--plan",agentPath,"--approve-plan",ap.SHA256,"--wireguard","--heartbeat");err!=nil{return err}
  if err=g.advance();err!=nil{return err}
 }
 if s.Stage==5 {
  fmt.Fprintln(g.ui.out,"Preparing the model and runtime test required for this host…")
  if _,err=g.plan(ctx,"plan-certification","certification.json","--agent-plan",agentPath);err!=nil{return err}
  var p certificationPlan;if err=guidedRead(certPath,&p,true);err!=nil{return err}
  fmt.Fprintf(g.ui.out,"Model: %s (%d bytes)\nStored at: %s\nRuntime: %s\n",p.Run.Manifest.CatalogID,p.Run.Manifest.SizeBytes,p.ModelPath,p.Run.Manifest.ImageDigest)
  token,e:=g.ui.ask("Private model-token file, only if this model requires a license/token (Enter to skip)","");if e!=nil{return e}
  extra:=[]string{"--run"};if token!=""{extra=append(extra,"--model-token-file",token)}
  if err=g.apply(ctx,"certify-agent","certification.json","Download this model and run its hardware certification test?",extra...);err!=nil{return err}
  if err=g.advance();err!=nil{return err}
 }
 if s.Stage==6 {
  fmt.Fprintln(g.ui.out,"Certification passed. Confirm ownership in your browser to start the service paused; selling remains off.")
  if err=g.apply(ctx,"activate-agent","certification.json","Activate the host service in paused mode?","--start");err!=nil{return err}
  if err=g.advance();err!=nil{return err}
 }
 fmt.Fprintln(g.ui.out,"Checking the host's current status…")
 // Display live status rather than declaring readiness from the wizard cursor.
 return g.execute(ctx,"host-status","--plan",certPath)
}
func RunGuidedInstall(ctx context.Context,args []string,input io.Reader,out,diagnostic io.Writer)int {
 flags:=flag.NewFlagSet("install",flag.ContinueOnError);flags.SetOutput(diagnostic)
 receipt:=flags.String("receipt","","download receipt (normally found beside the executable)")
 if err:=flags.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
 if flags.NArg()!=0{return 2}
 exe,err:=os.Executable();if err!=nil{fmt.Fprintln(diagnostic,"cannot locate the verified installer");return 1}
 if os.Geteuid()!=0{fmt.Fprintf(diagnostic,"Start guided setup with:\n  sudo %q\n",exe);return 1}
 ui:=guidedUI{bufio.NewReader(input),out}
 fmt.Fprintln(out,"Hypermesh host setup\nThis guide checks your host, installs required software, connects your account and tests readiness. You approve machine changes before they run.")
 dir:=filepath.Join(defaultSetupState,"guided")
 unlock,err:=lockSetupState(dir);if err!=nil{fmt.Fprintln(diagnostic,err);return 1};defer unlock()
 statePath:=filepath.Join(dir,"session.json")
 var s guidedState
 machine,_,err:=rebootIdentity("");if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 if _,e:=os.Lstat(statePath);e==nil{
  err=guidedRead(statePath,&s,true)
  if err==nil&&(s.Schema!=1||s.Machine!=machine||s.Stage<0||s.Stage>7){err=fmt.Errorf("saved installation state is inconsistent; preserve it for recovery")}
  if err==nil{err=validateGuidedDownload(s.Download)}
  if err==nil{fmt.Fprintf(out,"Resuming setup for %s on %s. Completed steps will be checked by the installation gates.\n",s.Host,s.Network)}
 }else if !os.IsNotExist(e){err=e}else{
  if *receipt==""{*receipt=exe+".install.json"}
  err=guidedRead(*receipt,&s.Download,false)
  if err==nil{err=validateGuidedDownload(s.Download)}
  if err!=nil{fmt.Fprintln(diagnostic,"Cannot find a matching download receipt. Run the website verifier for this release again in a new directory.");return 1}
  s.Schema=1;s.Machine=machine
  s.Network,err=ui.ask("Account network: production or test (separate from the "+s.Download.Channel+" software channel)","production")
  var network guidedNetwork
  if err==nil{network,err=installationNetwork(s.Network)}
  if err==nil{s.Tenant,err=guidedTenant(ctx,network)}
  p:=hardware.Detect(ctx)
  s.Mode,s.Allocation,s.Class=guidedDefaults(p)
  if err==nil {
   if s.Class=="" {err=fmt.Errorf("this hardware has no supported registration/certification class yet; no host software was installed")} else {
    err=registrationClass(s.Class,Plan{Hardware:p,Resources:ResourceReport{Resources:Resources{SystemBytes:p.Facts.MemoryBytes}}})
   }
  }
  hostname,_:=os.Hostname()
  if err==nil{s.Host,err=ui.ask("Name shown in your host dashboard",hostname)}
  if err==nil&&!agentNameRE.MatchString(s.Host){err=fmt.Errorf("host name must contain 1-63 letters, digits, dots, underscores or hyphens")}
  if err==nil{s.Directory,err=ui.ask("Existing directory for model storage","/var/lib/hypermesh/models")}
  if err==nil&&(!filepath.IsAbs(s.Directory)||filepath.Clean(s.Directory)!=s.Directory){err=fmt.Errorf("choose an absolute existing model directory")}
  if err==nil&&s.Mode=="uma"{
   var answer string;answer,err=ui.ask("Memory available to models (GiB)",strconv.FormatInt(s.Allocation,10))
   if err==nil{s.Allocation,err=strconv.ParseInt(answer,10,64)}
   if err==nil&&s.Allocation<12{err=fmt.Errorf("at least 12 GiB is required; no changes applied")}
  }
  if err==nil{
   var answer string;answer,err=ui.ask("Use headless mode (disable graphical login after reboot)? yes/no","yes")
   if err==nil {switch strings.ToLower(answer){case "yes","y":s.Headless=true;case "no","n":default:err=fmt.Errorf("answer yes or no")}}
  }
  if err==nil {
   key,e:=readSetupRegular(s.Download.TrustedKey,false)
   if e!=nil {err=e} else {
    path:=filepath.Join(dir,"trusted-release.pub")
    payload:=agentPayload{agentFile{path,digestSetupBytes(key),0o600},key}
    exists,e:=agentFileMatches(path,payload)
    if e!=nil {err=e} else if !exists {err=writeAgentFile(path,payload)}
    if err==nil {s.Download.TrustedKey=path}
   }
  }
  if err==nil{err=writeGuided(statePath,s)}
 }
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 flow:=guidedFlow{dir:dir,state:&s,ui:ui,diagnostic:diagnostic,run:guidedCommand,save:func()error{return writeGuided(statePath,s)},
  rebootNeeded:func(p SetupPlan)(bool,error){
   needed,e:=liveRebootRequired("");if e!=nil{return false,e}
   headless,e:=headlessRebootNeeded(ctx,p,rebootSetupSource());return needed||headless,e
  },boot:func()(string,string,error){return rebootIdentity("")}}
 if err=flow.finish(ctx);err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 if s.Stage==7{fmt.Fprintf(out,"Setup completed. Review %s to manage your host. Selling is enabled separately in the dashboard.\n",s.Host)}
 return 0
}
