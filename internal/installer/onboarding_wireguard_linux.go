//go:build linux

package installer

import (
 "context"
 "encoding/json"
 "fmt"
 "net"
 "os"
 "os/exec"
 "path/filepath"
 "strconv"
 "strings"
 "time"

 "github.com/FyberLabs/hypermesh-host/internal/hostregistration"
 "github.com/FyberLabs/hypermesh-host/internal/tunnel"
)

// All command output stays private; wg may return key material.
type wireguardProbe struct {
 run func(context.Context,string,...string)([]byte,error)
 start func(context.Context,string)(func(),error)
 exists func(string)bool
 wait func(context.Context,time.Duration)error
 now func()time.Time
}
func newWireguardProbe()wireguardProbe {
 return wireguardProbe{
  run:func(ctx context.Context,name string,args ...string)([]byte,error){
   ctx,cancel:=context.WithTimeout(ctx,4*time.Second);defer cancel()
   return exec.CommandContext(ctx,name,args...).Output()
  },
  start:func(ctx context.Context,iface string)(func(),error){
   // Foreground child of this bounded command, never a systemd service.
   cmd:=exec.CommandContext(ctx,"wireguard-go","-f",iface)
   if err:=cmd.Start();err!=nil{return nil,fmt.Errorf("cannot start temporary userspace WireGuard")}
   return func(){_ = cmd.Process.Kill();_ = cmd.Wait()},nil
  },
  exists:func(iface string)bool{_,err:=net.InterfaceByName(iface);return err==nil},
  wait:func(ctx context.Context,d time.Duration)error{
   timer:=time.NewTimer(d);defer timer.Stop()
   select {case <-ctx.Done():return ctx.Err();case <-timer.C:return nil}
  },
  now:time.Now,
 }
}
func ownedProbeLink(raw []byte,alias string)bool {
 var rows []struct{Alias string `json:"ifalias"`}
 return json.Unmarshal(raw,&rows)==nil&&len(rows)==1&&rows[0].Alias==alias
}
func peerHandshake(raw []byte,key string,started,now time.Time)bool {
 lines:=strings.Split(strings.TrimSpace(string(raw)),"\n")
 if len(lines)!=1{return false}
 fields:=strings.Fields(lines[0])
 if len(fields)!=2||fields[0]!=key{return false}
 epoch,err:=strconv.ParseInt(fields[1],10,64)
 return err==nil&&epoch>0&&epoch>=started.Unix()&&epoch<=now.Unix()+1
}

// A /32 route and interface-bound ICMP require traffic through the selected peer.
// Temporary devices are removed before success is returned. No persistent
// configuration, service, lease, certificate, or local selling state is changed.
func(p wireguardProbe) check(ctx context.Context,allocation hostregistration.OnboardingTunnel,private,public,mode,digest string)(observed time.Time,err error) {
 if err=allocation.Validate(public);err!=nil{return}
 actual,e:=tunnel.PublicFromPrivate(private)
 if e!=nil||actual!=public||!setupDigestRE.MatchString(digest)||(mode!="kernel"&&mode!="userspace"){return observed,fmt.Errorf("invalid local WireGuard probe identity")}
 ctx,cancel:=context.WithTimeout(ctx,90*time.Second);defer cancel()
 iface:="hm"+digest[:12]
 alias:="hypermesh-onboarding-"+digest
 if p.exists("hypermesh"){return observed,fmt.Errorf("existing hypermesh interface must be reviewed before inactive onboarding")}
 // Recovery owns an exact alias and key, never an arbitrary existing interface.
 if p.exists(iface){
  raw,e:=p.run(ctx,"ip","-j","link","show","dev",iface)
  key,ke:=p.run(ctx,"wg","show",iface,"public-key")
  if e!=nil||ke!=nil||!ownedProbeLink(raw,alias)||strings.TrimSpace(string(key))!=public {
   return observed,fmt.Errorf("temporary WireGuard interface conflict; inspect it locally before retrying")
  }
  if _,e=p.run(ctx,"ip","link","delete","dev",iface);e!=nil{return observed,fmt.Errorf("cannot recover prior WireGuard probe; preserve identity")}
 }
 routes,e:=p.run(ctx,"ip","route","show","exact",allocation.ProbeAddress+"/32")
 if e!=nil||len(strings.TrimSpace(string(routes)))!=0{return observed,fmt.Errorf("WireGuard probe route already exists or cannot be inspected")}
 directory,e:=os.MkdirTemp("","hypermesh-wg-probe-")
 if e!=nil{return observed,fmt.Errorf("cannot stage private WireGuard probe")}
 defer os.RemoveAll(directory)
 keyPath:=filepath.Join(directory,"private")
 if e=os.WriteFile(keyPath,[]byte(private+"\n"),0600);e!=nil{return observed,fmt.Errorf("cannot stage WireGuard key")}
 created:=false
 var stop func()
 defer func(){
  clean,cancel:=context.WithTimeout(context.Background(),8*time.Second);defer cancel()
  if created&&p.exists(iface){
   if _,e:=p.run(clean,"ip","link","delete","dev",iface);e!=nil{err=fmt.Errorf("temporary WireGuard cleanup failed; rerun with the same plan to recover");observed=time.Time{}}
  }
  if stop!=nil{stop()}
 }()
 if mode=="kernel" {
  if _,e=p.run(ctx,"ip","link","add","dev",iface,"type","wireguard");e!=nil{return observed,fmt.Errorf("kernel WireGuard unavailable; review approved mode and prerequisites")}
  created=true
 } else {
  stop,e=p.start(ctx,iface)
  if e!=nil{return observed,e}
  created=true
  for !p.exists(iface){if e=p.wait(ctx,100*time.Millisecond);e!=nil{return observed,fmt.Errorf("userspace WireGuard did not create its temporary interface")}}
  created=true
 }
 command:=func(name string,args ...string)error{
  _,e:=p.run(ctx,name,args...);if e!=nil{return fmt.Errorf("temporary WireGuard configuration failed; inspect prerequisites and route conflicts")}
  return nil
 }
 if err=command("ip","link","set","dev",iface,"alias",alias);err!=nil{return}
 started:=p.now()
 if err=command("wg","set",iface,"private-key",keyPath,"peer",allocation.ServerPublicKey,
  "endpoint",allocation.Endpoint,"allowed-ips",allocation.ProbeAddress+"/32","persistent-keepalive","1");err!=nil{return}
 if err=command("ip","address","add",allocation.ClientAddress,"dev",iface);err!=nil{return}
 if err=command("ip","link","set","dev",iface,"up");err!=nil{return}
 if err=command("ip","route","add",allocation.ProbeAddress+"/32","dev",iface);err!=nil{return}
 for {
  // ICMP payload/reply matching is performed by ping. No HTTPS fallback.
  _,pingErr:=p.run(ctx,"ping","-n","-I",iface,"-c","1","-W","1",allocation.ProbeAddress)
  raw,wgErr:=p.run(ctx,"wg","show",iface,"latest-handshakes")
  if pingErr==nil&&wgErr==nil&&peerHandshake(raw,allocation.ServerPublicKey,started,p.now()){
   return p.now().UTC(),nil
  }
  if p.wait(ctx,time.Second)!=nil{return observed,fmt.Errorf("WireGuard handshake/data path timed out; check gateway peer sync, UDP endpoint/firewall, and overlay ICMP reachability")}
 }
}
