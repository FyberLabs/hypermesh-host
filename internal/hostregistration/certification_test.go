package hostregistration

import (
 "context"
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"
 "time"
)
func TestCertificationResponseBinding(t *testing.T){
 for _,kind:=range []string{"pass","nonce","tenant","device","manifest","selling","permit"}{
  t.Run(kind,func(t *testing.T){
   r:=fixture(t);r.ClassID="agx-large";id,_:=NewRunID()
   m:=CertificationManifest{SuiteID:"pack-v1",CatalogID:"model",ClassID:r.ClassID,FileHash:"sha256:"+strings.Repeat("a",64),
    ImageDigest:"example.test/runtime@sha256:"+strings.Repeat("b",64),SourceRef:"unused",SizeBytes:12,
    Runtime:"nvidia",Backend:"cuda-jetson",ContextTokens:4096,FullContext:true,DecodeTokS:10}
   prev:=CertificationRun{RunID:id,Manifest:m,ManifestSHA256:strings.Repeat("c",64)}
   server:=httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter,q *http.Request){
    if q.Header.Get("Authorization")!="Bearer hm_dev_"+strings.Repeat("s",32){t.Error("wrong credential")}
    var body map[string]any;json.NewDecoder(q.Body).Decode(&body)
    until:=time.Now().Add(time.Minute)
    s:=CertificationRun{ProtocolVersion:1,Nonce:body["nonce"].(string),RunID:id,TenantID:testTenant,
     DeviceID:r.RequestID,RequestID:r.RequestID,Manifest:m,ManifestSHA256:prev.ManifestSHA256,
     State:"passed",ExpiresAt:time.Now().Add(time.Hour),SellingAllowed:flagValue(false),ActivationAllowed:flagValue(true),PermitExpiresAt:&until}
    switch kind{
    case "nonce":s.Nonce="stale"
    case "tenant":s.TenantID=r.RequestID
    case "device":s.DeviceID=id
    case "manifest":s.Manifest.ContextTokens++
    case "selling":s.SellingAllowed=flagValue(true)
    case "permit":s.PermitExpiresAt=nil
    }
    json.NewEncoder(w).Encode(s)
   }));defer server.Close()
   c,_:=New(server.URL,testTenant);c.http=server.Client()
   _,err:=c.ActivationPermit(context.Background(),r,r.RequestID,"hm_dev_"+strings.Repeat("s",32),prev)
   if (err==nil)!=(kind=="pass"){t.Fatalf("%s: %v",kind,err)}
  })
 }
}
