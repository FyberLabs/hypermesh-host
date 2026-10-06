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
 "regexp"
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
 Allocation int64 `json:"uma_gib,omitempty"` // Legacy sessions only; never a new installation budget.
 Headless bool `json:"headless"`
 Class string `json:"class"`
 Stage int `json:"stage"`
 PlanDirectory string `json:"plan_directory,omitempty"`
}
type guidedNetwork struct { Plane, Issuer, Dashboard string }
func installationNetwork(name string)(guidedNetwork,error) {
 suffix:=""
 switch name {case "production":case "test":suffix="test.";default:return guidedNetwork{},fmt.Errorf("choose production or test")}
 return guidedNetwork{"https://api."+suffix+"hyperme.sh","https://auth."+suffix+"hyperme.sh/realms/controlplane","https://portal."+suffix+"hyperme.sh"},nil
}
type guidedUI struct { in *bufio.Reader; out io.Writer; ctx context.Context }
func (u guidedUI) ask(label,def string)(string,error) {
 if def!="" {fmt.Fprintf(u.out,"%s [%s]: ",label,def)} else {fmt.Fprintf(u.out,"%s: ",label)}
 var line string;var err error
 if u.ctx==nil {line,err=u.in.ReadString('\n')} else {
  if u.ctx.Err()!=nil{return "",fmt.Errorf("setup cancelled; run the same installer to resume")}
  type answer struct{line string;err error}
  result:=make(chan answer,1)
  go func(){line,err:=u.in.ReadString('\n');result<-answer{line,err}}()
  select {
  case value:=<-result:line,err=value.line,value.err
  case <-u.ctx.Done():return "",fmt.Errorf("setup cancelled; run the same installer to resume")
  }
 }
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
 if p.Facts.Tegra||p.Backend=="hip" {mode="uma"}
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
 wireguardMode func()(string,string,error)
 repairWireguard func(context.Context,string,agentInstallPlan)(agentInstallPlan,error)
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
 decoder:=json.NewDecoder(&raw);decoder.UseNumber()
 if decoder.Decode(&p)!=nil{return nil,fmt.Errorf("cannot read generated installation plan")}
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
 ctx=context.WithValue(ctx,platformReviewPrompt{},g.ui.confirm)
 ctx=context.WithValue(ctx,platformReviewOutput{},g.ui.out)
 s:=g.state;n,err:=installationNetwork(s.Network);if err!=nil{return err}
 setupPath:=filepath.Join(g.dir,"setup.json")
 agentPath:=filepath.Join(g.dir,"agent.json")
 certPath:=filepath.Join(g.dir,"certification.json")
 if s.Stage==0 {
  fmt.Fprintln(g.ui.out,"Checking hardware, memory, model storage and required software…")
  args:=[]string{"--memory-mode",s.Mode,"--model-dir",s.Directory,
   "--aiuditor-check-url",n.Plane+"/api/v1/hypermesh/public/host-installation","--profile","auto"}
  if g.wireguardMode!=nil { mode,detail,e:=g.wireguardMode();if e!=nil{return e};fmt.Fprintln(g.ui.out,"WireGuard:",detail);args=append(args,"--wireguard-mode",mode) }
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
    extra,prompt,e:=guidedRebootRetry(setupRebootPath(defaultSetupState,rp.SHA256),rp)
    if e!=nil{return e}
    fmt.Fprintln(g.ui.out,"A reboot is required. This will disconnect all login sessions. Reconnect afterwards and run the same installer; it will continue here.")
    if len(extra)>1{fmt.Fprintln(g.ui.out,"The previous reboot has not been observed. Retrying requires your approval and repeats the safety checks; shutdown inhibitors remain enforced.")}
    if err=g.apply(ctx,"reboot-setup","reboot.json",prompt,extra...);err!=nil{return err}
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
  fmt.Fprintln(g.ui.out,"Checking the saved installation and registration before sign-in…")
  args:=[]string{"--plan",agentPath,"--approve-plan",ap.SHA256,"--issuer",n.Issuer,"--dashboard-url",n.Dashboard,"--class-id",s.Class}
  var registration registrationJournal
  if guidedRead(registrationPath(defaultSetupState,ap.SHA256),&registration,true)==nil && registration.State=="denied" {
   if err=g.ui.confirm("The previous browser approval ended. Start a new approval with fresh keys?");err!=nil{return err}
   args=append(args,"--restart-approval")
  }
  if err=g.execute(ctx,"register-agent",args...);err!=nil{return err}
  if err=g.advance();err!=nil{return err}
 }
 if s.Stage==4 {
  if g.repairWireguard!=nil {ap,err=g.repairWireguard(ctx,g.dir,ap);if err!=nil{return err}}
  fmt.Fprintln(g.ui.out,"Checking registration, WireGuard connectivity and heartbeat…")
  if err=g.execute(ctx,"check-agent","--plan",agentPath,"--approve-plan",ap.SHA256,"--wireguard","--heartbeat");err!=nil{return err}
  if err=g.advance();err!=nil{return err}
 }
 if s.Stage==5 {
  fmt.Fprintln(g.ui.out,"Preparing the model and runtime test required for this host…")
  if _,err=g.plan(ctx,"plan-certification","certification.json","--agent-plan",agentPath);err!=nil{return err}
  var p certificationPlan;if err=guidedRead(certPath,&p,true);err!=nil{return err}
  expired:=!p.Run.ExpiresAt.IsZero()&&!p.Run.ExpiresAt.After(time.Now())
  var journal certificationJournal
  journalPath:=certificationStatePath(defaultSetupState,p.SHA256)
  retryTerminal:=false
  if _,e:=os.Lstat(journalPath);e==nil {
   if e=guidedRead(journalPath,&journal,true);e!=nil{return e}
   if !validCertificationJournal(journal)||journal.PlanSHA256!=p.SHA256{return fmt.Errorf("certification history is inconsistent; preserve it for recovery")}
   // Submitted evidence and completed runs must reconcile their original run.
   if journal.State=="evidence-ready"||journal.State=="passed"||journal.State=="activating"||journal.State=="active"{expired=false}
   retryTerminal=journal.State=="failed"&&journal.FailureCode=="class_band"
  }else if !os.IsNotExist(e){return e}
  if expired||retryTerminal {
   if err=validateCertificationPlan(p,p.SHA256);err!=nil{return err}
   if err=g.ui.confirm("The previous model test plan expired or failed its class target. Obtain a new plan for review?");err!=nil{return err}
   archive:=filepath.Join(g.dir,"certification-history-"+p.SHA256+".json")
   if _,e:=os.Lstat(archive);!os.IsNotExist(e){return fmt.Errorf("certification plan archive already exists or is unavailable; preserve history")}
   if err=os.Rename(certPath,archive);err!=nil{return err}
   if _,err=g.plan(ctx,"plan-certification","certification.json","--agent-plan",agentPath);err!=nil{return err}
   if err=guidedRead(certPath,&p,true);err!=nil{return err}
  }
  fmt.Fprintf(g.ui.out,"Model: %s (%d bytes)\nStored at: %s\nRuntime: %s\n",p.Run.Manifest.CatalogID,p.Run.Manifest.SizeBytes,p.ModelPath,p.Run.Manifest.ImageDigest)
  extra:=[]string{"--run"}
  if journal.State=="failed"&&journal.FailureCode=="acquisition" {
   fmt.Fprintln(g.ui.out,"The previous model download failed. Check connectivity and disk space. A gated model may also require accepting its license and providing an access token.")
   token,e:=g.ui.ask("Private token file if required (Enter to retry without one)","");if e!=nil{return e}
   if token!=""{extra=append(extra,"--model-token-file",token)}
  }
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
 ui:=guidedUI{in:bufio.NewReader(input),out:out,ctx:ctx}
 fmt.Fprintln(out,"Hypermesh host setup\nThis guide checks your host, installs required software, connects your account and tests readiness. You approve machine changes before they run.")
 dir:=filepath.Join(defaultSetupState,"guided")
 unlock,err:=lockSetupState(dir);if err!=nil{fmt.Fprintln(diagnostic,err);return 1};defer unlock()
 statePath:=filepath.Join(dir,"session.json")
 var s guidedState
 machine,_,err:=rebootIdentity("");if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 if _,e:=os.Lstat(statePath);e==nil{
  err=guidedRead(statePath,&s,true)
  if err==nil&&(s.Schema!=1||s.Machine!=machine||s.Stage<0||s.Stage>7){err=fmt.Errorf("saved installation state is inconsistent; preserve it for recovery")}
  if err==nil && s.Stage==4 {
   err=func()error{
    unlockState,e:=lockSetupState(defaultSetupState);if e!=nil{return e};defer unlockState()
    planDir,e:=guidedPlanDirectory(dir,s);if e!=nil{return e}
    return resumeWireguardTransition(ctx,planDir,"",wireguardTransitionDeps{})
   }()
  }
  if err==nil && (s.Download.Build!=buildinfo.CurrentBuild()||s.Download.Commit!=buildinfo.Current()) {
   if *receipt==""{*receipt=exe+".install.json"}
   err=func()error{
    unlockState,e:=lockSetupState(defaultSetupState);if e!=nil{return e};defer unlockState()
    if s.Stage==3 || s.Stage==4 {
     return upgradeGuidedInstalled(ctx,dir,statePath,*receipt,&s,ui,func(p agentInstallPlan)error{return verifyRegistrationService(ctx,p)})
    }
    return upgradeGuidedPrerequisites(ctx,dir,statePath,*receipt,&s,ui,
     func()error{return guardRebootHost(ctx,"",defaultSetupState,rebootSetupSource())})
   }()
  }
  if err==nil{err=validateGuidedDownload(s.Download)}
  if err==nil{fmt.Fprintf(out,"Resuming setup for %s on %s. Completed steps will be checked by the installation gates.\n",s.Host,s.Network)}
 }else if !os.IsNotExist(e){err=e}else{
  // Do not treat an installation made outside this guide as a fresh machine:
  // prerequisite changes or a reboot could otherwise disrupt an existing host.
  if e:=guardRebootHost(ctx,"",defaultSetupState,rebootSetupSource());e!=nil {
   fmt.Fprintln(diagnostic,"An existing host installation or service configuration needs maintenance/recovery, not a fresh install. Existing identity and files were preserved:",e)
   return 1
  }
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
  if err==nil {
   info,e:=os.Stat(s.Directory)
   if os.IsNotExist(e) {
    fmt.Fprintln(out,"The model directory does not exist. Its parent filesystem must be the disk you intend to use.")
    if err=ui.confirm("Create "+s.Directory+" without changing partitions or mounts?");err==nil {err=agentDirectory(s.Directory,true)}
   } else if e!=nil||!info.IsDir(){err=fmt.Errorf("model storage must be an accessible directory")}
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
 planDir,err:=guidedPlanDirectory(dir,s)
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 flow:=guidedFlow{dir:planDir,state:&s,ui:ui,diagnostic:diagnostic,run:guidedCommand,save:func()error{return writeGuided(statePath,s)},
  wireguardMode:func()(string,string,error){return detectGuidedWireguard(ctx,ui.confirm)},
  repairWireguard:func(ctx context.Context,dir string,p agentInstallPlan)(agentInstallPlan,error){
   unlockState,e:=lockSetupState(defaultSetupState);if e!=nil{return p,e};defer unlockState()
   return repairGuidedWireguard(ctx,dir,p,ui)
  },
  rebootNeeded:func(p SetupPlan)(bool,error){
   needed,e:=liveRebootRequired("");if e!=nil{return false,e}
   headless,e:=headlessRebootNeeded(ctx,p,rebootSetupSource());return needed||headless,e
  },boot:func()(string,string,error){return rebootIdentity("")}}
 if err=flow.finish(ctx);err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 if s.Stage==7{fmt.Fprintf(out,"Setup completed. Review %s to manage your host. Selling is enabled separately in the dashboard.\n",s.Host)}
 return 0
}

func guidedRebootRetry(path string,p setupRebootPlan)([]string,string,error){
 if _,err:=os.Lstat(path);os.IsNotExist(err){return []string{"--reboot"},"Reboot this machine now?",nil
 }else if err!=nil{return nil,"",err}
 var j setupRebootJournal
 if err:=guidedRead(path,&j,true);err!=nil{return nil,"",fmt.Errorf("cannot read previous reboot checkpoint; preserve it: %w",err)}
 if !validSetupRebootJournal(j)||j.State!="requested"||j.PlanSHA256!=p.SHA256||
 j.SetupPlanSHA256!=p.Setup.PlanSHA256||j.MachineSHA256!=p.MachineSHA256||j.BootID!=p.BootID{
  return nil,"",fmt.Errorf("previous reboot checkpoint is inconsistent; preserve it for recovery")
 }
 return []string{"--reboot","--retry-request"},"Request the reboot again now?",nil
}

func guidedPlanDirectory(dir string,s guidedState)(string,error){
 if s.PlanDirectory==""{return dir,nil}
 if !regexp.MustCompile(`^run-[a-f0-9]{40}$`).MatchString(s.PlanDirectory){
  return "",fmt.Errorf("invalid saved plan directory")
 }
 path:=filepath.Join(dir,s.PlanDirectory)
 if err:=secureSetupDirectory(path,false);err!=nil{return "",err}
 return path,nil
}

// Before agent installation, a newer verified CLI can re-observe prerequisites.
// Preserve every old plan/journal and atomically switch only the wizard cursor;
// never rewrite an old approval to pretend it belongs to the new executable.
func upgradeGuidedPrerequisites(ctx context.Context,dir,statePath,receipt string,s *guidedState,ui guidedUI,guard func()error)error{
 if s.Stage>1{return fmt.Errorf("an installed host must resume with its original installer; existing approvals were preserved")}
 var download guidedDownload
 if err:=guidedRead(receipt,&download,false);err!=nil{return fmt.Errorf("new installer needs its matching verifier receipt: %w",err)}
 if err:=validateGuidedDownload(download);err!=nil{return err}
 if download.Build<=s.Download.Build||download.Channel!=s.Download.Channel{
  return fmt.Errorf("prerequisite recovery requires a newer verified build on the same software channel")
 }
 if err:=guard();err!=nil{return err}
 if err:=ui.confirm("Use this newer installer to recheck prerequisites and review fresh plans? Previous plans and model files will be preserved.");err!=nil{return err}
 if err:=ctx.Err();err!=nil{return err}
 next:=*s;next.Download=download;next.Allocation=0;next.Stage=0;next.PlanDirectory="run-"+download.Commit
 path:=filepath.Join(dir,next.PlanDirectory)
 if err:=secureSetupDirectory(path,true);err!=nil{return err}
 key,err:=readSetupRegular(download.TrustedKey,false);if err!=nil{return err}
 keyPath:=filepath.Join(path,"trusted-release.pub")
 payload:=agentPayload{agentFile{keyPath,digestSetupBytes(key),0o600},key}
 matches,err:=agentFileMatches(keyPath,payload);if err!=nil{return err}
 if !matches{if err=writeAgentFile(keyPath,payload);err!=nil{return err}}
 next.Download.TrustedKey=keyPath
 if err=guard();err!=nil{return err}
 if err=writeGuided(statePath,next);err!=nil{return err}
 *s=next
 return nil
}

func upgradeGuidedInstalled(ctx context.Context,dir,statePath,receipt string,s *guidedState,ui guidedUI,verify func(agentInstallPlan)error)error{
 if s.Stage!=3 && s.Stage!=4 {return fmt.Errorf("installed continuation requires registration or connectivity checks")}
 var download guidedDownload
 if err:=guidedRead(receipt,&download,false);err!=nil{return err}
 if err:=validateGuidedDownload(download);err!=nil{return err}
 if download.Build<=s.Download.Build||download.Channel!=s.Download.Channel{return fmt.Errorf("a newer verified installer on the same channel is required")}
 planDir,err:=guidedPlanDirectory(dir,*s);if err!=nil{return err}
 var p agentInstallPlan
 if err=guidedRead(filepath.Join(planDir,"agent.json"),&p,true);err!=nil{return err}
 if !setupDigestRE.MatchString(p.SHA256)||agentPlanDigest(p)!=p.SHA256||
 p.Setup.InstallerBuild>s.Download.Build||
 (s.PlanDirectory!=""&&s.PlanDirectory!="run-"+p.Setup.InstallerCommit)||
 p.HostName!=s.Host||p.TenantID!=s.Tenant||p.Setup.Preflight.Resources.ModelDirectory!=s.Directory {
  return fmt.Errorf("installed plan does not match the saved session; preserve state")
 }
 network,err:=installationNetwork(s.Network);if err!=nil{return err}
 if p.ControlPlane!=network.Plane{return fmt.Errorf("installed network differs from session")}
 if err=validateInstalledAgentInputs(p);err!=nil{return err}
 if err=verify(p);err!=nil{return err}
 if err=ui.confirm("Use this newer installer to continue onboarding of the verified inactive installation? Existing files and approvals will be preserved.");err!=nil{return err}
 if err=ctx.Err();err!=nil{return err}
 if err=verify(p);err!=nil{return err}
 next:=*s;next.Download=download
 if err=writeGuided(statePath,next);err!=nil{return err}
 *s=next
 return nil
}
