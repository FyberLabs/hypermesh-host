package hostregistration

import (
 "context"
 "crypto/rand"
 "encoding/hex"
 "fmt"
 "regexp"
 "time"
)

// OnboardingSnapshot is a read-only, nonce-bound response, never an activation
// permit. Pointer booleans distinguish false from an omitted protocol field.
type OnboardingSnapshot struct {
 Protocol string `json:"protocol"`
 ProtocolVersion int `json:"protocol_version"`
 Capabilities []string `json:"capabilities"`
 Nonce string `json:"nonce"`
 CheckedAt time.Time `json:"checked_at"`
 TenantID string `json:"tenant_id"`
 RequestID string `json:"request_id"`
 DeviceID string `json:"device_id"`
 HostName string `json:"host_name"`
 ClassID string `json:"class_id"`
 ClassAvailable *bool `json:"class_available"`
 AIuditorProgramState string `json:"aiuditor_program_state"`
 KeyFingerprint string `json:"key_fingerprint"`
 PathAState string `json:"path_a_state"`
 PathBState string `json:"path_b_state"`
 SellState string `json:"sell_state"`
 ScheduleHold *bool `json:"schedule_hold"`
 LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
 ActivationAllowed *bool `json:"activation_allowed"`
 SellingAllowed *bool `json:"selling_allowed"`
 TunnelVerified *bool `json:"tunnel_verified"`
}
var onboardingSecretRE = regexp.MustCompile("^hm_dev_[A-Za-z0-9_-]{32,}$")

type onboardingRequest struct {
 ProtocolVersion int `json:"protocol_version"`
 RequestID string `json:"request_id"`
 DeviceID string `json:"device_id"`
 Nonce string `json:"nonce"`
}

// CheckDevice uses the saved machine credential, never the human login token.
// The fresh challenge and every identity field must match the local journal.
func (c *Client) CheckDevice(ctx context.Context,r Request,deviceID,secret string)(OnboardingSnapshot,error) {
 var out OnboardingSnapshot
 if err:=r.Validate();err!=nil{return out,err}
 if !UUID.MatchString(deviceID)||!onboardingSecretRE.MatchString(secret) {
  return out,fmt.Errorf("saved device identity is invalid")
 }
 challenge:=make([]byte,32)
 if _,err:=rand.Read(challenge);err!=nil{return out,fmt.Errorf("cannot generate onboarding challenge")}
 body:=onboardingRequest{1,r.RequestID,deviceID,hex.EncodeToString(challenge)}
 if err:=c.request(ctx,"POST","/api/v1/hypermesh/agent/onboarding/check",secret,body,&out);err!=nil{return out,err}
 if err:=c.checkOnboarding(out,r,body);err!=nil{return OnboardingSnapshot{},err}
 return out,nil
}
func oneOf(value string,options ...string)bool {
 for _,option:=range options {if value==option{return true}}
 return false
}
func (c *Client) checkOnboarding(s OnboardingSnapshot,r Request,q onboardingRequest)error {
 if s.Protocol!="hypermesh-host-onboarding"||s.ProtocolVersion!=1 {
  return fmt.Errorf("plane does not support onboarding protocol version 1")
 }
 capabilities:=map[string]bool{}
 for _,capability:=range s.Capabilities {
  if capabilities[capability]{return fmt.Errorf("duplicate onboarding capability")}
  capabilities[capability]=true
 }
 if !capabilities["device_identity_v1"]||!capabilities["readiness_snapshot_v1"]||!capabilities["aiuditor_program_v1"] {
  return fmt.Errorf("plane lacks required onboarding capabilities")
 }
 if s.Nonce!=q.Nonce||s.TenantID!=c.tenant||s.RequestID!=r.RequestID||s.DeviceID!=q.DeviceID||
 s.HostName!=r.HostName||s.ClassID!=r.ClassID||s.KeyFingerprint!=Fingerprint(r.WGPublicKey) {
  return fmt.Errorf("onboarding response does not match this device and check")
 }
 if s.CheckedAt.IsZero()||s.ClassAvailable==nil||s.ScheduleHold==nil||
 s.ActivationAllowed==nil||s.SellingAllowed==nil||s.TunnelVerified==nil||
 *s.ActivationAllowed||*s.SellingAllowed||*s.TunnelVerified {
  return fmt.Errorf("incompatible onboarding readiness response")
 }
 if !oneOf(s.AIuditorProgramState,"ready","unconfigured","unavailable","unauthorized","writer_mismatch","missing_suite","invalid_response") {
  return fmt.Errorf("missing or unrecognized certification program state; update the plane and retry")
 }
 if !oneOf(s.PathAState,"accepted","pending","rejected","unknown")||
 !oneOf(s.PathBState,"green","never-run","suspended","unknown")||
 !oneOf(s.SellState,"paused","selling","draining","off","n/a","unknown") {
  return fmt.Errorf("unrecognized onboarding readiness state")
 }
 return nil
}
