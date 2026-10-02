//go:build linux

package installer

import (
 "bytes"
 "context"
 "encoding/json"
 "math"
 "os"
 "path/filepath"
 "strings"
 "testing"
 "time"

 "github.com/FyberLabs/hypermesh-host/internal/hostregistration"
)

func supportCertificationFixture(t *testing.T) (string,certificationJournal) {
 t.Helper()
 dir:=t.TempDir()
 if err:=os.Chmod(dir,0700);err!=nil{t.Fatal(err)}
 unlock,err:=lockSetupState(dir);if err!=nil{t.Fatal(err)};unlock()
 id,err:=hostregistration.NewRunID();if err!=nil{t.Fatal(err)}
 thermal:=52.0
 j:=certificationJournal{Schema:1,PlanSHA256:strings.Repeat("a",64),RunID:id,State:"passed",
  RecordedAt:time.Date(2026,10,1,10,0,0,0,time.FixedZone("private",-4*3600)),
  Evidence:&hostregistration.CertificationEvidence{
   HostImageHash:"sha256:"+strings.Repeat("b",64),FileHash:"sha256:"+strings.Repeat("c",64),
   ImageDigest:"private.invalid/runtime@sha256:"+strings.Repeat("d",64),
   SampleMS:100,TokensPerSecond:12.5,ThermalC:&thermal,ContextTokens:4096,
   PromptTokens:4080,GeneratedTokens:16,ContextElapsedMS:123,
   CleanupComplete:true,TunnelVerified:true,
  }}
 return dir,j
}
func writeSupportCertificationFixture(t *testing.T,dir string,j certificationJournal) {
 t.Helper()
 raw,err:=json.Marshal(j);if err!=nil{t.Fatal(err)}
 if err=os.WriteFile(certificationStatePath(dir,j.PlanSHA256),raw,0600);err!=nil{t.Fatal(err)}
}
func TestSupportCertificationStatesAndPrivacy(t *testing.T) {
 for _,state:=range []string{"running","evidence-ready","passed","failed","activating","active"} {
  t.Run(state,func(t *testing.T){
   dir,j:=supportCertificationFixture(t);j.State=state
   if state=="running"{j.Evidence=nil}
   if state=="failed"{j.FailureCode="class_band"}
   writeSupportCertificationFixture(t,dir,j)
   path:=certificationStatePath(dir,j.PlanSHA256);before,_:=os.ReadFile(path)
   bundle,err:=collectSupportWithCertification(context.Background(),dir,"","","",j.PlanSHA256)
   if err!=nil{t.Fatal(err)}
   stage:=bundle.Stages[6]
   if !bundle.HistoricalOnly||!bundle.CollectionComplete||stage.Collection!="collected"||stage.State!=state||stage.NextAction==""{t.Fatalf("%+v",stage)}
   for _,s:=range bundle.Stages[:6]{if s.Collection!="not-requested"{t.Fatal("read unrelated stage")}}
   if state=="running" { if stage.Certification!=nil{t.Fatal("invented evidence")} } else {
    e:=stage.Certification
    if e==nil||e.ContextTokens!=4096||e.PromptTokens!=4080||e.GeneratedTokens!=16||
     e.ContextElapsedMS!=123||e.SampleMS!=100||e.TokensPerSecond!=12.5||e.ThermalC==nil||*e.ThermalC!=52||
     !e.CleanupRecorded||!e.TunnelRecorded||e.ResultAccepted!=(state!="evidence-ready")||
     e.ClassPassed!=supportOneOf(state,"passed","activating","active")||e.StartupRecorded!=(state=="active"){t.Fatalf("%+v",e)}
   }
   raw,_:=json.Marshal(bundle)
   for _,private:=range []string{j.RunID,"private.invalid","sha256:","-04:00"}{
    if bytes.Contains(raw,[]byte(private)){t.Fatal("private metadata leaked",private)}
   }
   after,_:=os.ReadFile(path);if !bytes.Equal(before,after){t.Fatal("modified source journal")}
  })
 }
}
func TestSupportCertificationFailuresAndGuidance(t *testing.T) {
 cases:=[]struct{state,code,fragment string}{
  {"failed","preflight","prerequisites"},{"failed","acquisition","model access/license"},
  {"failed","workload","cleanup is unconfirmed"},{"failed","tunnel","UDP/overlay"},
  {"failed","postflight","lease state"},{"failed","class_band","terminal"},
  {"passed","service_start","rollback was recorded complete"},{"activating","cleanup","rollback is unconfirmed"},
  {"running","","interrupted"},{"evidence-ready","","identical evidence"},{"active","","does not prove"},
 }
 for _,tc:=range cases {
  t.Run(tc.state+"/"+tc.code,func(t *testing.T){
   dir,j:=supportCertificationFixture(t);j.State=tc.state;j.FailureCode=tc.code
   if tc.state=="running"||(tc.state=="failed"&&tc.code!="class_band"){j.Evidence=nil}
   writeSupportCertificationFixture(t,dir,j)
   stage:=supportCertification(dir,j.PlanSHA256)
   if stage.Collection!="collected"||!strings.Contains(stage.NextAction,tc.fragment){t.Fatalf("%+v",stage)}
  })
 }
}
func TestSupportCertificationRejectsIncompleteEvidence(t *testing.T) {
 kinds:=[]string{"missing-evidence","empty-evidence","host-hash","file-hash","image","sample","rate","context","prompt","generated","elapsed","cleanup","tunnel",
  "running-evidence","running-code","passed-code","active-code","activating-code","failed-code","class-band-evidence","failed-evidence",
  "wrong-plan","wrong-run","timestamp","schema","unknown-state","corrupt","unknown-field","duplicate","missing","symlink","hardlink","unsafe-mode"}
 for _,kind:=range kinds {
  t.Run(kind,func(t *testing.T){
   dir,j:=supportCertificationFixture(t);sha:=j.PlanSHA256;path:=certificationStatePath(dir,sha);want:="invalid"
   switch kind {
   case "missing-evidence":j.Evidence=nil
   case "empty-evidence":j.Evidence=&hostregistration.CertificationEvidence{}
   case "host-hash":j.Evidence.HostImageHash="PRIVATE_HASH"
   case "file-hash":j.Evidence.FileHash="PRIVATE_HASH"
   case "image":j.Evidence.ImageDigest="PRIVATE_IMAGE"
   case "sample":j.Evidence.SampleMS=0
   case "rate":j.Evidence.TokensPerSecond=-1
   case "context":j.Evidence.ContextTokens=0
   case "prompt":j.Evidence.PromptTokens--
   case "generated":j.Evidence.GeneratedTokens--
   case "elapsed":j.Evidence.ContextElapsedMS=0
   case "cleanup":j.Evidence.CleanupComplete=false
   case "tunnel":j.Evidence.TunnelVerified=false
   case "running-evidence":j.State="running"
   case "running-code":j.State="running";j.Evidence=nil;j.FailureCode="preflight"
   case "passed-code":j.FailureCode="cleanup"
   case "active-code":j.State="active";j.FailureCode="service_start"
   case "activating-code":j.State="activating";j.FailureCode="service_start"
   case "failed-code":j.State="failed";j.Evidence=nil;j.FailureCode="PRIVATE_CODE"
   case "class-band-evidence":j.State="failed";j.FailureCode="class_band";j.Evidence=nil
   case "failed-evidence":j.State="failed";j.FailureCode="workload"
   case "wrong-plan":j.PlanSHA256=strings.Repeat("e",64)
   case "wrong-run":j.RunID="PRIVATE_RUN"
   case "timestamp":j.RecordedAt=time.Time{}
   case "schema":j.Schema=2
   case "unknown-state":j.State="PRIVATE_STATE"
   case "missing":want="missing"
   case "symlink","hardlink","unsafe-mode":want="unavailable"
   }
   raw,_:=json.Marshal(j)
   switch kind {
   case "corrupt":raw=[]byte("PRIVATE_CORRUPT")
   case "unknown-field":raw=append(raw[:len(raw)-1],[]byte(`,"private":"PRIVATE_FIELD"}`)...)
   case "duplicate":raw=[]byte(`{"schema":1,"Schema":1}`)
   }
   if err:=os.WriteFile(path,raw,0600);err!=nil{t.Fatal(err)}
   if kind=="missing"||kind=="symlink"{if err:=os.Remove(path);err!=nil{t.Fatal(err)}}
   if kind=="symlink"{if err:=os.Symlink(filepath.Join(dir,"absent"),path);err!=nil{t.Fatal(err)}}
   if kind=="hardlink"{if err:=os.Link(path,path+".copy");err!=nil{t.Fatal(err)}}
   if kind=="unsafe-mode"{if err:=os.Chmod(path,0644);err!=nil{t.Fatal(err)}}
   bundle,err:=collectSupportWithCertification(context.Background(),dir,"","","",sha);if err!=nil{t.Fatal(err)}
   stage:=bundle.Stages[6]
   if bundle.CollectionComplete||stage.Collection!=want||stage.Certification!=nil{t.Fatalf("%+v",stage)}
   exported,_:=json.Marshal(bundle)
   if bytes.Contains(exported,[]byte("PRIVATE_")){t.Fatal("private text leaked")}
  })
 }
 // JSON cannot encode these values, but validate directly as well.
 for _,value:=range []float64{math.NaN(),math.Inf(1),math.Inf(-1)} {
  _,j:=supportCertificationFixture(t);j.Evidence.TokensPerSecond=value
  if validSupportCertification(j){t.Fatal("non-finite throughput")}
  j.Evidence.TokensPerSecond=12;j.Evidence.ThermalC=&value
  if validSupportCertification(j){t.Fatal("non-finite thermal reading")}
 }
}
func TestCertificationStatusAndSupportCLI(t *testing.T) {
 dir,j:=supportCertificationFixture(t);writeSupportCertificationFixture(t,dir,j)
 args:=[]string{"--plan-sha",j.PlanSHA256}
 var out,diagnostic bytes.Buffer
 if code:=runCertificationStatus(context.Background(),args,&out,&diagnostic,dir,true);code!=0{t.Fatalf("%d: %s",code,diagnostic.String())}
 var status supportBundle;if err:=json.Unmarshal(out.Bytes(),&status);err!=nil{t.Fatal(err)}
 if !status.HistoricalOnly||status.Stages[6].State!="passed"{t.Fatal("wrong status")}
 output:=filepath.Join(dir,"support.json")
 if runSupportBundle(context.Background(),[]string{"--certification-plan-sha",j.PlanSHA256,"--output",output},&out,&diagnostic,dir,true)!=0{t.Fatal(diagnostic.String())}
 raw,err:=os.ReadFile(output);if err!=nil{t.Fatal(err)}
 var bundle supportBundle;if err=json.Unmarshal(raw,&bundle);err!=nil{t.Fatal(err)}
 if !sameSetupJSON(status.Stages,bundle.Stages){t.Fatal("status and support disagree")}
 unlock,err:=lockSetupState(dir);if err!=nil{t.Fatal(err)}
 if runCertificationStatus(context.Background(),args,&out,&diagnostic,dir,true)!=1{t.Fatal("read concurrent writer")}
 unlock()
 ctx,cancel:=context.WithCancel(context.Background());cancel()
 if runCertificationStatus(ctx,args,&out,&diagnostic,dir,true)!=1{t.Fatal("ignored cancellation")}
 if runCertificationStatus(context.Background(),args,&out,&diagnostic,dir,false)!=1{t.Fatal("non-root accepted")}
 if runCertificationStatus(context.Background(),[]string{"--plan-sha","../private"},&out,&diagnostic,dir,true)!=2{t.Fatal("unsafe input")}
 if runCertificationStatus(context.Background(),[]string{"--help"},&out,&diagnostic,dir,false)!=0{t.Fatal("help failed")}
 if err=os.Remove(certificationStatePath(dir,j.PlanSHA256));err!=nil{t.Fatal(err)}
 out.Reset()
 if runCertificationStatus(context.Background(),args,&out,&diagnostic,dir,true)!=1{t.Fatal("missing state marked complete")}
 if !bytes.Contains(out.Bytes(),[]byte(`"collection_complete":false`)){t.Fatal("missing incomplete report")}
 output=filepath.Join(dir,"partial.json")
 if runSupportBundle(context.Background(),[]string{"--certification-plan-sha",j.PlanSHA256,"--output",output},&out,&diagnostic,dir,true)!=1{t.Fatal("partial bundle succeeded")}
 raw,err=os.ReadFile(output)
 if err!=nil||!bytes.Contains(raw,[]byte(`"collection_complete": false`)){t.Fatal("partial bundle not saved")}
}
