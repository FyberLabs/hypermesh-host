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
)

func heartbeatFixture(r Request,q heartbeatRequest)HeartbeatReceipt {
 snapshot:=snapshotFixture(r,q.onboardingRequest)
 snapshot.Capabilities=append(snapshot.Capabilities,"onboarding_heartbeat_v1")
 at:=snapshot.CheckedAt
 snapshot.LastHeartbeatAt=&at
 uptime:=q.UptimeS
 return HeartbeatReceipt{snapshot,"recorded",&at,&uptime}
}
func TestHeartbeatUsesDeviceCredentialAndFreshBoundAcknowledgment(t *testing.T){
 r:=fixture(t);secret:="hm_dev_"+strings.Repeat("s",43)
 nonces:=map[string]bool{}
 server:=httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter,req *http.Request){
  if req.Method!="POST"||req.URL.Path!="/prefix/api/v1/hypermesh/agent/onboarding/heartbeat"||
   req.Header.Get("Authorization")!="Bearer "+secret||req.Header.Get("X-Tenant-ID")!=testTenant{t.Error("wrong heartbeat authority")}
  var q heartbeatRequest
  if json.NewDecoder(req.Body).Decode(&q)!=nil||q.UptimeS!=125||len(q.Nonce)!=64{t.Error("bad heartbeat request")}
  if nonces[q.Nonce]{t.Error("reused heartbeat nonce")};nonces[q.Nonce]=true
  json.NewEncoder(w).Encode(heartbeatFixture(r,q))
 }));defer server.Close()
 c,_:=New(server.URL+"/prefix",testTenant);c.http=server.Client();c.bearer="unused-human-token"
 for i:=0;i<2;i++{if _,err:=c.HeartbeatDevice(context.Background(),r,r.RequestID,secret,125);err!=nil{t.Fatal(err)}}
 if len(nonces)!=2{t.Fatal("missing independent observations")}
}
func TestHeartbeatRejectsUncommittedOrMismatchedReceipts(t *testing.T){
 r:=fixture(t);q:=heartbeatRequest{onboardingRequest{1,r.RequestID,r.RequestID,strings.Repeat("a",64)},125}
 c,_:=New("https://plane.example",testTenant)
 changes:=[]func(*HeartbeatReceipt){
  func(s *HeartbeatReceipt){s.Nonce="old"},
  func(s *HeartbeatReceipt){s.DeviceID="other"},
  func(s *HeartbeatReceipt){s.RequestID="other"},
  func(s *HeartbeatReceipt){s.TenantID="other"},
  func(s *HeartbeatReceipt){s.KeyFingerprint="other"},
  func(s *HeartbeatReceipt){s.Capabilities=[]string{"device_identity_v1","readiness_snapshot_v1","aiuditor_program_v1"}},
  func(s *HeartbeatReceipt){s.HeartbeatState=""},
  func(s *HeartbeatReceipt){s.HeartbeatState="pending"},
  func(s *HeartbeatReceipt){s.HeartbeatRecordedAt=nil},
  func(s *HeartbeatReceipt){s.HeartbeatRecordedAt=&time.Time{}},
  func(s *HeartbeatReceipt){s.HeartbeatUptimeS=nil},
  func(s *HeartbeatReceipt){wrong:=int64(126);s.HeartbeatUptimeS=&wrong},
  func(s *HeartbeatReceipt){s.LastHeartbeatAt=nil},
  func(s *HeartbeatReceipt){wrong:=s.CheckedAt.Add(-time.Minute);s.LastHeartbeatAt=&wrong},
  func(s *HeartbeatReceipt){s.CheckedAt=s.CheckedAt.Add(time.Minute)},
  func(s *HeartbeatReceipt){s.SellState="selling"},
  func(s *HeartbeatReceipt){s.PathBState="green"},
  func(s *HeartbeatReceipt){s.PathAState="rejected"},
  func(s *HeartbeatReceipt){s.ScheduleHold=flagValue(true)},
  func(s *HeartbeatReceipt){s.ClassAvailable=flagValue(false)},
  func(s *HeartbeatReceipt){s.ActivationAllowed=flagValue(true)},
  func(s *HeartbeatReceipt){s.SellingAllowed=flagValue(true)},
  func(s *HeartbeatReceipt){s.TunnelVerified=flagValue(true)},
  func(s *HeartbeatReceipt){s.AIuditorProgramState="missing_suite"},
 }
 for i,change:=range changes {
  s:=heartbeatFixture(r,q);change(&s)
  if c.checkHeartbeat(s,r,q)==nil{t.Errorf("accepted mutation %d",i)}
 }
}
func TestHeartbeatRejectsInvalidUptimeWithoutSending(t *testing.T){
 r:=fixture(t);c,_:=New("https://plane.example",testTenant)
 for _,uptime:=range []int64{-1,1<<31}{
  if _,err:=c.HeartbeatDevice(context.Background(),r,r.RequestID,"hm_dev_"+strings.Repeat("s",32),uptime);err==nil{t.Fatal("invalid uptime sent")}
 }
}
func TestHeartbeatErrorsAndLostResponsesRemainRetryableAndRedacted(t *testing.T){
 for _,status:=range []int{401,403,404,409,410,422,503}{t.Run(fmt.Sprint(status),func(t *testing.T){
  r:=fixture(t);calls:=0
  server:=httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter,req *http.Request){
   calls++
   if calls==1 {http.Error(w,"private-response",status);return}
   var q heartbeatRequest
   json.NewDecoder(req.Body).Decode(&q)
   json.NewEncoder(w).Encode(heartbeatFixture(r,q))
  }));defer server.Close()
  c,_:=New(server.URL,testTenant);c.http=server.Client()
  s,err:=c.HeartbeatDevice(context.Background(),r,r.RequestID,"hm_dev_"+strings.Repeat("s",32),0)
  if err==nil||strings.Contains(err.Error(),"private-response")||s.HeartbeatRecordedAt!=nil{t.Fatal("failed response became evidence",err)}
  if _,err=c.HeartbeatDevice(context.Background(),r,r.RequestID,"hm_dev_"+strings.Repeat("s",32),0);err!=nil{t.Fatal("retry failed",err)}
 })}
}
func TestHeartbeatDeadlineCoversBody(t *testing.T){
 r:=fixture(t)
 server:=httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter,req *http.Request){
  w.WriteHeader(200);w.Write([]byte("{"));w.(http.Flusher).Flush()
  <-req.Context().Done()
 }));defer server.Close()
 c,_:=New(server.URL,testTenant);c.http=server.Client();c.timeout=40*time.Millisecond
 start:=time.Now()
 if _,err:=c.HeartbeatDevice(context.Background(),r,r.RequestID,"hm_dev_"+strings.Repeat("s",32),1);err==nil{t.Fatal("unfinished receipt accepted")}
 if time.Since(start)>time.Second{t.Fatal("body deadline not applied")}
}
