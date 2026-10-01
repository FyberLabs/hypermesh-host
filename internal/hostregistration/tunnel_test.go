package hostregistration

import (
 "context"
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"
)

func TestPrepareTunnelBindsSavedIdentityAndAllocation(t *testing.T){
 for _,bad:=range []string{"","nonce","peer","route","lease","endpoint","selling","capability"}{t.Run(bad,func(t *testing.T){
  r:=fixture(t);secret:="hm_dev_"+strings.Repeat("s",32)
  server:=httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter,req *http.Request){
   if req.URL.Path!="/api/v1/hypermesh/agent/onboarding/tunnel"||req.Header.Get("Authorization")!="Bearer "+secret{t.Error("wrong authenticated operation")}
   var q onboardingRequest
   if json.NewDecoder(req.Body).Decode(&q)!=nil{t.Error("bad body")}
   s:=snapshotFixture(r,q);s.Capabilities=append(s.Capabilities,"onboarding_tunnel_v1")
   peer:=OnboardingTunnel{TunnelID:r.RequestID,Status:"allocated",Endpoint:"wg.example:51820",
    ServerPublicKey:r.WGPublicKey,PeerPublicKey:r.WGPublicKey,ClientAddress:"10.77.0.2/32",ProbeAddress:"10.77.0.1"}
   switch bad {
   case "nonce":s.Nonce="replay"
   case "peer":peer.PeerPublicKey="other"
   case "route":peer.ClientAddress="0.0.0.0/0"
   case "lease":lease:=r.RequestID;peer.LeaseID=&lease
   case "endpoint":peer.Endpoint="wg.example\nPostUp=bad:51820"
   case "selling":s.SellState="selling"
   case "capability":s.Capabilities=s.Capabilities[:len(s.Capabilities)-1]
   }
   _=json.NewEncoder(w).Encode(TunnelReceipt{s,peer})
  }));defer server.Close()
  c,_:=New(server.URL,testTenant);c.http=server.Client()
  _,err:=c.PrepareTunnel(context.Background(),r,r.RequestID,secret)
  if (err==nil)!=(bad==""){t.Fatalf("bad=%q err=%v",bad,err)}
 })}
}
