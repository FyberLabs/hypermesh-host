//go:build linux

package installer

import (
 "context"
 "encoding/json"
 "fmt"
 "strings"
 "testing"
 "time"

 "github.com/FyberLabs/hypermesh-host/internal/hostregistration"
)
func heartbeatSnapshot()hostregistration.OnboardingSnapshot {
 s:=onboardingFixture()
 s.Capabilities=[]string{"onboarding_heartbeat_v1"}
 return s
}
func receiptFixture()hostregistration.HeartbeatReceipt {
 s:=heartbeatSnapshot();at:=time.Now().UTC();s.CheckedAt=at;s.LastHeartbeatAt=&at
 uptime:=int64(125)
 return hostregistration.HeartbeatReceipt{OnboardingSnapshot:s,HeartbeatState:"recorded",HeartbeatRecordedAt:&at,HeartbeatUptimeS:&uptime}
}
func TestHeartbeatIntentPrecedesMutationAndKeepsAllOtherGates(t *testing.T){
 var j,durable onboardingJournal
 events:=[]string{}
 receipt:=receiptFixture()
 deps:=onboardingDeps{
  Verify:func(context.Context)error{events=append(events,"verify");return nil},
  Preflight:func(context.Context)error{events=append(events,"preflight");return nil},
  Remote:func(context.Context)(hostregistration.OnboardingSnapshot,error){events=append(events,"check");return heartbeatSnapshot(),nil},
  Heartbeat:func(context.Context)(hostregistration.HeartbeatReceipt,error){
   events=append(events,"heartbeat")
   if !durable.HeartbeatRequested||durable.State!="checking"||durable.HeartbeatRecordedAt!=nil{t.Fatal("mutation before durable intent")}
   return receipt,nil
  },
  Save:func(value onboardingJournal)error{events=append(events,"save:"+value.State);durable=value;return nil},
 }
 if err:=performOnboardingCheck(context.Background(),&j,deps);err!=nil{t.Fatal(err)}
 if strings.Join(events,",")!="save:checking,verify,preflight,check,save:checking,heartbeat,save:checked"{t.Fatal(events)}
 if !j.HeartbeatRequested||j.HeartbeatRecordedAt==nil||*j.HeartbeatUptimeS!=125||!j.LastHeartbeatAt.Equal(*receipt.HeartbeatRecordedAt){t.Fatal("missing exact acknowledgment")}
 for _,gate:=range j.Gates {
  if gate.ID=="heartbeat"&&gate.State!="passed"{t.Fatal("heartbeat not recorded")}
  if (gate.ID=="activation"||gate.ID=="selling"||gate.ID=="tunnel"||gate.ID=="path_b")&&gate.State=="passed"{t.Fatal("probe bypassed gate",gate.ID)}
 }
 if j.ActivationAllowed||j.SellingAllowed{t.Fatal("probe authorized workloads")}
 // The default read does not reuse historical probe success.
 deps.Heartbeat=nil
 if err:=performOnboardingCheck(context.Background(),&j,deps);err!=nil{t.Fatal(err)}
 if j.HeartbeatRequested||j.HeartbeatRecordedAt!=nil||j.HeartbeatUptimeS!=nil{t.Fatal("stale acknowledgment retained")}
}
func TestHeartbeatFailureRecoveryDoesNotRepeatEnrollment(t *testing.T){
 for _,failure:=range []string{"old-plane","program","lost-reply","cancel","intent-save","final-save"}{t.Run(failure,func(t *testing.T){
  var j,durable onboardingJournal
  ctx,cancel:=context.WithCancel(context.Background());defer cancel()
  writes,saves:=0,0
  deps:=onboardingDeps{
   Verify:func(context.Context)error{return nil},
   Preflight:func(context.Context)error{return nil},
   Remote:func(context.Context)(hostregistration.OnboardingSnapshot,error){
    s:=heartbeatSnapshot()
    if failure=="old-plane"{s.Capabilities=nil}
    if failure=="program"{s.AIuditorProgramState="unavailable"}
    return s,nil
   },
   Heartbeat:func(context.Context)(hostregistration.HeartbeatReceipt,error){
    writes++
    if failure=="lost-reply"{return hostregistration.HeartbeatReceipt{},fmt.Errorf("private-response-secret")}
    if failure=="cancel"{cancel()}
    return receiptFixture(),nil
   },
   Save:func(value onboardingJournal)error{
    saves++
    if (failure=="intent-save"&&saves==2)||(failure=="final-save"&&value.State=="checked"){return fmt.Errorf("fsync failed")}
    durable=value;return nil
   },
  }
  if performOnboardingCheck(ctx,&j,deps)==nil{t.Fatal("ignored failed stage")}
  expected:=0;if failure=="lost-reply"||failure=="cancel"||failure=="final-save"{expected=1}
  if writes!=expected{t.Fatal("unexpected heartbeat writes",writes)}
  if j.State!="failed"||j.HeartbeatRecordedAt!=nil||j.ActivationAllowed||j.SellingAllowed{t.Fatal("failure presented as successful heartbeat")}
  if failure=="final-save"&&durable.State!="checking"{t.Fatal("undurable acknowledgment reported complete")}
  raw,_:=json.Marshal(durable)
  if strings.Contains(string(raw),"private-response-secret"){t.Fatal("leaked raw transport failure")}
 })}
}
func TestHeartbeatUptimeBoundaries(t *testing.T){
 for _,test:=range []struct{raw string;want int64;ok bool}{
  {"0.00 0.00\n",0,true},{"125.99 999.10\n",125,true},
  {"2147483647.00 0\n",1<<31-1,true},
  {"",0,false},{"-1 0",0,false},{"NaN 0",0,false},{"+Inf 0",0,false},
  {"2147483648 0",0,false},{"1e99 0",0,false},{"123",0,false},
 } {
  got,err:=parseHeartbeatUptime(test.raw)
  if (err==nil)!=test.ok||(test.ok&&got!=test.want){t.Fatalf("%q: got %d, %v",test.raw,got,err)}
 }
}

func TestHeartbeatJournalRejectsIncompleteEvidence(t *testing.T){
 r:=receiptFixture()
 j:=onboardingJournal{State:"checked",HeartbeatRequested:true,HeartbeatRecordedAt:r.HeartbeatRecordedAt,
  HeartbeatUptimeS:r.HeartbeatUptimeS,LastHeartbeatAt:r.LastHeartbeatAt,PlaneCheckedAt:&r.CheckedAt,
  Gates:[]onboardingGate{{"heartbeat","passed","constant"}}}
 if !validHeartbeatJournal(j){t.Fatal("valid committed observation rejected")}
 changes:=[]func(*onboardingJournal){
  func(j *onboardingJournal){j.HeartbeatRequested=false},
  func(j *onboardingJournal){j.State="checking"},
  func(j *onboardingJournal){j.HeartbeatRecordedAt=nil},
  func(j *onboardingJournal){j.HeartbeatUptimeS=nil},
  func(j *onboardingJournal){j.LastHeartbeatAt=nil},
  func(j *onboardingJournal){j.PlaneCheckedAt=nil},
  func(j *onboardingJournal){j.Gates=nil},
 }
 for _,change:=range changes{copy:=j;change(&copy);if validHeartbeatJournal(copy){t.Fatal("corrupt heartbeat journal accepted")}}
}
