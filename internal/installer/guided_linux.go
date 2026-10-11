//go:build linux

package installer

import (
 "strconv"
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
 Autonomous bool `json:"autonomous,omitempty"`
 // SellConsent records that the saved continuation approval covers selling
 // after certification. Approvals saved by earlier installers covered paused
 // startup only.
 SellConsent bool `json:"sell_consent,omitempty"`
 ContinuationSHA string `json:"continuation_sha256,omitempty"`
 HostCLISHA string `json:"host_cli_sha256,omitempty"`
 LauncherSHA string `json:"launcher_sha256,omitempty"`
}
type guidedNetwork struct { Plane, Issuer, Dashboard string }
func installationNetwork(name string)(guidedNetwork,error) {
 suffix:=""
 switch name {case "production":case "test":suffix="test.";default:return guidedNetwork{},fmt.Errorf("choose production or test")}
 return guidedNetwork{"https://api."+suffix+"hyperme.sh","https://auth."+suffix+"hyperme.sh/realms/controlplane","https://portal."+suffix+"hyperme.sh"},nil
}
type guidedUI struct {
 in *bufio.Reader; out io.Writer; ctx context.Context
 // yes answers the covered questions without reading input; see yesPrompt.
 yes bool
 // Settings given as flags answer their question instead of asking it.
 cacheQuota,cacheReserve string
}
const (
 promptEnroll="Enroll this host and apply the listed changes, including required reboot, automatic model tests, signed service installation, and service startup and selling after admin approval?"
 promptContinue="Continue this registered installation automatically after reboot, network interruptions and admin review, including model tests, service startup and selling once certified?"
 promptPausedOnly="The saved approval covered paused startup only. Start selling on this host once the service is verified?"
 promptModule="Load the installed WireGuard kernel module to verify it works? No packages or kernel files will be changed."
 promptCacheSave="Save this host's model cache quota and free-space reserve?"
)
// yesPrompt lists the questions that --yes answers. It is the saved continuation
// list plus the questions of a first setup that precede it. It is not a general
// "yes": review of changed packages, a new browser approval, a newer installer,
// a WireGuard repair and anything unexpected stay with the owner.
func yesPrompt(label string)bool {
 switch label {
 case promptEnroll,promptContinue,promptPausedOnly,promptModule,promptCacheSave:return true
 }
 return continuationPrompt(label)||(strings.HasPrefix(label,"Create ")&&strings.HasSuffix(label," without changing partitions or mounts?"))
}
// preset returns the value of a setting given as a flag, announcing its use.
func (u guidedUI) preset(flagName,value string)(string,bool) {
 if value==""{return "",false}
 fmt.Fprintf(u.out,"Using --%s %s\n",flagName,value)
 return value,true
}
func (u guidedUI) ask(label,def string)(string,error) {
 // --yes is this run's own consent, so it answers even when a saved continuation
 // approval is also present; the saved approval is not widened for other runs.
 if u.yes {
  if def!=""{fmt.Fprintf(u.out,"%s: %s (default)\n",label,def)}
  return def,nil
 }
 if u.ctx!=nil && u.ctx.Value(continuationConsent{})==true {
  if label=="Private Hugging Face token file for a gated model (Enter for public or cached models)" || label=="Private token file if required (Enter to retry without one)" {return "",nil}
  return "",fmt.Errorf("onboarding requires additional configuration: %s",label)
 }
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
 if u.yes {
  if yesPrompt(label){fmt.Fprintf(u.out,"Approved by --yes: %s\n",label);return nil}
  return fmt.Errorf("--yes does not cover this question: %s Run the installer again without --yes to answer it",label)
 }
 if u.ctx!=nil && u.ctx.Value(continuationConsent{})==true {
  if continuationPrompt(label){return nil}
  return fmt.Errorf("onboarding needs review of a change outside saved consent: %s",label)
 }

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
 prepareContinuation func(context.Context, SetupPlan)error
 adoptEnrollment func(context.Context,agentInstallPlan)(bool,error)
 dir string
 state *guidedState
 ui guidedUI
 diagnostic io.Writer
 run func(string)guidedRunner
 save func()error
 rebootNeeded func(SetupPlan)(bool,error)
 boot func()(string,string,error)
 wireguardMode func()(string,string,error)
 qualification func(context.Context,string)(bool,error)
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
 if code!=0 && g.state.Autonomous{return fmt.Errorf("%s has not completed; the continuation service will retry with saved identity and approvals",name)}
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
  if g.state.Autonomous{return nil,fmt.Errorf("%s is blocked; the continuation service will retry after backoff",name)}
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
 // Older installers advanced the cursor even when activation release was
 // unavailable. Reconcile through the existing authenticated activation path;
 // its active-service branch does not restart the host or clear an owner pause.
 if g.ui.yes&&s.Stage==7&&s.Autonomous&&!s.SellConsent {
  // --yes is this run's consent to start selling for a session an older installer approved for paused startup only.
  if err=g.ui.confirm(promptPausedOnly);err!=nil{return err}
  s.SellConsent=true
  if err=g.save();err!=nil{return err}
 }
 if s.Stage==7&&s.Autonomous&&s.SellConsent {
  if err=g.apply(ctx,"activate-agent","certification.json","Activate the host service and start selling?","--start");err!=nil{return err}
 }
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
  if g.prepareContinuation!=nil {
   if err=g.prepareContinuation(ctx,p);err!=nil{return err}
   ctx=withContinuationConsent(ctx);g.ui.ctx=ctx
   ctx=context.WithValue(ctx,platformReviewPrompt{},g.ui.confirm)
  }
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
    if s.Autonomous {fmt.Fprintln(g.ui.out,"A reboot is required. This will disconnect login sessions. Onboarding will continue automatically after boot.")} else {fmt.Fprintln(g.ui.out,"A reboot is required. This will disconnect all login sessions. Reconnect afterwards and run the same installer; it will continue here.")}
    if len(extra)>1{fmt.Fprintln(g.ui.out,"The previous reboot has not been observed. Retrying repeats the safety checks; shutdown inhibitors remain enforced.")}
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
  adopted:=false
  if g.adoptEnrollment!=nil {adopted,err=g.adoptEnrollment(ctx,ap);if err!=nil{return err}}
  if !adopted {if err=g.execute(ctx,"register-agent",args...);err!=nil{return err}}
  if err=g.advance();err!=nil{return err}
 }
 if s.Stage==4 {
  if g.repairWireguard!=nil {ap,err=g.repairWireguard(ctx,g.dir,ap);if err!=nil{return err}}
  fmt.Fprintln(g.ui.out,"Checking registration, WireGuard connectivity and heartbeat…")
  if err=g.execute(ctx,"check-agent","--plan",agentPath,"--approve-plan",ap.SHA256,"--wireguard","--heartbeat");err!=nil{return err}
  if err=g.advance();err!=nil{return err}
 }
 if s.Stage==5 {
  if g.qualification!=nil {ready,e:=g.qualification(ctx,agentPath);if e!=nil{return e};if !ready{return nil}}
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
   retryTerminal=journal.State=="failed"
  }else if !os.IsNotExist(e){return e}
  if expired||retryTerminal {
   if err=validateCertificationPlan(p,p.SHA256);err!=nil{return err}
   if err=g.ui.confirm("The previous model test expired or failed. Keep its history and obtain a fresh plan to rebenchmark?");err!=nil{return err}
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
  fmt.Fprintln(g.ui.out,"Certification passed. Using this host’s saved registration to start the service; selling starts once startup is verified.")
  fmt.Fprintln(g.ui.out,"Pause or resume selling at any time with: sudo hypermesh-host sell pause | sell resume")
  if g.state.Autonomous&&!g.state.SellConsent{
   if !g.ui.yes&&g.ui.ctx!=nil&&g.ui.ctx.Value(continuationConsent{})==true{
    return fmt.Errorf("certification passed, but the saved approval covers paused startup only; run the installer again with --interactive to approve starting selling")
   }
   if err=g.ui.confirm(promptPausedOnly);err!=nil{return err}
   g.state.SellConsent=true
   if err=g.save();err!=nil{return err}
  }
  if err=g.apply(ctx,"activate-agent","certification.json","Activate the host service and start selling?","--start");err!=nil{return err}
  if err=g.advance();err!=nil{return err}
 }
 fmt.Fprintln(g.ui.out,"Checking the host's current status…")
 // Display live status rather than declaring readiness from the wizard cursor.
 return g.execute(ctx,"host-status","--plan",certPath)
}
func RunGuidedInstall(ctx context.Context,args []string,input io.Reader,out,diagnostic io.Writer)int {
 flags:=flag.NewFlagSet("install",flag.ContinueOnError);flags.SetOutput(diagnostic)
 background:=flags.Bool("continue",false,"resume saved onboarding consent without interactive input")
 interactive:=flags.Bool("interactive",false,"explicitly review prompts and supply recovery input")
 cacheSettings:=flags.Bool("cache-settings",false,"review host model cache quota and reserve")
 receipt:=flags.String("receipt","","download receipt (normally found beside the executable)")
 yes:=flags.Bool("yes",false,"give the consent for this whole setup once: accept the setup questions with the defaults detected on this host, or the settings given as flags")
 networkFlag:=flags.String("network","","account network: production or test (default production)")
 hostFlag:=flags.String("host-name","","name shown in your host dashboard (default this machine's host name)")
 dirFlag:=flags.String("model-dir","","existing directory for model storage (default /var/lib/hypermesh/models)")
 headlessFlag:=flags.Bool("headless",true,"disable graphical login after reboot (use --headless=false to keep it)")
 quotaFlag:=flags.String("cache-quota-gib","","maximum disk space for downloaded models in GiB")
 reserveFlag:=flags.String("cache-reserve-gib","","disk space to keep free on the model filesystem in GiB (at least 2)")
 if err:=flags.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
 if flags.NArg()!=0{return 2}
 given:=map[string]bool{}
 flags.Visit(func(f *flag.Flag){given[f.Name]=true})
 headlessSet:=given["headless"]
 *hostFlag=strings.TrimSpace(*hostFlag)
 for _,name:=range []string{"network","host-name","model-dir","cache-quota-gib","cache-reserve-gib"} {
  value:=map[string]string{"network":*networkFlag,"host-name":*hostFlag,"model-dir":*dirFlag,"cache-quota-gib":*quotaFlag,"cache-reserve-gib":*reserveFlag}[name]
  if given[name]&&value=="" {fmt.Fprintf(diagnostic,"--%s needs a value; leave the flag out to use the default\n",name);return 2}
 }
 settings:=given["network"]||given["host-name"]||given["model-dir"]||headlessSet
 if *yes && *interactive {fmt.Fprintln(diagnostic,"--yes and --interactive contradict each other: --yes answers the questions that --interactive asks");return 2}
 if *background && (*interactive || *cacheSettings || *yes || settings || given["cache-quota-gib"] || given["cache-reserve-gib"]) {fmt.Fprintln(diagnostic,"--continue cannot be combined with interactive options, --yes or setting flags");return 2}
 if *networkFlag!="" {if _,e:=installationNetwork(*networkFlag);e!=nil{fmt.Fprintln(diagnostic,"--network:",e);return 2}}
 if *hostFlag!="" && !agentNameRE.MatchString(*hostFlag) {fmt.Fprintln(diagnostic,"--host-name must contain 1-63 letters, digits, dots, underscores or hyphens");return 2}
 if *quotaFlag!="" {if n,e:=strconv.ParseUint(*quotaFlag,10,32);e!=nil||n==0{fmt.Fprintln(diagnostic,"--cache-quota-gib must be a positive whole number of GiB");return 2}}
 if *reserveFlag!="" {if n,e:=strconv.ParseUint(*reserveFlag,10,32);e!=nil||n<2{fmt.Fprintln(diagnostic,"--cache-reserve-gib must be a whole number of GiB, at least 2");return 2}}
 if *dirFlag!="" && (!filepath.IsAbs(*dirFlag)||filepath.Clean(*dirFlag)!=*dirFlag) {fmt.Fprintln(diagnostic,"--model-dir must be an absolute, clean directory path");return 2}
 cacheFlags:=given["cache-quota-gib"]||given["cache-reserve-gib"]
 // --yes answers the questions itself; it must not turn off the saved consent that later steps use.
 if *interactive || (*cacheSettings&&!*yes) {ctx=context.WithValue(ctx,continuationInteractive{},true)}
 exe,err:=os.Executable();if err!=nil{fmt.Fprintln(diagnostic,"cannot locate the verified installer");return 1}
 if os.Geteuid()!=0{fmt.Fprintf(diagnostic,"Start guided setup with:\n  sudo %q\n",exe);return 1}
 ui:=guidedUI{in:bufio.NewReader(input),out:out,ctx:ctx,yes:*yes,cacheQuota:*quotaFlag,cacheReserve:*reserveFlag}
 fmt.Fprintln(out,"Hypermesh host setup\nThis guide checks your host, installs required software, connects your account and tests readiness. You approve machine changes before they run.")
 if *yes {fmt.Fprintln(out,"--yes: you consent once to this whole setup. Questions about the network, host name, model storage, headless mode, system changes, model tests, and continuing automatically (including starting selling once the host is certified) are answered with this host's defaults or your flags. Anything else, and any setting that is wrong or does not fit, still stops here with a message.")}
 dir:=filepath.Join(defaultSetupState,"guided")
 unlock,err:=lockSetupState(dir);if err!=nil{fmt.Fprintln(diagnostic,err);return 1};defer unlock()
 statePath:=filepath.Join(dir,"session.json")
 var s guidedState
 machine,_,err:=rebootIdentity("");if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 if _,e:=os.Lstat(statePath);e==nil{
  err=guidedRead(statePath,&s,true)
  if err==nil&&(s.Schema!=1||s.Machine!=machine||s.Stage<0||s.Stage>7){err=fmt.Errorf("saved installation state is inconsistent; preserve it for recovery")}
  // Refuse conflicting settings before any recovery or upgrade work.
  if err==nil{err=resumedSettingsMatch(s,*networkFlag,*hostFlag,*dirFlag,headlessSet,*headlessFlag)}
  if err==nil&&cacheFlags&&!*yes{ctx=context.WithValue(ctx,continuationInteractive{},true);ui.ctx=ctx}
  // After approval, a verified root-owned copy carries on, not this downloaded file.
  if err==nil&&!*background {
   target,e:=resumeTarget(s)
   if e!=nil{err=e} else if target!="" {
    fmt.Fprintln(diagnostic,"This installer file differs from the verified copy saved when setup was approved. Continuing with the verified root-owned copy.")
    unlock();return runVerifiedCopy(ctx,target,args,input,out,diagnostic)
   }
  }
  if err==nil && *background && s.ContinuationSHA!="" {
   target,e:=continuationTarget(s);if e!=nil{fmt.Fprintln(diagnostic,e);return 1}
   if target!="" {unlock();return runContinuation(ctx,target,input,out,diagnostic)}
  }
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
    if s.Stage>=3 && s.Stage<=7 {
     return upgradeGuidedInstalled(ctx,dir,statePath,*receipt,&s,ui,func(p agentInstallPlan)error{
      if s.Stage==7 {
       if e:=verifyInstalledForActivation(ctx,"",p,true);e!=nil{return e}
       return checkAgentUnitPaths(ctx,setupSource{},"",agentPayloads(p,nil))
      }
      return verifyRegistrationService(ctx,p)
     },func(next *guidedState)error{return installContinuation(ctx,next)})
    }
    return upgradeGuidedPrerequisites(ctx,dir,statePath,*receipt,&s,ui,
     func()error{return guardRebootHost(ctx,"",defaultSetupState,rebootSetupSource())},func(next *guidedState)error{return installContinuation(ctx,next)})
   }()
  }
  if err==nil{err=validateGuidedDownload(s.Download)}
  if err==nil{fmt.Fprintf(out,"Resuming setup for %s on %s. Completed steps will be checked by the installation gates.\n",s.Host,s.Network)}
 }else if !os.IsNotExist(e){err=e}else{
  if *background {fmt.Fprintln(diagnostic,"no saved onboarding session");return 1}
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
  if v,ok:=ui.preset("network",*networkFlag);ok{s.Network=v}else{s.Network,err=ui.ask("Account network: production or test (separate from the "+s.Download.Channel+" software channel)","production")}
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
  if v,ok:=ui.preset("host-name",*hostFlag);ok&&err==nil{s.Host=v}else if err==nil{s.Host,err=ui.ask("Name shown in your host dashboard",hostname)}
  if err==nil&&!agentNameRE.MatchString(s.Host){err=fmt.Errorf("host name must contain 1-63 letters, digits, dots, underscores or hyphens")}
  if v,ok:=ui.preset("model-dir",*dirFlag);ok&&err==nil{s.Directory=v}else if err==nil{s.Directory,err=ui.ask("Existing directory for model storage","/var/lib/hypermesh/models")}
  if err==nil&&(!filepath.IsAbs(s.Directory)||filepath.Clean(s.Directory)!=s.Directory){err=fmt.Errorf("choose an absolute existing model directory")}
  if err==nil {
   info,e:=os.Stat(s.Directory)
   if os.IsNotExist(e) {
    fmt.Fprintln(out,"The model directory does not exist. Its parent filesystem must be the disk you intend to use.")
    if err=ui.confirm("Create "+s.Directory+" without changing partitions or mounts?");err==nil {err=agentDirectory(s.Directory,true)}
   } else if e!=nil||!info.IsDir(){err=fmt.Errorf("model storage must be an accessible directory")}
  }
  if err==nil{
   var answer string
   if headlessSet {answer="no";if *headlessFlag{answer="yes"};fmt.Fprintf(out,"Using --headless=%v\n",*headlessFlag)} else {answer,err=ui.ask("Use headless mode (disable graphical login after reboot)? yes/no","yes")}
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
 if *background {
  status,e:=readContinuation(dir);if e!=nil{fmt.Fprintln(diagnostic,e);return 1}
  if status.Stage==s.Stage&&time.Now().Before(status.NextAttempt){return 0}
 }
 if *background&&!s.Autonomous {fmt.Fprintln(diagnostic,"no saved continuation consent");return 1}
 if s.Autonomous {
  if !*background {if err=installContinuation(ctx,&s);err==nil{err=writeGuided(statePath,s)};if err!=nil{fmt.Fprintln(diagnostic,err);return 1}}
  ctx=withContinuationConsent(ctx);ui.ctx=ctx
 }
 if err=guidedCachePolicy(s.Directory,ui,*cacheSettings||cacheFlags);err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 planDir,err:=guidedPlanDirectory(dir,s)
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 if !*background && s.Stage>=4 && !s.Autonomous {
  if err=ui.confirm(promptContinue);err!=nil{fmt.Fprintln(diagnostic,err);return 1}
  s.Autonomous=true
  s.SellConsent=true
  if err=installContinuation(ctx,&s);err==nil{err=writeGuided(statePath,s)}
  if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
  ctx=withContinuationConsent(ctx);ui.ctx=ctx
 }
 flow:=guidedFlow{dir:planDir,state:&s,ui:ui,diagnostic:diagnostic,run:guidedCommand,save:func()error{return writeGuided(statePath,s)},
  prepareContinuation:func(ctx context.Context,p SetupPlan)error{
   if !s.Autonomous {
    if e:=ui.confirm(promptEnroll);e!=nil{return e}
    s.SellConsent=true
   }
   if e:=earlyEnrollment(ctx,planDir,&s,p,diagnostic);e!=nil{return e}
   s.Autonomous=true
   if e:=installContinuation(ctx,&s);e!=nil{return e}
   if e:=writeGuided(statePath,s);e!=nil{return e}
   fmt.Fprintln(ui.out,"A verified, root-owned copy of this installer is saved. To resume later from any directory, run: sudo hypermesh-host")
   return nil
  },
  adoptEnrollment:func(ctx context.Context,p agentInstallPlan)(bool,error){return adoptEarlyEnrollment(ctx,planDir,p,s)},
  qualification:func(ctx context.Context,path string)(bool,error){u:=ui;u.ctx=ctx;return guidedQualification(ctx,path,u,diagnostic)},
  wireguardMode:func()(string,string,error){return detectGuidedWireguard(ctx,ui.confirm)},
  repairWireguard:func(ctx context.Context,dir string,p agentInstallPlan)(agentInstallPlan,error){
   unlockState,e:=lockSetupState(defaultSetupState);if e!=nil{return p,e};defer unlockState()
   u:=ui;u.ctx=ctx;return repairGuidedWireguard(ctx,dir,p,u)
  },
  rebootNeeded:func(p SetupPlan)(bool,error){
   needed,e:=liveRebootRequired("");if e!=nil{return false,e}
   headless,e:=headlessRebootNeeded(ctx,p,rebootSetupSource());return needed||headless,e
  },boot:func()(string,string,error){return rebootIdentity("")}}
 err=flow.finish(ctx)
 if s.Autonomous {
  if e:=recordContinuation(dir,s,err);e!=nil{fmt.Fprintln(diagnostic,e);return 1}
  if s.Stage==7&&err==nil {_,_=(setupSource{}).output(ctx,"systemctl","disable","--now","hypermesh-onboarding.timer")}
 }
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 if s.Autonomous&&s.Stage<7 {fmt.Fprintln(out,"Setup continues automatically in hypermesh-onboarding.service. You may disconnect; progress is saved across reboots.")}
 if s.Stage==7{fmt.Fprintf(out,"Setup completed for %s. Manage this host with sudo hypermesh-host sell status, sell pause or sell resume.\n",s.Host)}
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
func upgradeGuidedPrerequisites(ctx context.Context,dir,statePath,receipt string,s *guidedState,ui guidedUI,guard func()error,prepare ...func(*guidedState)error)error{
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
 if next.Autonomous {next.ContinuationSHA="";if len(prepare)==0{return fmt.Errorf("continuation upgrade preparation is required")};if err=prepare[0](&next);err!=nil{return err}}
 if err=writeGuided(statePath,next);err!=nil{return err}
 *s=next
 return nil
}

func upgradeGuidedInstalled(ctx context.Context,dir,statePath,receipt string,s *guidedState,ui guidedUI,verify func(agentInstallPlan)error,prepare ...func(*guidedState)error)error{
 if s.Stage<3 || s.Stage>7 {return fmt.Errorf("installed continuation requires an installed host")}
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
 if err=ui.confirm("Use this newer installer to continue onboarding of the verified installation? Existing files and approvals will be preserved.");err!=nil{return err}
 if err=ctx.Err();err!=nil{return err}
 if err=verify(p);err!=nil{return err}
 next:=*s;next.Download=download
 if next.Autonomous {next.ContinuationSHA="";if len(prepare)==0{return fmt.Errorf("continuation upgrade preparation is required")};if err=prepare[0](&next);err!=nil{return err}}
 if err=writeGuided(statePath,next);err!=nil{return err}
 *s=next
 return nil
}

// A saved session keeps the settings it was created with. A setting flag that
// disagrees would otherwise be ignored, so say so instead.
func resumedSettingsMatch(s guidedState,network,host,dir string,headlessSet,headless bool)error {
 var differs []string
 if network!=""&&network!=s.Network{differs=append(differs,"--network")}
 if host!=""&&host!=s.Host{differs=append(differs,"--host-name")}
 if dir!=""&&dir!=s.Directory{differs=append(differs,"--model-dir")}
 if headlessSet&&headless!=s.Headless{differs=append(differs,"--headless")}
 if len(differs)>0 {
  return fmt.Errorf("setup was already started with different settings, so %s cannot change it. Run the installer again without those flags to resume it",strings.Join(differs," and "))
 }
 return nil
}
