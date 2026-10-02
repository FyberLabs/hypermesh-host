//go:build linux

package installer
import (
 "context"
 "fmt"
 "os"
 "path/filepath"
 "strings"
 "testing"
 "time"
)
func recoveryFixture(t *testing.T,op string)(recoveryPlan,[]agentPayload,string,string,recoveryJournal){
 t.Helper()
 agent:=agentInstallFixture(t);files:=agentPayloads(agent,[]byte("verified agent"))
 root:=t.TempDir();dir:=filepath.Join(root,"recovery")
 if err:=secureSetupDirectory(dir,true);err!=nil{t.Fatal(err)}
 p:=recoveryPlan{Schema:1,Operation:op,Agent:agent}
 for i,file:=range files{
  path:=agentDest(root,file.Path)
  if i!=1{
   raw:=append([]byte("old-"),file.Data...)
   if err:=writeAgentFile(path,agentPayload{agentFile{file.Path,digestSetupBytes(raw),file.Mode},raw});err!=nil{t.Fatal(err)}
  }
  snap,err:=recoverySnapshot(root,file.Path);if err!=nil{t.Fatal(err)};p.Before=append(p.Before,snap)
 }
 p.SHA256=recoveryDigest(p)
 if err:=validateRecovery(p,p.SHA256);err!=nil{t.Fatal(err)}
 return p,files,root,dir,recoveryJournal{Schema:1,PlanSHA256:p.SHA256,State:"prepared",RecordedAt:time.Now()}
}
func TestRecoveryPreservesIdentityModelsAndBackups(t *testing.T){
 for _,op:=range []string{"repair","uninstall"}{t.Run(op,func(t *testing.T){
  p,files,root,dir,j:=recoveryFixture(t,op)
  for _,name:=range []string{"/var/lib/hypermesh/device.json","/var/lib/hypermesh/registration-keys/key","/models/cache/model.gguf"}{
   path:=agentDest(root,name);if err:=os.MkdirAll(filepath.Dir(path),0700);err!=nil{t.Fatal(err)};if err:=os.WriteFile(path,[]byte("preserved"),0600);err!=nil{t.Fatal(err)}
  }
  guard:=func(context.Context)error{return nil};save:=func(v recoveryJournal)error{return saveRecovery(dir,v)}
  if err:=applyRecovery(context.Background(),root,dir,p,files,&j,guard,save);err!=nil{t.Fatal(err)}
  if j.State!="complete"||j.Completed!=len(files){t.Fatal("not complete")}
  for i,file:=range files{
   snap,err:=recoverySnapshot(root,file.Path);if err!=nil{t.Fatal(err)}
   if op=="repair"&&(!snap.Present||snap.SHA256!=file.SHA256||snap.Mode!=file.Mode){t.Fatal("repair differs")}
   if op=="uninstall"&&snap.Present{t.Fatal("uninstall left managed file")}
   if p.Before[i].Present{snap,err:=recoverySnapshot("",filepath.Join(dir,fmt.Sprintf("backup-%d",i)));if err!=nil||snap.SHA256!=p.Before[i].SHA256||snap.Mode!=0600{t.Fatal("backup lost")}}
  }
  for _,name:=range []string{"/var/lib/hypermesh/device.json","/var/lib/hypermesh/registration-keys/key","/models/cache/model.gguf"}{
   raw,err:=os.ReadFile(agentDest(root,name));if err!=nil||string(raw)!="preserved"{t.Fatal("unrelated data changed")}
  }
  if err:=applyRecovery(context.Background(),root,dir,p,files,&j,guard,save);err!=nil{t.Fatal("repeat failed",err)}
 })}
}
func TestRecoveryInterruptedWriteResumesFromDurableIntent(t *testing.T){
 p,files,root,dir,j:=recoveryFixture(t,"repair")
 durable:=j
 saves:=0
 save:=func(v recoveryJournal)error{saves++;if saves==2{return fmt.Errorf("lost checkpoint")};durable=v;return saveRecovery(dir,v)}
 if applyRecovery(context.Background(),root,dir,p,files,&j,func(context.Context)error{return nil},save)==nil{t.Fatal("ignored save failure")}
 j=durable
 if j.State!="applying"||j.Completed!=0{t.Fatal("wrong durable checkpoint")}
 if err:=applyRecovery(context.Background(),root,dir,p,files,&j,func(context.Context)error{return nil},func(v recoveryJournal)error{return saveRecovery(dir,v)});err!=nil{t.Fatal(err)}
}
func TestRecoveryRejectsChangedFilesUnsafePathsAndMissingBackups(t *testing.T){
 for _,kind:=range []string{"changed","symlink","guard","payload","missing-backup","complete-changed"}{t.Run(kind,func(t *testing.T){
  p,files,root,dir,j:=recoveryFixture(t,"repair")
  guard:=func(context.Context)error{return nil}
  save:=func(v recoveryJournal)error{return saveRecovery(dir,v)}
  path:=agentDest(root,p.Before[0].Path)
  switch kind{
  case "changed":if err:=os.WriteFile(path,[]byte("changed"),0755);err!=nil{t.Fatal(err)}
  case "symlink":if err:=os.Remove(path);err!=nil{t.Fatal(err)};if err:=os.Symlink("/private",path);err!=nil{t.Fatal(err)}
  case "guard":guard=func(context.Context)error{return fmt.Errorf("active lease")}
  case "payload":files[0].SHA256=digestSetupBytes([]byte("wrong"))
  case "missing-backup":j.State="applying"
  case "complete-changed":
   if err:=applyRecovery(context.Background(),root,dir,p,files,&j,guard,save);err!=nil{t.Fatal(err)}
   if err:=os.WriteFile(path,[]byte("old-verified agent"),0755);err!=nil{t.Fatal(err)}
  }
  if applyRecovery(context.Background(),root,dir,p,files,&j,guard,save)==nil{t.Fatal("unsafe recovery accepted")}
 })}
}
func TestRecoveryApprovalBindsOperationAndExactSnapshot(t *testing.T){
 p,_,_,_,_:=recoveryFixture(t,"repair")
 p.Operation="uninstall"
 if validateRecovery(p,p.SHA256)==nil{t.Fatal("operation not bound")}
 p.Operation="repair";p.Before[0].SHA256=digestSetupBytes([]byte("changed"))
 if validateRecovery(p,p.SHA256)==nil{t.Fatal("snapshot not bound")}
 p.SHA256=recoveryDigest(p);p.Before[0].Path="/etc/passwd";p.SHA256=recoveryDigest(p)
 if validateRecovery(p,p.SHA256)==nil{t.Fatal("arbitrary destination accepted")}
}

func TestUninstalledSupportNeverRecommendsRegistration(t *testing.T){
 p:=agentInstallFixture(t);dir:=t.TempDir()
 j:=agentInstallJournal{Schema:1,PlanSHA256:p.SHA256,State:"uninstalled",Started:true}
 for _,f:=range p.Files{j.Completed=append(j.Completed,f.Path)}
 if err:=saveAgentJournal(filepath.Join(dir,"agent-"+p.SHA256+".json"),j);err!=nil{t.Fatal(err)}
 report:=supportAgent(dir,p.SHA256)
 if report.Collection!="collected"||report.State!="uninstalled"||!strings.Contains(report.NextAction,"repair"){t.Fatal(report)}
}
