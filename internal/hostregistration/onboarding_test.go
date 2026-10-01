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
func flagValue(value bool)*bool{return &value}
func snapshotFixture(r Request,q onboardingRequest)OnboardingSnapshot {
 return OnboardingSnapshot{Protocol:"hypermesh-host-onboarding",ProtocolVersion:1,
 AIuditorProgramState:"ready",Capabilities:[]string{"device_identity_v1","readiness_snapshot_v1","aiuditor_program_v1"},Nonce:q.Nonce,CheckedAt:time.Now().UTC(),
 TenantID:testTenant,RequestID:r.RequestID,DeviceID:q.DeviceID,HostName:r.HostName,ClassID:r.ClassID,
 ClassAvailable:flagValue(true),KeyFingerprint:Fingerprint(r.WGPublicKey),PathAState:"pending",
 PathBState:"never-run",SellState:"paused",ScheduleHold:flagValue(false),
 ActivationAllowed:flagValue(false),SellingAllowed:flagValue(false),TunnelVerified:flagValue(false)}
}
func TestOnboardingUsesSavedCredentialAndFreshChallenge(t *testing.T){
 r:=fixture(t);deviceID:=r.RequestID;secret:="hm_dev_"+strings.Repeat("s",32)
 seen:=map[string]bool{}
 server:=httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter,req *http.Request){
  if req.Method!="POST"||req.URL.Path!="/prefix/api/v1/hypermesh/agent/onboarding/check"||
  req.Header.Get("Authorization")!="Bearer "+secret||req.Header.Get("X-Tenant-ID")!=testTenant{t.Error("wrong device request")}
  var q onboardingRequest
  if json.NewDecoder(req.Body).Decode(&q)!=nil||q.ProtocolVersion!=1||q.RequestID!=r.RequestID||q.DeviceID!=deviceID||len(q.Nonce)!=64{t.Error("bad body")}
  if seen[q.Nonce]{t.Error("reused challenge")};seen[q.Nonce]=true
  json.NewEncoder(w).Encode(snapshotFixture(r,q))
 }));defer server.Close()
 c,_:=New(server.URL+"/prefix",strings.ToUpper(testTenant));c.http=server.Client();c.bearer="human-token-must-not-be-used"
 for i:=0;i<2;i++{if _,err:=c.CheckDevice(context.Background(),r,deviceID,secret);err!=nil{t.Fatal(err)}}
 if len(seen)!=2{t.Fatal("missing fresh requests")}
}
func TestOnboardingRejectsUnboundOrIncompleteResponses(t *testing.T){
 r:=fixture(t);q:=onboardingRequest{1,r.RequestID,r.RequestID,strings.Repeat("a",64)}
 c,_:=New("https://plane.example",testTenant)
 changes:=[]func(*OnboardingSnapshot){
 func(s *OnboardingSnapshot){s.AIuditorProgramState=""},
 func(s *OnboardingSnapshot){s.AIuditorProgramState="forged"},
 func(s *OnboardingSnapshot){s.Capabilities=[]string{"device_identity_v1","readiness_snapshot_v1"}},
 func(s *OnboardingSnapshot){s.Protocol="health"},
 func(s *OnboardingSnapshot){s.ProtocolVersion=2},
 func(s *OnboardingSnapshot){s.Capabilities=[]string{"device_identity_v1"}},
 func(s *OnboardingSnapshot){s.Capabilities=append(s.Capabilities,"device_identity_v1")},
 func(s *OnboardingSnapshot){s.Nonce="old-check"},
 func(s *OnboardingSnapshot){s.TenantID="other"},
 func(s *OnboardingSnapshot){s.RequestID="other"},
 func(s *OnboardingSnapshot){s.DeviceID="other"},
 func(s *OnboardingSnapshot){s.HostName="other"},
 func(s *OnboardingSnapshot){s.ClassID="other"},
 func(s *OnboardingSnapshot){s.KeyFingerprint="other"},
 func(s *OnboardingSnapshot){s.CheckedAt=time.Time{}},
 func(s *OnboardingSnapshot){s.ClassAvailable=nil},
 func(s *OnboardingSnapshot){s.ScheduleHold=nil},
 func(s *OnboardingSnapshot){s.ActivationAllowed=nil},
 func(s *OnboardingSnapshot){s.SellingAllowed=nil},
 func(s *OnboardingSnapshot){s.TunnelVerified=nil},
 func(s *OnboardingSnapshot){s.ActivationAllowed=flagValue(true)},
 func(s *OnboardingSnapshot){s.SellingAllowed=flagValue(true)},
 func(s *OnboardingSnapshot){s.TunnelVerified=flagValue(true)},
 func(s *OnboardingSnapshot){s.PathAState="forged"},
 func(s *OnboardingSnapshot){s.PathBState="forged"},
 func(s *OnboardingSnapshot){s.SellState="forged"},
 }
 for i,change:=range changes{s:=snapshotFixture(r,q);change(&s);if c.checkOnboarding(s,r,q)==nil{t.Errorf("accepted response mutation %d",i)}}
}
func TestOnboardingOldPlaneAndCredentialFailuresStayRedacted(t *testing.T){
 for _,code:=range []int{401,403,404,409,410,422,503}{t.Run(fmt.Sprint(code),func(t *testing.T){
  server:=httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter,req *http.Request){http.Error(w,"private-response-secret",code)}));defer server.Close()
  c,_:=New(server.URL,testTenant);c.http=server.Client();r:=fixture(t)
  _,err:=c.CheckDevice(context.Background(),r,r.RequestID,"hm_dev_"+strings.Repeat("s",32))
  if err==nil||strings.Contains(err.Error(),"private-response-secret"){t.Fatal("failure accepted or leaked",err)}
 })}
}

func TestOnboardingAcceptsLongerRegisteredCredentials(t *testing.T){
 r:=fixture(t);secret:="hm_dev_"+strings.Repeat("s",43)
 server:=httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter,req *http.Request){
  if req.Header.Get("Authorization")!="Bearer "+secret{t.Error("changed longer credential")}
  var q onboardingRequest
  if json.NewDecoder(req.Body).Decode(&q)!=nil{t.Error("invalid request")}
  json.NewEncoder(w).Encode(snapshotFixture(r,q))
 }));defer server.Close()
 c,_:=New(server.URL,testTenant);c.http=server.Client()
 if _,err:=c.CheckDevice(context.Background(),r,r.RequestID,secret);err!=nil{t.Fatal(err)}
}

func TestOnboardingProgramFailuresRemainBoundSnapshots(t *testing.T){
 r:=fixture(t);q:=onboardingRequest{1,r.RequestID,r.RequestID,strings.Repeat("a",64)}
 c,_:=New("https://plane.example",testTenant)
 for _,state:=range []string{"unconfigured","unavailable","unauthorized","writer_mismatch","missing_suite","invalid_response"} {
  s:=snapshotFixture(r,q);s.AIuditorProgramState=state
  if err:=c.checkOnboarding(s,r,q);err!=nil{t.Fatal("lost actionable program state",state,err)}
  if *s.ActivationAllowed||*s.SellingAllowed{t.Fatal("failed program authorized activation")}
 }
}
