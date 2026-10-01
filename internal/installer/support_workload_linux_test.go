//go:build linux

package installer

import (
 "bytes"
 "context"
 "encoding/json"
 "os"
 "path/filepath"
 "strings"
 "testing"
 "time"

 "github.com/FyberLabs/hypermesh-host/internal/pathb"
)

func supportWorkloadFixture(t *testing.T, full bool) (string, workloadJournal) {
 t.Helper()
 dir:=t.TempDir()
 if err:=os.Chmod(dir,0700);err!=nil{t.Fatal(err)}
 unlock,err:=lockSetupState(dir);if err!=nil{t.Fatal(err)};unlock()
 p,r:=workloadFixture(t)
 p.Request.FullContext=full
 p.Request.CatalogID="PRIVATE_CATALOG"
 p.Request.ImageDigest="private.invalid/runtime@sha256:"+strings.Repeat("a",64)
 p.Request.ModelPath=filepath.Join(p.Setup.Preflight.Resources.ModelDirectory,"PRIVATE_MODEL.gguf")
 // Historical journals remain useful after an installer update.
 p.Setup.InstallerBuild++
 p.Setup.PlanSHA256=setupPlanDigest(p.Setup)
 p.SHA256=workloadDigest(p);p.Request.PlanSHA256=p.SHA256;r.Request=p.Request
 if full { r.ContextFit=&pathb.ContextFit{Protocol:pathb.ContextFitProtocol,ContextTokens:4096,PromptTokens:4080,GeneratedTokens:16,ElapsedMS:123} }
 stamp:=time.Date(2026,10,1,10,0,0,0,time.FixedZone("local",-4*3600))
 j:=workloadJournal{Schema:1,Plan:p,State:"verified",StartedAt:stamp,RecordedAt:stamp,Receipt:&r,NextAction:"PRIVATE_NEXT"}
 if err:=saveWorkloadJournal(workloadJournalPath(dir,p.SHA256),j);err!=nil{t.Fatal(err)}
 return dir,j
}

func TestSupportWorkloadEvidenceAndPrivacy(t *testing.T) {
 for _,full:=range []bool{false,true} {
  dir,j:=supportWorkloadFixture(t,full)
  path:=workloadJournalPath(dir,j.Plan.SHA256);before,_:=os.ReadFile(path)
  bundle,err:=collectSupportWithWorkload(context.Background(),dir,"","",j.Plan.SHA256)
  if err!=nil{t.Fatal(err)}
  if !bundle.CollectionComplete||!bundle.HistoricalOnly||bundle.WorkloadPlanSHA256!=j.Plan.SHA256{t.Fatal("wrong bundle status")}
  stage:=bundle.Stages[5];v:=stage.Workload
  if stage.Collection!="collected"||v==nil||!v.InferenceVerified||!v.CleanupComplete||
   v.FullContextRequested!=full||v.FullContextVerified!=full||v.SampleMS!=100||v.ContextTokens!=4096{t.Fatalf("%+v",stage)}
  if full&&(v.PromptTokens!=4080||v.GeneratedTokens!=16||v.ContextElapsedMS!=123){t.Fatal("lost context evidence")}
  if !full&&(v.PromptTokens!=0||v.GeneratedTokens!=0||v.ContextElapsedMS!=0){t.Fatal("invented context evidence")}
  for _,s:=range bundle.Stages[:5]{if s.Collection!="not-requested"{t.Fatal("read unrelated journals")}}
  raw,_:=json.Marshal(bundle)
  for _,private:=range []string{"PRIVATE_","private.invalid",j.Plan.Request.ModelPath,j.Plan.Request.FileHash,j.Receipt.HostImageHash,"-04:00"}{
   if strings.Contains(string(raw),private){t.Fatalf("export leaked %q",private)}
  }
  after,_:=os.ReadFile(path);if !bytes.Equal(before,after){t.Fatal("changed historical journal")}
 }
}

func TestSupportWorkloadFailureAndIncompleteEvidence(t *testing.T) {
 for _,kind:=range []string{"checking","preflight","runtime","interrupted","status_persistence","missing-receipt","cleanup","context","wrong-plan","wrong-setup","bad-code","corrupt","missing","symlink"}{
  t.Run(kind,func(t *testing.T){
   dir,j:=supportWorkloadFixture(t,true)
   sha:=j.Plan.SHA256;path:=workloadJournalPath(dir,sha)
   setupSHA:="";want:="invalid"
   switch kind{
   case "checking":j.State="checking";j.Receipt=nil;want="collected"
   case "preflight","runtime","interrupted","status_persistence":
    j.State="failed";j.FailureCode=kind;j.Receipt=nil;want="collected"
   case "missing-receipt":j.Receipt=nil
   case "cleanup":j.Receipt.CleanupComplete=false
   case "context":j.Receipt.ContextFit.ContextTokens++
   case "wrong-plan":j.Plan.SHA256=strings.Repeat("f",64)
   case "wrong-setup":setupSHA=strings.Repeat("f",64)
   case "bad-code":j.State="failed";j.Receipt=nil;j.FailureCode="PRIVATE_CODE"
   case "missing":want="missing"
   case "symlink":want="unavailable"
   }
   raw,_:=json.Marshal(j)
   if kind=="corrupt"{raw=[]byte("PRIVATE_CORRUPT")}
   if err:=os.WriteFile(path,raw,0600);err!=nil{t.Fatal(err)}
   if kind=="missing"||kind=="symlink"{if err:=os.Remove(path);err!=nil{t.Fatal(err)}}
   if kind=="symlink"{if err:=os.Symlink(filepath.Join(dir,"absent"),path);err!=nil{t.Fatal(err)}}
   bundle,err:=collectSupportWithWorkload(context.Background(),dir,"",setupSHA,sha);if err!=nil{t.Fatal(err)}
   stage:=bundle.Stages[5]
   if stage.Collection!=want{t.Fatalf("got %s want %s",stage.Collection,want)}
   if bundle.CollectionComplete!=(want=="collected"){t.Fatal("incomplete evidence marked complete")}
   if stage.Workload!=nil&&(stage.Workload.InferenceVerified||stage.Workload.FullContextVerified||stage.Workload.CleanupComplete){t.Fatal("invented successful evidence")}
   exported,_:=json.Marshal(bundle)
   if strings.Contains(string(exported),"PRIVATE_"){t.Fatal("private text leaked")}
   if kind=="checking"&&!strings.Contains(stage.NextAction,"interrupted"){t.Fatal("missing recovery guidance")}
   if kind=="runtime"&&!strings.Contains(stage.NextAction,"Cleanup is unconfirmed"){t.Fatal("unsafe retry guidance")}
  })
 }
}

func TestSupportWorkloadCLIAndLock(t *testing.T) {
 dir,j:=supportWorkloadFixture(t,true)
 output:=filepath.Join(dir,"support.json")
 args:=[]string{"--workload-plan-sha",j.Plan.SHA256,"--output",output}
 var out,diagnostic bytes.Buffer
 if code:=runSupportBundle(context.Background(),args,&out,&diagnostic,dir,true);code!=0{t.Fatalf("exit %d: %s",code,diagnostic.String())}
 info,err:=os.Stat(output);if err!=nil||!privateSetupFile(info){t.Fatal("unsafe publication")}
 unlock,err:=lockSetupState(dir);if err!=nil{t.Fatal(err)}
 if _,err:=collectSupportWithWorkload(context.Background(),dir,"","",j.Plan.SHA256);err==nil{t.Fatal("read during workload writer")}
 unlock()
 args[1]="../private"
 if runSupportBundle(context.Background(),args,&out,&diagnostic,dir,true)!=2{t.Fatal("unsafe digest accepted")}
 args[1]=j.Plan.SHA256;args[3]=filepath.Join(dir,"partial.json")
 if err:=os.Remove(workloadJournalPath(dir,j.Plan.SHA256));err!=nil{t.Fatal(err)}
 if runSupportBundle(context.Background(),args,&out,&diagnostic,dir,true)!=1{t.Fatal("missing journal reported complete")}
 raw,err:=os.ReadFile(args[3]);if err!=nil||!bytes.Contains(raw,[]byte(`"collection_complete": false`)){t.Fatal("partial evidence not saved")}
}
