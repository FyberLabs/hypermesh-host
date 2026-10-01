package hostregistration

import (
 "context"
 "crypto/rand"
 "encoding/base64"
 "encoding/hex"
 "fmt"
 "net"
 "net/netip"
 "strconv"
 "strings"
 "time"
)

type OnboardingTunnel struct {
 TunnelID string `json:"tunnel_id"`
 Status string `json:"status"`
 LeaseID *string `json:"lease_id"`
 Endpoint string `json:"endpoint"`
 ServerPublicKey string `json:"server_public_key"`
 PeerPublicKey string `json:"peer_public_key"`
 ClientAddress string `json:"client_address"`
 ProbeAddress string `json:"probe_address"`
}
type TunnelReceipt struct {
 OnboardingSnapshot
 Tunnel OnboardingTunnel `json:"tunnel"`
}
func (s OnboardingSnapshot) SupportsTunnel()bool {
 for _,c:=range s.Capabilities {if c=="onboarding_tunnel_v1"{return true}}
 return false
}
func (t OnboardingTunnel) Validate(public string)error {
 raw,err:=base64.StdEncoding.DecodeString(t.ServerPublicKey)
 ip,e:=netip.ParsePrefix(t.ClientAddress)
 host,port,ep:=net.SplitHostPort(t.Endpoint)
 n,pe:=strconv.Atoi(port)
 if !UUID.MatchString(t.TunnelID)||t.Status!="allocated"||t.LeaseID!=nil||
  t.PeerPublicKey!=public||err!=nil||len(raw)!=32||base64.StdEncoding.EncodeToString(raw)!=t.ServerPublicKey||
  t.ServerPublicKey==base64.StdEncoding.EncodeToString(make([]byte,32))||
  e!=nil||!ip.Addr().Is4()||ip.Bits()!=32||!netip.MustParsePrefix("10.77.0.0/24").Contains(ip.Addr())||
  ip.Addr()==netip.MustParseAddr("10.77.0.1")||ip.Addr()==netip.MustParseAddr("10.77.0.0")||
  ip.Addr()==netip.MustParseAddr("10.77.0.255")||t.ProbeAddress!="10.77.0.1"||
  ep!=nil||pe!=nil||n<1||n>65535||host==""||len(host)>253 {
  return fmt.Errorf("invalid onboarding WireGuard allocation")
 }
 for _,c:=range host {if !(c>='a'&&c<='z'||c>='A'&&c<='Z'||c>='0'&&c<='9'||strings.ContainsRune(".-:",c)){return fmt.Errorf("invalid WireGuard endpoint")}}
 return nil
}
func(c *Client) PrepareTunnel(ctx context.Context,r Request,deviceID,secret string)(TunnelReceipt,error) {
 var out TunnelReceipt
 if err:=r.Validate();err!=nil{return out,err}
 if !UUID.MatchString(deviceID)||!onboardingSecretRE.MatchString(secret){return out,fmt.Errorf("invalid saved device identity")}
 nonce:=make([]byte,32);if _,err:=rand.Read(nonce);err!=nil{return out,fmt.Errorf("cannot create tunnel challenge")}
 q:=onboardingRequest{1,r.RequestID,deviceID,hex.EncodeToString(nonce)}
 started:=time.Now()
 if err:=c.request(ctx,"POST","/api/v1/hypermesh/agent/onboarding/tunnel",secret,q,&out);err!=nil{return out,err}
 if err:=c.checkOnboarding(out.OnboardingSnapshot,r,q);err!=nil{return TunnelReceipt{},err}
 if !out.SupportsTunnel()||out.AIuditorProgramState!="ready"||out.SellState!="paused"||
  !oneOf(out.PathBState,"never-run","green")||*out.ScheduleHold||!(*out.ClassAvailable)||
  out.CheckedAt.Before(started.Add(-time.Minute))||out.CheckedAt.After(time.Now().Add(time.Minute)) {
  return TunnelReceipt{},fmt.Errorf("plane did not authorize an inactive WireGuard probe")
 }
 if err:=out.Tunnel.Validate(r.WGPublicKey);err!=nil{return TunnelReceipt{},err}
 return out,nil
}
