package hostregistration

import (
 "context"
 "encoding/json"
 "fmt"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"
 "time"

 "github.com/FyberLabs/hypermesh-host/internal/tunnel"
)
const testTenant="12345678-1234-4234-8234-123456789abc"
func fixture(t *testing.T)Request {
 t.Helper();_,pub,err:=tunnel.GenerateKeypair();if err!=nil{t.Fatal(err)}
 return Request{RequestID:"12345678-1234-4234-8234-123456789def",HostName:"test-host",ClassID:"agx-large",
 Hardware:Hardware{"ubuntu-jetson-cuda","arm64","ARM","Tegra",64<<30,32<<30},
 WGPublicKey:pub,DeviceSecretHash:strings.Repeat("a",64),CredentialPrefix:"abcdefgh"}
}
func status(r Request,state string)Status {
 s:=Status{RequestID:r.RequestID,TenantID:testTenant,State:state,HostName:r.HostName,ClassID:r.ClassID,Hardware:r.Hardware,KeyFingerprint:Fingerprint(r.WGPublicKey),ExpiresAt:"2030-01-01T00:00:00Z",ApprovalPath:"/dashboard/hypermesh/host/approve/"+r.RequestID}
 if state=="completed"{id:="12345678-1234-4234-8234-123456789aaa";s.DeviceID=&id}
 return s
}
func TestLoginAndBoundRegistration(t *testing.T) {
 r:=fixture(t);bound:=false;grants:=0
 server:=httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter,q *http.Request){
  if q.Header.Get("X-Tenant-ID")!=testTenant{t.Error("missing tenant")}
  switch q.URL.Path {
  case "/api/v1/auth/me":if q.Header.Get("Authorization")!="Bearer raw-private"{t.Error("missing device token")};fmt.Fprint(w,`{"sub":"verified-owner"}`)
  case "/api/v1/auth/enrollment/grant":
   grants++;if !bound{t.Error("grant before durable owner binding")}
   var body map[string]any;json.NewDecoder(q.Body).Decode(&body)
   if body["tenant_id"]!=testTenant{t.Error("wrong grant tenant")}
   fmt.Fprint(w,`{"granted":["host"],"refused":[],"device_enrolled":false}`)
  case "/api/v1/auth/switch-tenant":fmt.Fprint(w,`{"access_token":"session-private","token_type":"Bearer","expires_in":900}`)
  default:
   if q.Header.Get("Authorization")!="Bearer session-private"{t.Error("raw token used for registration")}
   s:=status(r,"pending")
   if q.URL.Path=="/api/v1/hypermesh/host-registrations"{
    var submitted Request
    if json.NewDecoder(q.Body).Decode(&submitted)!=nil||submitted!=r{t.Error("registration body changed")}
   }
   if strings.HasSuffix(q.URL.Path,"/complete"){s=status(r,"completed")}
   json.NewEncoder(w).Encode(s)
  }
 }));defer server.Close()
 c,_:=New(server.URL,testTenant);c.http=server.Client()
 if err:=c.Login(context.Background(),"raw-private",func(owner string)error{if owner!="verified-owner"{t.Fatal(owner)};bound=true;return nil});err!=nil{t.Fatal(err)}
 if grants!=1{t.Fatal(grants)}
 if _,err:=c.Create(context.Background(),r);err!=nil{t.Fatal(err)}
 if _,err:=c.Get(context.Background(),r);err!=nil{t.Fatal(err)}
 if s,err:=c.Complete(context.Background(),r);err!=nil||s.DeviceID==nil{t.Fatal(s,err)}
 raw,_:=json.Marshal(c)
 if strings.Contains(fmt.Sprintf("%v %#v %s",c,c,raw),"session-private"){t.Fatal("client leaked bearer")}
}
func TestOwnerMismatchPreventsGrant(t *testing.T){
 count:=0
 server:=httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){count++;fmt.Fprint(w,`{"sub":"different-owner"}`)}));defer server.Close()
 c,_:=New(server.URL,testTenant);c.http=server.Client()
 if c.Login(context.Background(),"private",func(string)error{return fmt.Errorf("different owner")})==nil||count!=1{t.Fatal("mutation after owner refusal",count)}
}
func TestResponseBinding(t *testing.T){
 r:=fixture(t);c,_:=New("https://plane.example",testTenant)
 changes:=[]func(*Status){
 func(s *Status){s.TenantID="other"},func(s *Status){s.RequestID="other"},func(s *Status){s.HostName="other"},
 func(s *Status){s.ClassID="other"},func(s *Status){s.Hardware.ModelMemoryBytes++},func(s *Status){s.KeyFingerprint="other"},
 func(s *Status){s.ApprovalPath="https://attacker.example"},func(s *Status){s.ExpiresAt="invalid"},func(s *Status){s.State="selling"},
 func(s *Status){id:="invalid";s.DeviceID=&id},
 }
 for i,change:=range changes{s:=status(r,"pending");change(&s);if c.check(s,r)==nil{t.Errorf("accepted mutation %d",i)}}
}
func TestTransportBoundAndRedacted(t *testing.T){
 for _,mode:=range []string{"error","redirect","large","trickle","bad-json"}{t.Run(mode,func(t *testing.T){
  server:=httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
   switch mode {
   case "error":http.Error(w,"secret-response-private",403)
   case "redirect":http.Redirect(w,r,"https://attacker.example/secret",302)
   case "large":fmt.Fprint(w,strings.Repeat("x",(1<<20)+1))
   case "bad-json":fmt.Fprint(w,"secret-response-private")
   case "trickle":
    w.WriteHeader(200);w.(http.Flusher).Flush()
    ticker:=time.NewTicker(10*time.Millisecond);defer ticker.Stop()
    for{select{case <-r.Context().Done():return;case <-ticker.C:fmt.Fprint(w," ");w.(http.Flusher).Flush()}}
   }
  }));defer server.Close()
  c,_:=New(server.URL,testTenant);c.http=server.Client();c.timeout=80*time.Millisecond;c.bearer="secret-token-private"
  start:=time.Now();_,err:=c.Create(context.Background(),fixture(t))
  if err==nil||strings.Contains(err.Error(),"private")||strings.Contains(err.Error(),"attacker"){t.Fatal("unsafe error",err)}
  if time.Since(start)>time.Second{t.Fatal("response exceeded deadline")}
 })}
}
func TestOriginAndRequestValidation(t *testing.T){
 for _,s:=range []string{"http://plane.example","https://user:pass@plane.example","https://plane.example/path","https://plane.example?","https://plane.example/#frag","https://plane.example/\n"}{if Origin(s){t.Fatal(s)}}
 r:=fixture(t);r.WGPublicKey=strings.Repeat("A",43)+"=";if r.Validate()==nil{t.Fatal("zero public key")}
}
