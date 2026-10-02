//go:build linux
package installer
import(
 "context"
 "encoding/json"
 "flag"
 "fmt"
 "io"
 "os"
 "path/filepath"
 "runtime"
 "strings"
 "time"
 "github.com/FyberLabs/hypermesh-host/internal/buildinfo"
 "github.com/FyberLabs/hypermesh-host/internal/update"
)
type installerUpdatePlan struct{
 Schema int `json:"schema"`
 SHA256 string `json:"plan_sha256"`
 Destination string `json:"destination"`
 Before recoveryFile `json:"before"`
 ReleaseURL string `json:"release_url"`
 Channel string `json:"channel"`
 Arch string `json:"arch"`
 PublicKey string `json:"public_key"`
 Fingerprints []string `json:"fingerprints"`
 Release releaseMetadata `json:"release"`
 PayloadSHA256 string `json:"payload_sha256"`
}
func installerUpdateDigest(p installerUpdatePlan)string{p.SHA256="";raw,_:=json.Marshal(p);return digestSetupBytes(raw)}
func updateInstallerOptions(p installerUpdatePlan,output string)downloadOptions{return downloadOptions{BaseURL:p.ReleaseURL,Channel:p.Channel,Arch:p.Arch,Output:output,Artifact:"deploy",MinimumBuild:p.Release.Build,Key:[]byte(p.PublicKey),Fingerprints:p.Fingerprints}}
func validateInstallerUpdate(p installerUpdatePlan,sha string)error{
 if p.Schema!=1||p.SHA256!=sha||!setupDigestRE.MatchString(sha)||installerUpdateDigest(p)!=sha||!filepath.IsAbs(p.Destination)||filepath.Clean(p.Destination)!=p.Destination||
 p.Before.Path!=p.Destination||!p.Before.Present||!setupDigestRE.MatchString(p.Before.SHA256)||p.Before.Mode!=0700&&p.Before.Mode!=0755||!setupDigestRE.MatchString(p.PayloadSHA256)||
 p.Arch!=runtime.GOARCH||p.Release.Build<=0||p.Release.Channel!=p.Channel||!releaseVersion.MatchString(p.Release.Version)||!releaseCommit.MatchString(p.Release.Commit)||
 !strings.Contains(p.PublicKey,"-----BEGIN PGP PUBLIC KEY BLOCK-----")||strings.Contains(p.PublicKey,"PRIVATE KEY")||len(p.PublicKey)>1<<20{
 return fmt.Errorf("invalid reviewed installer update")}
 fps,err:=update.ParseFingerprints(strings.Join(p.Fingerprints,","));if err!=nil||len(fps)==0||!sameSetupJSON(fps,p.Fingerprints){return fmt.Errorf("invalid signing fingerprints")}
 return validateDownload(updateInstallerOptions(p,"unused"))
}
func fetchInstallerUpdate(ctx context.Context,p installerUpdatePlan)(releaseMetadata,[]byte,error){
 dir,err:=os.MkdirTemp("","hypermesh-installer-update-");if err!=nil{return releaseMetadata{},nil,err};defer os.RemoveAll(dir)
 path:=filepath.Join(dir,"installer")
 meta,err:=downloadCLI(ctx,updateInstallerOptions(p,path),fetchArtifact,update.GPGVerify)
 if err!=nil{return meta,nil,err};raw,err:=os.ReadFile(path);return meta,raw,err
}
func RunPlanInstallerUpdate(ctx context.Context,args []string,out,diagnostic io.Writer)int{
 f:=flag.NewFlagSet("plan-installer-update",flag.ContinueOnError);f.SetOutput(diagnostic)
 p:=installerUpdatePlan{Schema:1,Arch:runtime.GOARCH}
 f.StringVar(&p.ReleaseURL,"release-url","","immutable signed release directory")
 f.StringVar(&p.Channel,"channel","","test or prod")
 key:=f.String("trusted-key","","independently trusted public key");fps:=f.String("fingerprints","","trusted fingerprints")
 if err:=f.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
 if f.NArg()!=0||*key==""||buildinfo.CurrentBuild()<=0{fmt.Fprintln(diagnostic,"release-built installer, immutable release URL, channel and trust required");return 2}
 if os.Geteuid()!=0{fmt.Fprintln(diagnostic,"installer update requires root");return 1}
 var err error;p.Destination,err=os.Executable()
 if err==nil{p.Before,err=recoverySnapshot("",p.Destination)}
 var raw []byte
 if err==nil{raw,err=readSetupRegular(*key,false);p.PublicKey=string(raw)}
 if err==nil{p.Fingerprints,err=update.ParseFingerprints(*fps)}
 p.Release.Build=int64(buildinfo.CurrentBuild())+1
 ctx,cancel:=context.WithTimeout(ctx,10*time.Minute);defer cancel()
 var binary []byte
 if err==nil{p.Release,binary,err=fetchInstallerUpdate(ctx,p)}
 if err==nil{p.PayloadSHA256=digestSetupBytes(binary);p.SHA256=installerUpdateDigest(p);err=validateInstallerUpdate(p,p.SHA256)}
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 if json.NewEncoder(out).Encode(p)!=nil{return 1};return 0
}
func installReviewedCLI(ctx context.Context,p installerUpdatePlan,raw []byte,dir string,rollback bool)error{
 if err:=validateInstallerUpdate(p,p.SHA256);err!=nil{return err}
 if err:=secureSetupDirectory(dir,false);err!=nil{return err}
 backup:=filepath.Join(dir,"previous")
 current,err:=recoverySnapshot("",p.Destination);if err!=nil{return err}
 target:=recoveryFile{Path:p.Destination,Present:true,SHA256:p.PayloadSHA256,Mode:p.Before.Mode}
 if rollback{
  if !sameSetupJSON(current,target)&&!sameSetupJSON(current,p.Before){return fmt.Errorf("installer changed; refuse rollback")}
  saved,err:=recoverySnapshot("",backup);if err!=nil||!saved.Present||saved.SHA256!=p.Before.SHA256||saved.Mode!=0600{return fmt.Errorf("verified prior installer backup unavailable")}
  raw,err=os.ReadFile(backup);if err!=nil||digestSetupBytes(raw)!=p.Before.SHA256{return fmt.Errorf("installer backup changed")}
  if err=ctx.Err();err!=nil{return err}
  return recoveryWrite(p.Destination,raw,os.FileMode(p.Before.Mode))
 }
 if digestSetupBytes(raw)!=p.PayloadSHA256{return fmt.Errorf("installer payload differs from approval")}
 if !sameSetupJSON(current,p.Before)&&!sameSetupJSON(current,target){return fmt.Errorf("installed CLI changed since review")}
 if saved,err:=recoverySnapshot("",backup);err!=nil{return err}else if saved.Present{
  if saved.SHA256!=p.Before.SHA256||saved.Mode!=0600{return fmt.Errorf("installer backup differs")}
 }else{
  if !sameSetupJSON(current,p.Before){return fmt.Errorf("installer backup missing after replacement")}
  old,err:=os.ReadFile(p.Destination);if err!=nil||digestSetupBytes(old)!=p.Before.SHA256{return fmt.Errorf("installer changed during backup")}
  if err=writeAgentFile(backup,agentPayload{agentFile{backup,p.Before.SHA256,0600},old});err!=nil{return err}
  if err=recoverySync(dir);err!=nil{return err}
 }
 if err=ctx.Err();err!=nil{return err}
 return recoveryWrite(p.Destination,raw,os.FileMode(p.Before.Mode))
}
func RunUpdateInstaller(ctx context.Context,args []string,out,diagnostic io.Writer)int{return runInstallerUpdate(ctx,args,out,diagnostic,false)}
func RunRollbackInstaller(ctx context.Context,args []string,out,diagnostic io.Writer)int{return runInstallerUpdate(ctx,args,out,diagnostic,true)}
func runInstallerUpdate(ctx context.Context,args []string,out,diagnostic io.Writer,rollback bool)int{
 f:=flag.NewFlagSet("update-installer",flag.ContinueOnError);f.SetOutput(diagnostic)
 path:=f.String("plan","","reviewed installer update");sha:=f.String("approve-plan","","exact update digest");apply:=f.Bool("apply",false,"approve installer replacement")
 if err:=f.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
 if f.NArg()!=0||*path==""||!*apply||!setupDigestRE.MatchString(*sha){fmt.Fprintln(diagnostic,"require --plan FILE --approve-plan SHA256 --apply");return 2}
 if os.Geteuid()!=0{fmt.Fprintln(diagnostic,"installer update requires root");return 1}
 var p installerUpdatePlan;raw,err:=readSetupRegular(*path,false);if err==nil{err=decodeSetupJSON(raw,&p)}
 if err==nil{err=validateInstallerUpdate(p,*sha)}
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 unlock,err:=lockSetupState(defaultSetupState);if err!=nil{fmt.Fprintln(diagnostic,err);return 1};defer unlock()
 ctx,cancel:=context.WithTimeout(ctx,10*time.Minute);defer cancel()
 dir:=filepath.Join(defaultSetupState,"installer-update-"+p.SHA256)
 if err=secureSetupDirectory(dir,true);err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 var binary []byte
 if !rollback{
  var meta releaseMetadata
  meta,binary,err=fetchInstallerUpdate(ctx,p)
  if err==nil&&!sameSetupJSON(meta,p.Release){err=fmt.Errorf("signed installer release changed")}
 }
 if err==nil{
  // A durable build floor survives rollback. Rollback is this explicit recovery
  // operation, never channel discovery silently choosing an older installer.
  _,persist,release,e:=lockBuildState(filepath.Join(defaultSetupState,"installer-update-floor.json"))
  if e!=nil{err=e}else{
   if !rollback{err=persist(p.Release.Build)}
   release()
  }
 }
 if err==nil{err=installReviewedCLI(ctx,p,binary,dir,rollback)}
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 operation:="updated";if rollback{operation="rolled-back"}
 _=json.NewEncoder(out).Encode(map[string]any{"state":operation,"plan_sha256":p.SHA256,"destination":p.Destination,"restart_required":true})
 return 0
}
