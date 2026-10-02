//go:build linux

package installer

import (
 "context"
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "flag"
 "fmt"
 "io"
 "os"
 "path/filepath"
 "syscall"
 "strings"
 "time"
)

type recoveryFile struct {
 Path string `json:"path"`
 Present bool `json:"present"`
 SHA256 string `json:"sha256,omitempty"`
 Mode uint32 `json:"mode,omitempty"`
}
type recoveryPlan struct {
 Schema int `json:"schema"`
 SHA256 string `json:"plan_sha256"`
 Operation string `json:"operation"`
 Agent agentInstallPlan `json:"agent"`
 Before []recoveryFile `json:"before"`
}
type recoveryJournal struct {
 Schema int `json:"schema"`
 PlanSHA256 string `json:"plan_sha256"`
 State string `json:"state"`
 Completed int `json:"completed"`
 RecordedAt time.Time `json:"recorded_at"`
}
func recoveryDigest(p recoveryPlan)string{p.SHA256="";b,_:=json.Marshal(p);return digestSetupBytes(b)}
func validateRecovery(p recoveryPlan,approved string)error{
 if p.Schema!=1||!supportOneOf(p.Operation,"repair","uninstall")||p.SHA256!=approved||!setupDigestRE.MatchString(approved)||recoveryDigest(p)!=approved||
 agentPlanDigest(p.Agent)!=p.Agent.SHA256||validateAgentInputs(p.Agent)!=nil{return fmt.Errorf("invalid recovery approval")}
 expected:=agentPayloads(p.Agent,nil)
 if len(expected)!=len(p.Agent.Files)||len(p.Before)!=len(expected){return fmt.Errorf("invalid recovery manifest")}
 for i,f:=range p.Agent.Files{
  old:=p.Before[i]
  if f.Path!=expected[i].Path||f.Mode!=expected[i].Mode||!setupDigestRE.MatchString(f.SHA256)||(i>0&&f.SHA256!=expected[i].SHA256)||old.Path!=f.Path||
   (old.Present&&(!setupDigestRE.MatchString(old.SHA256)||old.Mode&^0777!=0))||(!old.Present&&(old.SHA256!=""||old.Mode!=0)){return fmt.Errorf("invalid recovery file")}
 }
 return nil
}
func recoverySnapshot(root,path string)(recoveryFile,error){
 r:=recoveryFile{Path:path};full:=agentDest(root,path)
 parent:=filepath.Dir(full)
 for {if _,err:=os.Lstat(parent);!os.IsNotExist(err){break};parent=filepath.Dir(parent)}
 if err:=agentDirectory(parent,false);err!=nil{return r,err}
 info,err:=os.Lstat(full);if os.IsNotExist(err){return r,nil}
 if err!=nil||!info.Mode().IsRegular()||info.Size()>256<<20||info.Mode()&(os.ModeType|os.ModeSetuid|os.ModeSetgid|os.ModeSticky)!=0{return r,fmt.Errorf("unsafe recovery payload")}
 st,ok:=info.Sys().(*syscall.Stat_t)
 if !ok||st.Uid!=uint32(os.Geteuid())||st.Nlink!=1||info.Mode().Perm()&0022!=0{return r,fmt.Errorf("unsafe recovery ownership or permissions")}
 f,err:=os.OpenFile(full,os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK,0);if err!=nil{return r,err};defer f.Close()
 after,err:=f.Stat();if err!=nil||!os.SameFile(info,after){return r,fmt.Errorf("recovery payload changed")}
 h:=sha256.New();n,err:=io.Copy(h,io.LimitReader(f,(256<<20)+1));if err!=nil||n!=info.Size(){return r,fmt.Errorf("recovery payload unreadable or changed")}
 r.Present=true;r.SHA256=hex.EncodeToString(h.Sum(nil));r.Mode=uint32(info.Mode().Perm());return r,nil
}
func recoveryGuard(ctx context.Context,root string,p agentInstallPlan,source setupSource)error{
 if _,err:=os.Lstat(agentDest(root,"/var/lib/hypermesh/lease.json"));!os.IsNotExist(err){return fmt.Errorf("existing lease must be reconciled before recovery")}
 if err:=checkAgentUnitPaths(ctx,source,root,agentPayloads(p,nil));err!=nil{return err}
 if err:=inactiveMemoryServices(ctx,source);err!=nil{return fmt.Errorf("host services must be inactive before recovery; drain the host first")}
 for _,unit:=range []string{"hypermesh-agent.service","hypermesh-wg.service"}{
  raw,err:=source.output(ctx,"systemctl","--system","show","--all",unit,"-p","UnitFileState","--value")
  if err!=nil||!supportOneOf(strings.TrimSpace(string(raw)),"","disabled","static","masked"){return fmt.Errorf("disable host units before recovery so they cannot start at boot")}
 }
 if err:=noMaintenanceContainers(ctx);err!=nil{return err}
 return ctx.Err()
}
func saveRecovery(dir string,j recoveryJournal)error{
 if j.Schema!=1||!setupDigestRE.MatchString(j.PlanSHA256)||!supportOneOf(j.State,"prepared","applying","complete")||j.Completed<0||j.RecordedAt.IsZero(){return fmt.Errorf("invalid recovery checkpoint")}
 raw,_:=json.Marshal(j)
 return recoveryWrite(filepath.Join(dir,"journal.json"),raw,0600)
}
func recoveryWrite(path string,raw []byte,mode os.FileMode)error{
 if err:=agentDirectory(filepath.Dir(path),false);err!=nil{return err}
 f,err:=os.CreateTemp(filepath.Dir(path),".recovery-");if err!=nil{return err};defer os.Remove(f.Name())
 if _,err=f.Write(raw);err!=nil{f.Close();return err}
 if err=f.Chmod(mode);err!=nil{f.Close();return err}
 if err=f.Sync();err!=nil{f.Close();return err};if err=f.Close();err!=nil{return err}
 if err=os.Rename(f.Name(),path);err!=nil{return err}
 return recoverySync(filepath.Dir(path))
}
func recoverySync(path string)error{d,err:=os.Open(path);if err!=nil{return err};defer d.Close();return d.Sync()}

// Back up the entire reviewed set before the first mutation. Backups, keys,
// models and all registration history survive both repair and uninstall.
func applyRecovery(ctx context.Context,root,dir string,p recoveryPlan,payloads []agentPayload,j *recoveryJournal,guard func(context.Context)error,save func(recoveryJournal)error)error{
 if err:=validateRecovery(p,p.SHA256);err!=nil{return err}
 if p.Operation=="repair"&&!sameSetupJSON(agentFileManifest(payloads),p.Agent.Files){return fmt.Errorf("repair payload differs from reviewed signed installation")}
 if j.Schema!=1||j.PlanSHA256!=p.SHA256||j.Completed<0||j.Completed>len(p.Before)||!supportOneOf(j.State,"prepared","applying","complete")||
 (j.State=="prepared"&&j.Completed!=0)||(j.State=="complete"&&j.Completed!=len(p.Before)){return fmt.Errorf("invalid recovery checkpoint")}
 if err:=guard(ctx);err!=nil{return err}
 if err:=secureSetupDirectory(dir,false);err!=nil{return err}
 target:=func(i int)recoveryFile{r:=recoveryFile{Path:p.Before[i].Path};if p.Operation=="repair"{f:=p.Agent.Files[i];r.Present=true;r.SHA256=f.SHA256;r.Mode=f.Mode};return r}
 for i,old:=range p.Before{
  current,err:=recoverySnapshot(root,old.Path);if err!=nil{return err}
  if (j.State=="complete"&&!sameSetupJSON(current,target(i)))||(!sameSetupJSON(current,old)&&(j.State=="prepared"||!sameSetupJSON(current,target(i)))){return fmt.Errorf("payload changed since review; preserve checkpoint and review a new recovery plan")}
  backup:=filepath.Join(dir,fmt.Sprintf("backup-%d",i))
  if old.Present{
   if _,err:=os.Lstat(backup);os.IsNotExist(err){
    if j.State!="prepared"||!sameSetupJSON(current,old){return fmt.Errorf("required recovery backup is missing")}
    raw,err:=os.ReadFile(agentDest(root,old.Path));if err!=nil||digestSetupBytes(raw)!=old.SHA256{return fmt.Errorf("backup source changed")}
    if err=writeAgentFile(backup,agentPayload{agentFile{backup,old.SHA256,0600},raw});err!=nil{return err}
   }else if err!=nil{return err}
   snap,err:=recoverySnapshot("",backup)
   if err!=nil||!snap.Present||snap.SHA256!=old.SHA256||snap.Mode!=0600{return fmt.Errorf("recovery backup is unsafe or differs")}
  }
 }
 if j.State=="complete"{return nil}
 j.State="applying";j.RecordedAt=time.Now().UTC();if err:=save(*j);err!=nil{return err}
 for i,old:=range p.Before{
  if err:=guard(ctx);err!=nil{return err}
  current,err:=recoverySnapshot(root,old.Path);if err!=nil{return err}
  want:=target(i)
  if !sameSetupJSON(current,want){
   if !sameSetupJSON(current,old){return fmt.Errorf("payload changed during recovery")}
   path:=agentDest(root,old.Path)
   if p.Operation=="repair"{
    if err=agentDirectory(filepath.Dir(path),true);err!=nil{return err}
    if err=recoveryWrite(path,payloads[i].Data,os.FileMode(want.Mode));err!=nil{return err}
   }else if current.Present{
    if err=os.Remove(path);err!=nil{return err}
    if err=recoverySync(filepath.Dir(path));err!=nil{return err}
   }
  }
  j.Completed=i+1;j.RecordedAt=time.Now().UTC();if err=save(*j);err!=nil{return err}
 }
 if err:=guard(ctx);err!=nil{return err}
 j.State="complete";j.RecordedAt=time.Now().UTC();return save(*j)
}

func RunPlanRecovery(ctx context.Context,args []string,out,diagnostic io.Writer)int{
 f:=flag.NewFlagSet("plan-recovery",flag.ContinueOnError);f.SetOutput(diagnostic)
 path:=f.String("agent-plan","","original reviewed agent installation plan")
 op:=f.String("operation","","repair or uninstall; preserve identity and models")
 if err:=f.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
 if f.NArg()!=0||*path==""||!supportOneOf(*op,"repair","uninstall"){fmt.Fprintln(diagnostic,"require --agent-plan FILE --operation repair|uninstall");return 2}
 if os.Geteuid()!=0{fmt.Fprintln(diagnostic,"recovery requires root");return 1}
 p:=recoveryPlan{Schema:1,Operation:*op};raw,err:=readSetupRegular(*path,false)
 if err==nil{err=decodeSetupJSON(raw,&p.Agent)}
 if err==nil{err=validateAgentInputs(p.Agent)}
 if err!=nil{fmt.Fprintln(diagnostic,"invalid agent plan");return 1}
 unlock,err:=supportLock(defaultSetupState);if err!=nil{fmt.Fprintln(diagnostic,err);return 1};defer unlock()
 ctx,cancel:=context.WithTimeout(ctx,90*time.Second);defer cancel()
 if err=recoveryGuard(ctx,"",p.Agent,rebootSetupSource());err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 for _,file:=range p.Agent.Files{snap,e:=recoverySnapshot("",file.Path);if e!=nil{fmt.Fprintln(diagnostic,e);return 1};p.Before=append(p.Before,snap)}
 p.SHA256=recoveryDigest(p)
 if err=validateRecovery(p,p.SHA256);err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 if json.NewEncoder(out).Encode(p)!=nil{return 1};return 0
}
func RunRecoverAgent(ctx context.Context,args []string,out,diagnostic io.Writer)int{
 f:=flag.NewFlagSet("recover-agent",flag.ContinueOnError);f.SetOutput(diagnostic)
 path:=f.String("plan","","reviewed recovery plan")
 approved:=f.String("approve-plan","","exact recovery plan digest")
 apply:=f.Bool("apply",false,"apply recovery; never deletes keys or models or starts services")
 if err:=f.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
 if f.NArg()!=0||*path==""||!*apply||!setupDigestRE.MatchString(*approved){fmt.Fprintln(diagnostic,"require --plan FILE --approve-plan SHA256 --apply");return 2}
 if os.Geteuid()!=0{fmt.Fprintln(diagnostic,"recovery requires root");return 1}
 var p recoveryPlan;raw,err:=readSetupRegular(*path,false);if err==nil{err=decodeSetupJSON(raw,&p)}
 if err==nil{err=validateRecovery(p,*approved)}
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 unlock,err:=lockRebootState("",agentSetupState(p.Agent));if err!=nil{fmt.Fprintln(diagnostic,err);return 1};defer unlock()
 ctx,cancel:=context.WithTimeout(ctx,15*time.Minute);defer cancel()
 guard:=func(ctx context.Context)error{return recoveryGuard(ctx,"",p.Agent,rebootSetupSource())}
 if err=guard(ctx);err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 var files []agentPayload
 if p.Operation=="repair"{
  var meta releaseMetadata;var binary []byte
  meta,binary,err=fetchAgentPayload(ctx,p.Agent);files=agentPayloads(p.Agent,binary)
  if err==nil&&(!sameSetupJSON(meta,p.Agent.Release)||!sameSetupJSON(agentFileManifest(files),p.Agent.Files)){err=fmt.Errorf("signed repair release differs from original approval")}
  if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 }
 dir:=filepath.Join(defaultSetupState,"recovery-"+p.SHA256)
 if err=secureSetupDirectory(dir,true);err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 j:=recoveryJournal{Schema:1,PlanSHA256:p.SHA256,State:"prepared",RecordedAt:time.Now().UTC()}
 journal:=filepath.Join(dir,"journal.json")
 if _,e:=os.Lstat(journal);e==nil{raw,err=readSetupRegular(journal,true);if err==nil{err=decodeSetupJSON(raw,&j)}}else if os.IsNotExist(e){err=saveRecovery(dir,j)}else{err=e}
 if err==nil{err=applyRecovery(ctx,"",dir,p,files,&j,guard,func(v recoveryJournal)error{return saveRecovery(dir,v)})}
 if err==nil&&p.Operation=="repair"{
  installed:=agentInstallJournal{Schema:1,PlanSHA256:p.Agent.SHA256,State:"installed-awaiting-enrollment",Started:true}
  for _,file:=range p.Agent.Files{installed.Completed=append(installed.Completed,file.Path)}
  err=saveAgentJournal(filepath.Join(defaultSetupState,"agent-"+p.Agent.SHA256+".json"),installed)
 }
 if err==nil{
  _,err=liveSystemdOutput(ctx,"systemctl","--system","daemon-reload")
 }
 if json.NewEncoder(out).Encode(map[string]any{"operation":p.Operation,"plan_sha256":p.SHA256,"state":j.State,"attempt_succeeded":err==nil,"identity_preserved":true,"models_preserved":true})!=nil{return 1}
 if err!=nil{fmt.Fprintln(diagnostic,"recovery incomplete; preserve private backups and rerun the same approved plan:",err);return 1};return 0
}
