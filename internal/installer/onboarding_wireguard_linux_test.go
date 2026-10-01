//go:build linux

package installer

import (
 "context"
 "encoding/base64"
 "fmt"
 "os"
 "strings"
 "testing"
 "time"

 "github.com/FyberLabs/hypermesh-host/internal/hostregistration"
 "github.com/FyberLabs/hypermesh-host/internal/tunnel"
)

func TestWireGuardProbeRequiresHandshakeAndDataAndCleansUp(t *testing.T){
 for _,mode:=range []string{"kernel","userspace"}{
 for _,failure:=range []string{"","ping","handshake","peer","configure","cleanup","route","foreign"}{
 t.Run(mode+"/"+failure,func(t *testing.T){
  private:=base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a",32)))
  public,err:=tunnel.PublicFromPrivate(private);if err!=nil{t.Fatal(err)}
  peer:=hostregistration.OnboardingTunnel{TunnelID:"12345678-1234-1234-1234-123456789abc",
   Status:"allocated",Endpoint:"wg.example:51820",ServerPublicKey:public,PeerPublicKey:public,
   ClientAddress:"10.77.0.2/32",ProbeAddress:"10.77.0.1"}
  live:=failure=="foreign";stopped:=false;deletes:=0
  clock:=time.Unix(1700000000,0)
  keyfile:=""
  p:=wireguardProbe{
   exists:func(name string)bool{return name!="hypermesh"&&live},
   now:func()time.Time{return clock},
   wait:func(context.Context,time.Duration)error{return fmt.Errorf("deadline")},
   start:func(context.Context,string)(func(),error){live=true;return func(){stopped=true},nil},
   run:func(_ context.Context,name string,args ...string)([]byte,error){
    joined:=strings.Join(args," ")
    if strings.Contains(joined,private){t.Fatal("private key in command arguments")}
    if name=="ip"&&strings.Contains(joined,"route show"){if failure=="route"{return []byte("existing"),nil};return nil,nil}
    if name=="ip"&&strings.Contains(joined,"link add"){live=true}
    if name=="ip"&&strings.Contains(joined,"link delete"){deletes++;if failure=="cleanup"{return nil,fmt.Errorf("raw-private-error")};live=false}
    if name=="wg"&&len(args)>0&&args[0]=="set" {
     for i,arg:=range args{if arg=="private-key"{keyfile=args[i+1];info,e:=os.Stat(keyfile);if e!=nil||info.Mode().Perm()!=0600{t.Fatal("unsafe key staging")}}}
     if failure=="configure"{return nil,fmt.Errorf("raw-private-error")}
    }
    if name=="wg"&&strings.Contains(joined,"latest-handshakes"){
     key:=public;epoch:=clock.Unix()
     if failure=="handshake"{epoch--}
     if failure=="peer"{key="other"}
     return []byte(fmt.Sprintf("%s\t%d\n",key,epoch)),nil
    }
    if name=="ping" {
     if len(args)<3||args[1]!="-I"||!strings.HasPrefix(args[2],"hm"){t.Fatal("ping not bound to probe interface")}
     if failure=="ping"{return nil,fmt.Errorf("raw-private-error")}
    }
    return nil,nil
   },
  }
  at,err:=p.check(context.Background(),peer,private,public,mode,strings.Repeat("a",64))
  if (err==nil)!=(failure==""){t.Fatalf("failure %q got %v",failure,err)}
  if err!=nil&&strings.Contains(err.Error(),"raw-private"){t.Fatal("leaked command output")}
  if failure==""&&at.IsZero(){t.Fatal("missing evidence")}
  if failure!="foreign"&&failure!="route"&&failure!="cleanup"&&(live||deletes!=1){t.Fatal("temporary interface leaked")}
  if (failure=="foreign"||failure=="route")&&deletes!=0{t.Fatal("removed pre-existing state")}
  if mode=="userspace"&&failure!="foreign"&&failure!="route"&&!stopped{t.Fatal("userspace child not stopped")}
  if keyfile!=""{if _,e:=os.Stat(keyfile);!os.IsNotExist(e){t.Fatal("private temporary file retained")}}
 })}
 }
}
func TestWireGuardIntentAndJournal(t *testing.T){
 for _,failure:=range []string{"","old-plane","probe","save"}{t.Run(failure,func(t *testing.T){
  var j,durable onboardingJournal
  invoked:=false
  deps:=onboardingDeps{
   Verify:func(context.Context)error{return nil},
   Preflight:func(context.Context)error{return nil},
   Remote:func(context.Context)(hostregistration.OnboardingSnapshot,error){
    s:=onboardingFixture();if failure!="old-plane"{s.Capabilities=[]string{"onboarding_tunnel_v1"}}
    return s,nil
   },
   WireGuard:func(context.Context)(time.Time,error){
    invoked=true
    if !durable.WireGuardRequested||durable.State!="checking"||durable.WireGuardVerifiedAt!=nil{t.Fatal("missing durable intent")}
    if failure=="probe"{return time.Time{},fmt.Errorf("probe failed")}
    return time.Now().UTC(),nil
   },
   Save:func(value onboardingJournal)error{
    if failure=="save"&&value.State=="checked"{return fmt.Errorf("disk full")}
    durable=value;return nil
   },
  }
  err:=performOnboardingCheck(context.Background(),&j,deps)
  if (err==nil)!=(failure==""){t.Fatalf("%s %v",failure,err)}
  if failure=="old-plane"&&invoked{t.Fatal("invoked incompatible plane")}
  if failure=="" {
   if !validWireGuardJournal(j)||j.WireGuardVerifiedAt==nil||j.ActivationAllowed||j.SellingAllowed{t.Fatal("invalid evidence or authority")}
   deps.WireGuard=nil
   if err=performOnboardingCheck(context.Background(),&j,deps);err!=nil{t.Fatal(err)}
   if j.WireGuardRequested||j.WireGuardVerifiedAt!=nil{t.Fatal("historical check reused")}
  }else if j.WireGuardVerifiedAt!=nil{t.Fatal("failure retained verification")}
 })}
}
