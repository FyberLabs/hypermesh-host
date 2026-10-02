//go:build linux

package installer

import (
 "bytes"
 "context"
 "crypto/sha256"
 "fmt"
 "io"
 "net/http"
 "net/http/httptest"
 "net/url"
 "os"
 "path/filepath"
 "strings"
 "testing"
 "time"

 "github.com/FyberLabs/hypermesh-host/internal/hostregistration"
)

func TestAcquisitionIntegrityAndNoReplacement(t *testing.T){
 payload:=[]byte("GGUF-reviewed-test-data")
 pin:=fmt.Sprintf("sha256:%x",sha256.Sum256(payload))
 for _,kind:=range []string{"pass","short","extra","hash","existing","symlink","cancel"}{
  t.Run(kind,func(t *testing.T){
   dir:=t.TempDir();path:=filepath.Join(dir,"model.gguf")
   body:=payload
   if kind=="short"{body=body[:3]};if kind=="extra"{body=append(append([]byte{},body...),1)}
   if kind=="hash"{body=bytes.Repeat([]byte("x"),len(payload))}
   if kind=="existing"{if err:=os.WriteFile(path,[]byte("KEEP"),0600);err!=nil{t.Fatal(err)}}
   if kind=="symlink"{if err:=os.Symlink(filepath.Join(dir,"absent"),path);err!=nil{t.Fatal(err)}}
   ctx,cancel:=context.WithCancel(context.Background());defer cancel();if kind=="cancel"{cancel()}
   err:=acquireModel(ctx,path,pin,int64(len(payload)),func(context.Context)(io.ReadCloser,error){return io.NopCloser(bytes.NewReader(body)),nil})
   if (err==nil)!=(kind=="pass"){t.Fatalf("%s: %v",kind,err)}
   entries,_:=os.ReadDir(dir)
   for _,entry:=range entries{if strings.HasPrefix(entry.Name(),".certification-model-"){t.Fatal("partial leaked")}}
   if kind=="pass"{
    if verifyAcquiredModel(context.Background(),path,pin,int64(len(payload)))!=nil{t.Fatal("bad artifact")}
    if acquireModel(context.Background(),path,pin,int64(len(payload)),func(context.Context)(io.ReadCloser,error){t.Fatal("downloaded existing verified bytes");return nil,nil})!=nil{t.Fatal("reuse failed")}
   }
   if kind=="existing"{raw,_:=os.ReadFile(path);if string(raw)!="KEEP"{t.Fatal("replaced model")}}
  })
 }
}

func TestModelSourceAndRedirectCredentialScope(t *testing.T){
 valid:="bartowski/reviewed@"+strings.Repeat("a",40)+"/model.gguf"
 if _,err:=certificationURL(valid);err!=nil{t.Fatal(err)}
 for _,ref:=range []string{"https://evil.test/model.gguf","bartowski/reviewed@main/model.gguf",strings.Replace(valid,"/model.gguf","/../model.gguf",1),strings.Replace(valid,"bartowski","unknown",1)}{
  if _,err:=certificationURL(ref);err==nil{t.Fatal("untrusted source accepted")}
 }
 client:=modelTransport("PRIVATE_TOKEN")
 for _,address:=range []string{"https://cas-bridge.xethub.hf.co/object","https://evil.test/object","http://huggingface.co/object"}{
  u,_:=url.Parse(address);req:=&http.Request{URL:u,Header:make(http.Header)};req.Header.Set("Authorization","Bearer PRIVATE_TOKEN")
  err:=client.CheckRedirect(req,[]*http.Request{{}})
  if strings.HasPrefix(address,"https://cas-"){if err!=nil||req.Header.Get("Authorization")!=""{t.Fatal("CDN credential leakage")}}else if err==nil{t.Fatal("unsafe redirect accepted")}
 }
}
func TestModelTransferWholeDeadline(t *testing.T){
 server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){w.Write([]byte("G"));w.(http.Flusher).Flush();<-r.Context().Done()}));defer server.Close()
 ctx,cancel:=context.WithTimeout(context.Background(),30*time.Millisecond);defer cancel()
 start:=time.Now();path:=filepath.Join(t.TempDir(),"model.gguf")
 err:=acquireModel(ctx,path,"sha256:"+strings.Repeat("a",64),8,func(ctx context.Context)(io.ReadCloser,error){return modelBytes(ctx,server.Client(),server.URL,"")})
 if err==nil||time.Since(start)>time.Second{t.Fatal("transfer escaped deadline")}
}

func TestCertificationUploadRetryPreservesEvidence(t *testing.T){
 id,_:=hostregistration.NewRunID()
 p:=certificationPlan{SHA256:strings.Repeat("a",64),Run:hostregistration.CertificationRun{RunID:id}}
 var j,durable certificationJournal
 calls:=0;reportFails:=true
 d:=certificationDeps{
  Preflight:func(context.Context)error{return nil},Acquire:func(context.Context)error{calls++;return nil},
  Workload:func(context.Context)(hostregistration.CertificationEvidence,error){return hostregistration.CertificationEvidence{SampleMS:123},nil},
  Tunnel:func(context.Context)error{return nil},
  Report:func(_ context.Context,e hostregistration.CertificationEvidence)(bool,error){
   if durable.State!="evidence-ready"||e.SampleMS!=123||!e.TunnelVerified{t.Fatal("unbound evidence")}
   if reportFails{return false,fmt.Errorf("lost reply")};return true,nil
  },
  Save:func(v certificationJournal)error{durable=v;return nil},
 }
 if performCertification(context.Background(),p,&j,d)==nil||j.State!="evidence-ready"{t.Fatal("lost reply not recoverable")}
 reportFails=false
 if err:=performCertification(context.Background(),p,&j,d);err!=nil{t.Fatal(err)}
 if calls!=1||j.State!="passed"{t.Fatal("retry reran workload")}
}
func TestCertificationNeverUploadsAfterFailedStage(t *testing.T){
 for _,stage:=range []string{"preflight","acquisition","workload","tunnel","postflight"}{
  calls:=0;var j certificationJournal
  d:=certificationDeps{
   Preflight:func(context.Context)error{calls++;if stage=="preflight"||(stage=="postflight"&&calls==3){return fmt.Errorf("fail")};return nil},
   Acquire:func(context.Context)error{if stage=="acquisition"{return fmt.Errorf("fail")};return nil},
   Workload:func(context.Context)(hostregistration.CertificationEvidence,error){if stage=="workload"{return hostregistration.CertificationEvidence{},fmt.Errorf("fail")};return hostregistration.CertificationEvidence{},nil},
   Tunnel:func(context.Context)error{if stage=="tunnel"{return fmt.Errorf("fail")};return nil},
   Report:func(context.Context,hostregistration.CertificationEvidence)(bool,error){t.Fatal("uploaded incomplete work");return true,nil},
   Save:func(certificationJournal)error{return nil},
  }
  if performCertification(context.Background(),certificationPlan{},&j,d)==nil||j.State!="failed"{t.Fatal("failure accepted",stage)}
 }
}
func TestActivationRollbackAndDurableIntent(t *testing.T){
 for _,kind:=range []string{"pass","expired","intent","publish","start","verify","save-final","rollback"}{
  t.Run(kind,func(t *testing.T){
   j:=certificationJournal{State:"passed"};state:="";published:=false;rolled:=false
   d:=activationDeps{
    Save:func(v certificationJournal)error{if kind=="intent"||(kind=="save-final"&&v.State=="active"){return fmt.Errorf("fail")};state=v.State;return nil},
    Publish:func()error{if state!="activating"{t.Fatal("publish before durable intent")};published=true;if kind=="publish"{return fmt.Errorf("fail")};return nil},
    Start:func(context.Context)error{if !published{t.Fatal("start before marker")};if kind=="start"||kind=="rollback"{return fmt.Errorf("fail")};return nil},
    Verify:func(context.Context)error{if kind=="verify"{return fmt.Errorf("fail")};return nil},
    Rollback:func(context.Context)error{rolled=true;if kind=="rollback"{return fmt.Errorf("fail")};return nil},
   }
   until:=time.Now().Add(time.Minute);if kind=="expired"{until=time.Now().Add(-time.Minute)}
   err:=startCertified(context.Background(),&j,until,d)
   if kind=="pass"{if err!=nil||j.State!="active"||rolled{t.Fatal("failed valid start")}}else{
    if err==nil||j.State=="active"{t.Fatal("failure authorized activation")}
    if kind!="expired"&&kind!="intent"&&!rolled{t.Fatal("no rollback")}
    if kind=="rollback"&&j.State!="activating"{t.Fatal("hid uncertain cleanup")}
   }
  })
 }
}

func TestCertificationJournalRejectsUnconfirmedSuccess(t *testing.T){
 id,_:=hostregistration.NewRunID()
 j:=certificationJournal{Schema:1,PlanSHA256:strings.Repeat("a",64),RunID:id,RecordedAt:time.Now()}
 for _,state:=range []string{"evidence-ready","passed","activating","active","unknown"}{
  j.State=state
  if validCertificationJournal(j){t.Fatal("accepted incomplete journal",state)}
 }
 j.State="running"
 if !validCertificationJournal(j){t.Fatal("cannot recover interrupted execution")}
}
