//go:build linux

package installer

import (
 "context"
 "crypto/rand"
 "crypto/sha256"
 "encoding/base64"
 "encoding/hex"
 "encoding/json"
 "errors"
 "flag"
 "fmt"
 "io"
 "os"
 "path/filepath"
 "strings"
 "syscall"
 "time"

 "github.com/FyberLabs/hypermesh-host/internal/agent"
 "github.com/FyberLabs/hypermesh-host/internal/deviceauth"
 "github.com/FyberLabs/hypermesh-host/internal/hostregistration"
 "github.com/FyberLabs/hypermesh-host/internal/hardware"
 "github.com/FyberLabs/hypermesh-host/internal/tunnel"
)

// This private journal is the recovery authority. Never encode it to diagnostics.
type registrationJournal struct {
 Schema int `json:"schema"`
 PlanSHA256 string `json:"plan_sha256"`
 Issuer string `json:"issuer"`
 Dashboard string `json:"dashboard"`
 Owner string `json:"owner"`
 Request hostregistration.Request `json:"request"`
 PrivateKey string `json:"private_key"`
 DeviceSecret string `json:"device_secret"`
 DeviceID string `json:"device_id"`
 State string `json:"state"`
}
func (registrationJournal) String()string{return "[redacted private registration journal]"}
func (registrationJournal) GoString()string{return "[redacted private registration journal]"}
type registrationStatus struct {
 PlanSHA256 string `json:"plan_sha256"`
 RequestID string `json:"request_id"`
 HostName string `json:"host_name"`
 DeviceID string `json:"device_id,omitempty"`
 State string `json:"state"`
 NextAction string `json:"next_action"`
}
func publicRegistration(j registrationJournal)registrationStatus {
 next:="Rerun register-agent with the same plan and login to recover; do not delete the private journal."
 switch j.State {
 case "expired":next="Rerun the same register-agent command to authenticate again and renew browser approval automatically; previous request history is preserved. If using guided setup, rerun the same installer."
 case "denied":next="Approval ended. Rerun with --restart-approval to archive this request and require new browser approval with fresh keys."
 case "retired":next="Host was retired. Do not restore its identity; use a fresh reinstall workflow."
 case "registered-inactive":next="Host registered and paused. Compatibility, certification and service activation remain pending."
 }
 return registrationStatus{j.PlanSHA256,j.Request.RequestID,j.Request.HostName,j.DeviceID,j.State,next}
}
func registrationPath(state,sha string)string{return filepath.Join(state,"registration-"+sha+".json")}
func saveRegistration(path string,j registrationJournal)error {
 if err:=secureSetupDirectory(filepath.Dir(path),false);err!=nil{return err}
 if _,err:=os.Lstat(path);err==nil {
  if _,err=readSetupRegular(path,true);err!=nil{return fmt.Errorf("unsafe existing registration journal")}
 } else if !os.IsNotExist(err){return fmt.Errorf("cannot inspect registration journal")}
 raw,err:=json.Marshal(j);if err!=nil{return fmt.Errorf("cannot encode private journal")}
 f,err:=os.CreateTemp(filepath.Dir(path),".registration-");if err!=nil{return fmt.Errorf("cannot stage private journal")}
 defer os.Remove(f.Name())
 if _,err=f.Write(raw);err!=nil{f.Close();return fmt.Errorf("cannot write private journal")}
 if err=f.Sync();err!=nil{f.Close();return fmt.Errorf("cannot sync private journal")}
 if err=f.Close();err!=nil{return fmt.Errorf("cannot close private journal")}
 if err=os.Rename(f.Name(),path);err!=nil{return fmt.Errorf("cannot publish private journal")}
 d,err:=os.Open(filepath.Dir(path));if err!=nil{return fmt.Errorf("cannot open journal directory")};defer d.Close()
 if d.Sync()!=nil{return fmt.Errorf("cannot sync journal directory")}
 return nil
}
func newRegistration(p agentInstallPlan,issuer,dashboard,class string,fresh Plan)(registrationJournal,error) {
 var j registrationJournal
 // A candidate class is not certification. Until generic class admission is
 // supported by the plane, accept only a measured match for existing classes.
 if err:=registrationClass(class,fresh);err!=nil{return j,err}
 private,public,err:=tunnel.GenerateKeypair();if err!=nil{return j,fmt.Errorf("cannot generate host key")}
 id:=make([]byte,16);secret:=make([]byte,24)
 if _,err=rand.Read(id);err!=nil{return j,fmt.Errorf("cannot generate registration request")}
 if _,err=rand.Read(secret);err!=nil{return j,fmt.Errorf("cannot generate device credential")}
 id[6]=(id[6]&15)|64;id[8]=(id[8]&63)|128
 requestID:=fmt.Sprintf("%x-%x-%x-%x-%x",id[:4],id[4:6],id[6:8],id[8:10],id[10:])
 credential:="hm_dev_"+base64.RawURLEncoding.EncodeToString(secret)
 h:=fresh.Hardware
 cpu,gpu:=strings.TrimSpace(h.Facts.Processor),strings.TrimSpace(h.Facts.GPU)
 if cpu=="" {cpu=h.Facts.Arch+" processor"}
 if gpu=="" {gpu=h.Backend+" accelerator"}
 modelMemory:=fresh.Resources.Admission.AcceleratorBytes
 if fresh.Resources.Resources.UMA && fresh.Resources.Resources.UMAAllocationBytes==0 {
  resources:=fresh.Resources.Resources
  if !CheckResources(resources).Eligible {return j,fmt.Errorf("registration requires more than 12 GiB currently free in system and UMA runtime memory")}
  // Registration telemetry is a current observation, never a reserved budget.
  modelMemory=resources.AvailableSystemBytes
  if resources.UMARuntime.FreeBytes<modelMemory {modelMemory=resources.UMARuntime.FreeBytes}
 }
 report:=hostregistration.Hardware{Profile:p.Setup.Profile.ID,Architecture:normalizedSetupArch(h.Facts.Arch),
 CPU:cpu,GPU:gpu,SystemMemoryBytes:fresh.Resources.Resources.SystemBytes,ModelMemoryBytes:modelMemory}
 j=registrationJournal{Schema:1,PlanSHA256:p.SHA256,Issuer:issuer,Dashboard:dashboard,PrivateKey:private,DeviceSecret:credential,State:"prepared",
 Request:hostregistration.Request{RequestID:requestID,HostName:p.HostName,ClassID:class,Hardware:report,WGPublicKey:public,DeviceSecretHash:digestSetupBytes([]byte(credential)),CredentialPrefix:credential[7:15]}}
 return j,j.Request.Validate()
}
func registrationClass(class string,p Plan)error {
 f:=p.Hardware.Facts
 model:=strings.ToLower(f.Model)
 memory:=p.Resources.Resources.SystemBytes
 switch class {
 case "agx-large":
  if f.Tegra && p.Hardware.Backend=="cuda" && strings.Contains(model,"agx orin") && memory>=56*GiB && memory<=66*GiB{return nil}
 case "nx-volume":
  if f.Tegra && p.Hardware.Backend=="cuda" && strings.Contains(model,"orin nx") && memory>=14*GiB && memory<=17*GiB{return nil}
 }
 return fmt.Errorf("requested class does not have a supported measured hardware match; generic/other class admission remains pending")
}
func validateRegistration(j registrationJournal,p agentInstallPlan,issuer,dashboard,class string)error {
 pub,err:=tunnel.PublicFromPrivate(j.PrivateKey)
 secret,secretErr:=base64.RawURLEncoding.DecodeString(strings.TrimPrefix(j.DeviceSecret,"hm_dev_"))
 if j.Schema!=1||j.PlanSHA256!=p.SHA256||j.Issuer!=issuer||j.Dashboard!=dashboard||j.Request.HostName!=p.HostName||j.Request.ClassID!=class||
 err!=nil||pub!=j.Request.WGPublicKey||!strings.HasPrefix(j.DeviceSecret,"hm_dev_")||secretErr!=nil||len(secret)!=24||
 "hm_dev_"+base64.RawURLEncoding.EncodeToString(secret)!=j.DeviceSecret||
 digestSetupBytes([]byte(j.DeviceSecret))!=j.Request.DeviceSecretHash||len(j.DeviceSecret)<15||j.DeviceSecret[7:15]!=j.Request.CredentialPrefix {
 return fmt.Errorf("registration journal does not match; preserve it for recovery")
 }
 if err=j.Request.Validate();err!=nil{return err}
 switch j.State {case "prepared","pending","approved","denied","expired":
 if j.DeviceID!=""{return fmt.Errorf("unexpected journal device identity")}
 case "completed","registered-inactive","retired":
 if !hostregistration.UUID.MatchString(j.DeviceID){return fmt.Errorf("invalid journal device identity")}
 default:return fmt.Errorf("invalid registration journal state")}
 if j.State!="prepared"&&j.Owner==""{return fmt.Errorf("registration journal has no owner")}
 return nil
}
// Verify every installed payload before authentication. Stream the executable
// rather than using the 2 MiB JSON reader; refuse missing, linked or altered files.
func verifyRegistrationInstallation(ctx context.Context,root string,p agentInstallPlan)error {
 return verifyInstalledForActivation(ctx,root,p,false)
}
func verifyInstalledForActivation(ctx context.Context,root string,p agentInstallPlan,ownMarker bool)error {
 if err:=secureSetupDirectory(agentDest(root,"/var/lib/hypermesh"),false);err!=nil{return err}
 if _,err:=os.Lstat(agentDest(root,"/var/lib/hypermesh/activation-approved"));!ownMarker&&!os.IsNotExist(err){return fmt.Errorf("registration requires an inactive first installation")}
 var installed agentInstallJournal
 raw,err:=readSetupRegular(agentDest(root,filepath.Join(defaultSetupState,"agent-"+p.SHA256+".json")),true)
 if err==nil{err=decodeSetupJSON(raw,&installed)}
 if err!=nil||installed.Schema!=1||installed.PlanSHA256!=p.SHA256||installed.State!="installed-awaiting-enrollment"||!installed.Started {return fmt.Errorf("complete install-agent before registration")}
 expected:=agentPayloads(p,nil)
 if len(expected)!=len(p.Files)||len(installed.Completed)!=len(p.Files){return fmt.Errorf("incomplete installed manifest")}
 for i,f:=range p.Files {
  if f.Path!=expected[i].Path||f.Mode!=expected[i].Mode||!setupDigestRE.MatchString(f.SHA256)||
   (i!=0&&f.SHA256!=expected[i].SHA256)||installed.Completed[i]!=f.Path{return fmt.Errorf("invalid installed manifest")}
  if err=ctx.Err();err!=nil{return fmt.Errorf("installation verification interrupted")}
  path:=agentDest(root,f.Path)
  if err=agentDirectory(filepath.Dir(path),false);err!=nil{return err}
  info,e:=os.Lstat(path)
  if e!=nil||!info.Mode().IsRegular()||uint32(info.Mode().Perm())!=f.Mode{return fmt.Errorf("installed payload missing or unsafe")}
  st,ok:=info.Sys().(*syscall.Stat_t)
  if !ok||st.Uid!=uint32(os.Geteuid())||st.Nlink!=1{return fmt.Errorf("installed payload ownership or links unsafe")}
  file,e:=os.OpenFile(path,os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK,0)
  if e!=nil{return fmt.Errorf("cannot open installed payload")}
  after,e:=file.Stat()
  if e!=nil||!os.SameFile(info,after){file.Close();return fmt.Errorf("installed payload changed")}
  hash:=sha256.New();_,e=io.Copy(hash,io.LimitReader(file,256<<20));file.Close()
  got:=hex.EncodeToString(hash.Sum(nil))
  if e!=nil||info.Size()>256<<20{return fmt.Errorf("installed payload differs from approved plan")}
  if got!=f.SHA256 {
   if i!=0||verifyInstalledUpdate(root,p,got)!=nil{return fmt.Errorf("installed payload differs and has no matching trusted signed update evidence")}
  }
 }
 return nil
}
func registrationKeyDir(j registrationJournal)string{return "/var/lib/hypermesh/registration-keys/"+j.Request.RequestID}
func registrationIdentityFiles(j registrationJournal)[]agentPayload {
 state:=agent.State{WGKeyDir:registrationKeyDir(j),DeviceID:j.DeviceID,CredentialPrefix:j.Request.CredentialPrefix,DeviceSecret:j.DeviceSecret}
 raw,_:=json.Marshal(state)
 key:=[]byte(j.PrivateKey+"\n")
 return []agentPayload{{agentFile{registrationKeyDir(j)+"/hypermesh.key",digestSetupBytes(key),0o600},key},
 {agentFile{"/var/lib/hypermesh/device.json",digestSetupBytes(raw),0o600},raw}}
}
func checkRegistrationIdentity(root string,j registrationJournal,require bool)error {
 for _,f:=range registrationIdentityFiles(j) {
  // Validate all existing ancestors even before a path has been published.
  parent:=filepath.Dir(agentDest(root,f.Path))
  for {if _,err:=os.Lstat(parent);!os.IsNotExist(err){break};parent=filepath.Dir(parent)}
  if err:=agentDirectory(parent,false);err!=nil{return err}
  exists,err:=agentFileMatches(agentDest(root,f.Path),f)
  if err!=nil{return fmt.Errorf("existing host identity conflicts; preserve files for recovery")}
  if exists&&j.DeviceID==""{return fmt.Errorf("host identity exists before journaled completion")}
  if require&&!exists{return fmt.Errorf("completed host identity is missing; repair is required")}
 }
 return nil
}
func publishRegistration(root string,j registrationJournal)error {
 if !hostregistration.UUID.MatchString(j.DeviceID){return fmt.Errorf("server-assigned device ID required")}
 if err:=checkRegistrationIdentity(root,j,false);err!=nil{return err}
 for _,path:=range []string{"/var/lib/hypermesh/registration-keys",registrationKeyDir(j)} {
  if err:=secureSetupDirectory(agentDest(root,path),true);err!=nil{return err}
 }
 for _,f:=range registrationIdentityFiles(j) {
  path:=agentDest(root,f.Path)
  exists,err:=agentFileMatches(path,f);if err!=nil{return err}
  if !exists {if err=writeAgentFile(path,f);err!=nil{return fmt.Errorf("cannot publish host identity; rerun to recover")}}
 }
 return nil
}
type registrationRemote interface {
 Create(context.Context,hostregistration.Request)(hostregistration.Status,error)
 Get(context.Context,hostregistration.Request)(hostregistration.Status,error)
 Complete(context.Context,hostregistration.Request)(hostregistration.Status,error)
}
type preflightRegistrationRemote struct {
 registrationRemote
 check func(context.Context)error
}
func (r preflightRegistrationRemote) Complete(ctx context.Context,request hostregistration.Request)(hostregistration.Status,error) {
 if err:=r.check(ctx);err!=nil{return hostregistration.Status{},err}
 return r.registrationRemote.Complete(ctx,request)
}
func registrationPreflight(ctx context.Context,p agentInstallPlan,class string)(Plan,error) {
 var fresh Plan
 deps:=setupApplyDeps{Source:setupSource{},Preflight:func(ctx context.Context,plan SetupPlan)(Plan,error){var err error;fresh,err=freshSetupPreflight(ctx,plan);return fresh,err}}
 remaining,target,err:=validateSetupHost(ctx,p.Setup,deps)
 if err==nil&&(len(remaining)>0||(p.Setup.HeadlessRequested&&target!="multi-user.target")){err=fmt.Errorf("approved prerequisites are incomplete")}
 if err==nil{err=registrationClass(class,fresh)}
 return fresh,err
}
func verifyRegistrationService(ctx context.Context,p agentInstallPlan)error {
 if err:=verifyRegistrationInstallation(ctx,"",p);err!=nil{return err}
 if err:=checkAgentUnitPaths(ctx,setupSource{},"",agentPayloads(p,nil));err!=nil{return err}
 for _,name:=range []string{"hypermesh-agent.service","hypermesh-wg.service"} {
  raw,err:=(setupSource{}).output(ctx,"systemctl","--system","show",name,"-p","ActiveState","--value")
  state:=strings.TrimSpace(string(raw))
  if err!=nil||(state!="inactive"&&state!="failed"){return fmt.Errorf("host services must be inactive before registration")}
 }
 return nil
}
func waitRegistration(ctx context.Context,d time.Duration)error {
 timer:=time.NewTimer(d);defer timer.Stop()
 select {case <-ctx.Done():return fmt.Errorf("approval interrupted; rerun to recover");case <-timer.C:return nil}
}
var errRegistrationExpired = errors.New("registration approval expired")

// Renew only a server-confirmed expiry, once per invocation. Denial and retirement
// remain explicit decisions; transport and journal failures never start over.
func recoverRegistrationExpiry(run func() error, renew func() error, display io.Writer) error {
 err := run()
 if !errors.Is(err, errRegistrationExpired) { return err }
 fmt.Fprintln(display,"The browser approval timed out. Preparing a new approval link; the previous request will be archived.")
 if err = renew(); err != nil { return err }
 return run()
}

func finishRegistration(ctx context.Context,j *registrationJournal,remote registrationRemote,save func(registrationJournal)error,publish func(registrationJournal)error,display io.Writer,wait func(context.Context,time.Duration)error)error {
 // Retrying Create is idempotent, including after a lost completion response.
 s,err:=remote.Create(ctx,j.Request);if err!=nil{return err}
 printed:=false
 for {
  if j.DeviceID!="" && (s.DeviceID==nil||*s.DeviceID!=j.DeviceID){return fmt.Errorf("server device identity changed; preserve local state")}
  j.State=s.State
  if s.DeviceID!=nil{j.DeviceID=*s.DeviceID}
  if err=save(*j);err!=nil{return err}
  switch s.State {
  case "completed":
   if err=publish(*j);err!=nil{return err}
   j.State="registered-inactive";return save(*j)
  case "expired":return errRegistrationExpired
  case "denied","retired":return fmt.Errorf("registration %s; identity retained and service inactive",s.State)
  case "approved":s,err=remote.Complete(ctx,j.Request)
  case "pending":
   if !printed {fmt.Fprintln(display,"Approve this host installation in your dashboard:",j.Dashboard+s.ApprovalPath);printed=true}
   if err=wait(ctx,5*time.Second);err!=nil{return err}
   s,err=remote.Get(ctx,j.Request)
  default:return fmt.Errorf("unknown registration state")
  }
  if err!=nil{return err}
 }
}
// Restart requires a terminal server decision, fresh preflight and explicit
// operator intent. Archive the old private request before replacing its journal.
func restartRegistration(j registrationJournal,state hostregistration.Status,p agentInstallPlan,fresh Plan,archive func(registrationJournal)error,save func(registrationJournal)error)(registrationJournal,error) {
 if (state.State!="expired"&&state.State!="denied")||state.DeviceID!=nil||j.DeviceID!="" {
  return j,fmt.Errorf("only an expired or denied approval can restart; rerun without --restart-approval to resume the current request")
 }
 if j.Owner==""{return j,fmt.Errorf("verified owner required before restarting approval")}
 old:=j;old.State=state.State
 next,err:=newRegistration(p,j.Issuer,j.Dashboard,j.Request.ClassID,fresh)
 if err!=nil{return j,err}
 next.Owner=j.Owner
 if err=archive(old);err!=nil{return j,err}
 if err=save(next);err!=nil{return j,err}
 return next,nil
}
func archiveRegistration(state string,j registrationJournal)error {
 path:=filepath.Join(state,"registration-history-"+j.Request.RequestID+".json")
 raw,err:=json.Marshal(j);if err!=nil{return fmt.Errorf("cannot encode private registration history")}
 payload:=agentPayload{agentFile{path,digestSetupBytes(raw),0o600},raw}
 exists,err:=agentFileMatches(path,payload)
 if err!=nil{return fmt.Errorf("registration history differs; preserve files for recovery")}
 if exists{return nil}
 if err=writeAgentFile(path,payload);err!=nil{return fmt.Errorf("cannot preserve private registration history")}
 return nil
}
func RunRegisterAgent(ctx context.Context,args []string,out,diagnostic io.Writer)int {
 flags:=flag.NewFlagSet("register-agent",flag.ContinueOnError);flags.SetOutput(diagnostic)
 path:=flags.String("plan","","installed, reviewed agent plan")
 approved:=flags.String("approve-plan","","exact agent plan_sha256")
 issuer:=flags.String("issuer","","trusted HTTPS Keycloak realm issuer")
 dashboard:=flags.String("dashboard-url","","trusted HTTPS dashboard origin")
 class:=flags.String("class-id","","candidate platform class; checked against measured hardware")
 restart:=flags.Bool("restart-approval",false,"explicitly replace a denied/expired approval with fresh keys; preserves private history")
 if err:=flags.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
 if flags.NArg()!=0||*path==""||!setupDigestRE.MatchString(*approved)||validateEndpoint(*issuer)!=nil||!hostregistration.Origin(*dashboard)||*class==""{fmt.Fprintln(diagnostic,"require --plan, --approve-plan, --issuer, --dashboard-url and --class-id");return 2}
 *dashboard=strings.TrimRight(*dashboard,"/")
 if os.Geteuid()!=0{fmt.Fprintln(diagnostic,"register-agent requires root");return 1}
 var p agentInstallPlan
 raw,err:=readSetupRegular(*path,false)
 if err==nil{err=decodeSetupJSON(raw,&p)}
 if err==nil&&(p.SHA256!=*approved||agentPlanDigest(p)!=*approved){err=fmt.Errorf("agent approval digest does not match")}
 if err==nil{err=validateInstalledAgentInputs(p)}
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 remote,err:=hostregistration.New(p.ControlPlane,p.TenantID)
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 unlock,err:=lockSetupState(defaultSetupState);if err!=nil{fmt.Fprintln(diagnostic,err);return 1};defer unlock()
 ctx,cancel:=context.WithTimeout(ctx,25*time.Minute);defer cancel()
 err=verifyRegistrationService(ctx,p)
 if err==nil {
  now:=hardware.Detect(ctx)
  if !hardware.MatchesReportedHardware(p.Setup.Preflight.Hardware,now) {err=fmt.Errorf("hardware changed from approved installation; preserve identity and review preflight")}
 }
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 journalPath:=registrationPath(defaultSetupState,p.SHA256)
 var j registrationJournal
 if _,statErr:=os.Lstat(journalPath);statErr==nil {
  raw,err=readSetupRegular(journalPath,true)
  if err==nil{err=decodeSetupJSON(raw,&j)}
  if err==nil{err=validateRegistration(j,p,*issuer,*dashboard,*class)}
 } else if !os.IsNotExist(statErr){err=fmt.Errorf("cannot inspect registration journal")} else {
  if _,e:=os.Lstat("/var/lib/hypermesh/device.json");!os.IsNotExist(e){err=fmt.Errorf("existing host identity requires repair or reinstall")}
  var fresh Plan
  if err==nil{fresh,err=registrationPreflight(ctx,p,*class)}
  if err==nil{j,err=newRegistration(p,*issuer,*dashboard,*class,fresh)}
  if err==nil{err=saveRegistration(journalPath,j)}
 }
 if err==nil{err=checkRegistrationIdentity("",j,j.State=="registered-inactive")}
 if err!=nil{fmt.Fprintln(diagnostic,"registration state validation failed; preserve existing files:",err);return 1}
 save:=func(value registrationJournal)error{return saveRegistration(journalPath,value)}
 token,err:=deviceauth.Authorize(ctx,*issuer,diagnostic)
 if err==nil {err=remote.Login(ctx,token.Bearer(),func(owner string)error{
  if j.Owner!=""&&j.Owner!=owner{return fmt.Errorf("login belongs to a different owner; use the original account")}
  if j.Owner==""{j.Owner=owner;return save(j)}
  return nil
 })}
 if err==nil&&*restart {
  var status hostregistration.Status
  status,err=remote.Get(ctx,j.Request)
  if err==nil {
   var fresh Plan
   fresh,err=registrationPreflight(ctx,p,*class)
   if err==nil {err=checkRegistrationIdentity("",j,false)}
   if err==nil {
    j,err=restartRegistration(j,status,p,fresh,func(old registrationJournal)error{return archiveRegistration(defaultSetupState,old)},save)
   }
  }
 }
 if err==nil{
  checked:=preflightRegistrationRemote{remote,func(ctx context.Context)error{
   if err:=verifyRegistrationService(ctx,p);err!=nil{return err}
   _,err:=registrationPreflight(ctx,p,*class);return err
  }}
  err=recoverRegistrationExpiry(func()error{return finishRegistration(ctx,&j,checked,save,func(value registrationJournal)error{
   if err:=verifyRegistrationService(ctx,p);err!=nil{return err}
   return publishRegistration("",value)
  },diagnostic,waitRegistration)},func()error{
   status,e:=remote.Get(ctx,j.Request);if e!=nil{return e}
   if status.State!="expired"{return fmt.Errorf("registration changed while renewing approval; preserve local state")}
   if e=verifyRegistrationService(ctx,p);e!=nil{return e}
   fresh,e:=registrationPreflight(ctx,p,*class);if e!=nil{return e}
   if e=checkRegistrationIdentity("",j,false);e!=nil{return e}
   j,e=restartRegistration(j,status,p,fresh,func(old registrationJournal)error{return archiveRegistration(defaultSetupState,old)},save)
   return e
  },diagnostic)
 }
 if encodeErr:=json.NewEncoder(out).Encode(publicRegistration(j));encodeErr!=nil{return 1}
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 return 0
}
func RunRegistrationStatus(_ context.Context,args []string,out,diagnostic io.Writer)int {
 flags:=flag.NewFlagSet("registration-status",flag.ContinueOnError);flags.SetOutput(diagnostic)
 digest:=flags.String("plan-sha","","agent plan_sha256")
 if err:=flags.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
 if flags.NArg()!=0||!setupDigestRE.MatchString(*digest){fmt.Fprintln(diagnostic,"require --plan-sha SHA256");return 2}
 if err:=secureSetupDirectory(defaultSetupState,false);err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 raw,err:=readSetupRegular(registrationPath(defaultSetupState,*digest),true)
 var j registrationJournal
 if err==nil{err=decodeSetupJSON(raw,&j)}
 if err!=nil||j.Schema!=1||j.PlanSHA256!=*digest||j.Request.Validate()!=nil{fmt.Fprintln(diagnostic,"registration journal unavailable or invalid");return 1}
 if json.NewEncoder(out).Encode(publicRegistration(j))!=nil{return 1}
 return 0
}
