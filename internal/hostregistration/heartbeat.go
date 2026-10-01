package hostregistration

import (
 "context"
 "crypto/rand"
 "encoding/hex"
 "fmt"
 "time"
)

// HeartbeatReceipt proves a single committed HTTPS observation, not a running
// service, verified tunnel, certification or continued host availability.
type HeartbeatReceipt struct {
 OnboardingSnapshot
 HeartbeatState string `json:"heartbeat_state"`
 HeartbeatRecordedAt *time.Time `json:"heartbeat_recorded_at"`
 HeartbeatUptimeS *int64 `json:"heartbeat_uptime_s"`
}
type heartbeatRequest struct {
 onboardingRequest
 UptimeS int64 `json:"uptime_s"`
}
func (s OnboardingSnapshot) SupportsHeartbeat()bool {
 for _,capability:=range s.Capabilities {if capability=="onboarding_heartbeat_v1"{return true}}
 return false
}

// HeartbeatDevice is deliberately separate from CheckDevice: it writes liveness.
func (c *Client) HeartbeatDevice(ctx context.Context,r Request,deviceID,secret string,uptime int64)(HeartbeatReceipt,error) {
 var out HeartbeatReceipt
 if err:=r.Validate();err!=nil{return out,err}
 if !UUID.MatchString(deviceID)||!onboardingSecretRE.MatchString(secret)||uptime<0||uptime>1<<31-1 {
  return out,fmt.Errorf("saved device identity or measured uptime is invalid")
 }
 challenge:=make([]byte,32)
 if _,err:=rand.Read(challenge);err!=nil{return out,fmt.Errorf("cannot generate heartbeat challenge")}
 body:=heartbeatRequest{onboardingRequest{1,r.RequestID,deviceID,hex.EncodeToString(challenge)},uptime}
 if err:=c.request(ctx,"POST","/api/v1/hypermesh/agent/onboarding/heartbeat",secret,body,&out);err!=nil{return HeartbeatReceipt{},err}
 if err:=c.checkHeartbeat(out,r,body);err!=nil{return HeartbeatReceipt{},err}
 return out,nil
}
func (c *Client) checkHeartbeat(s HeartbeatReceipt,r Request,q heartbeatRequest)error {
 if err:=c.checkOnboarding(s.OnboardingSnapshot,r,q.onboardingRequest);err!=nil{return err}
 if !s.SupportsHeartbeat() {return fmt.Errorf("plane lacks onboarding heartbeat capability")}
 if s.AIuditorProgramState!="ready" {return fmt.Errorf("AIuditor program blocks heartbeat: %s; rerun check-agent for guidance",s.AIuditorProgramState)}
 if s.HeartbeatState!="recorded"||s.HeartbeatRecordedAt==nil||s.HeartbeatRecordedAt.IsZero()||
 s.HeartbeatUptimeS==nil||*s.HeartbeatUptimeS!=q.UptimeS||s.LastHeartbeatAt==nil||
 !s.LastHeartbeatAt.Equal(*s.HeartbeatRecordedAt)||!s.CheckedAt.Equal(*s.HeartbeatRecordedAt)||
 !*s.ClassAvailable||*s.ScheduleHold||s.SellState!="paused"||s.PathBState!="never-run"||
 !oneOf(s.PathAState,"pending","accepted") {
  return fmt.Errorf("heartbeat acknowledgment is incomplete or does not match this inactive host")
 }
 return nil
}
