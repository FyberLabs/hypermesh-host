//go:build linux

package installer

import (
 "context"
 "fmt"
 "io"
 "reflect"
 "strings"
 "testing"
 "time"
 "github.com/FyberLabs/hypermesh-host/internal/hostregistration"
)

func TestPlatformUpdateReviewAcceptDeclineAndRace(t *testing.T){
 for _,mode:=range []string{"accept","decline","changed-again","no-prompt"}{t.Run(mode,func(t *testing.T){
  p:=SetupPlan{Platform:SetupPlatform{ProtectedPackages:[]PackageChange{{"linux-libc-dev:arm64","1","1"},{"libnvidia-container1:arm64","1","1"}}}}
  original:=append([]PackageChange(nil),p.Platform.ProtectedPackages...)
  installed:=map[string]string{"linux-libc-dev:arm64":"2","libnvidia-container1:arm64":"2"}
  messages:=[]string{};checks,saves,prompts:=0,0,0
  confirm:=func(string)error{prompts++;if len(messages)!=3{t.Fatal("did not show every change before asking")};if mode=="decline"{return fmt.Errorf("declined")};return nil}
  if mode=="no-prompt"{confirm=nil}
  err:=reviewInstalledVersions(p,installed,func(candidate SetupPlan)error{
   checks++;if mode=="changed-again"&&checks==2{return fmt.Errorf("changed during review")}
   for _,pkg:=range candidate.Platform.ProtectedPackages{if pkg.To!="2"{t.Fatal("candidate lacks new versions")}}
   return nil
  },confirm,func(message string){messages=append(messages,message)},func(packages []PackageChange)error{saves++;return nil})
  if mode=="accept"{if err!=nil||saves!=1||checks!=2||prompts!=1{t.Fatal(err,saves,checks,prompts)}}else if err==nil||saves!=0{t.Fatal("saved without completed review")}
  if !reflect.DeepEqual(p.Platform.ProtectedPackages,original){t.Fatal("mutated original plan")}
  if !strings.Contains(messages[1],"libnvidia"){t.Fatal("nondeterministic review order")}
 })}
}
func TestLocalExpiryPreservesAmbiguousCompletion(t *testing.T){
 now:=time.Now()
 for _,state:=range []string{"prepared","pending","expired","approved","completed","registered-inactive","denied","retired"}{
  j:=registrationJournal{State:state,ExpiresAt:now.Add(-time.Minute)}
  if savedRegistrationExpired(j,now)!=(state=="expired"){t.Fatal(state)}
 }
 if savedRegistrationExpired(registrationJournal{State:"pending"},now){t.Fatal("invented legacy expiry")}
 if savedRegistrationExpired(registrationJournal{State:"pending",ExpiresAt:now.Add(time.Minute)},now){t.Fatal("unexpired request replaced")}
}
func TestSavedServerExpiryCheckedBeforeCreate(t *testing.T){
 _,j:=registrationFixture(t);j.State="pending"
 remote:=&fakeRegistrationRemote{states:[]string{"expired"}}
 renewals:=0
 err:=prepareSavedRegistration(context.Background(),j,remote,func(status hostregistration.Status)error{renewals++;return nil})
 if err!=nil||renewals!=1||remote.gets!=1||remote.creates!=0{t.Fatal(err,renewals,remote)}
}

func TestPendingApprovalReconcilesWithoutReplacement(t *testing.T){
 _,j:=registrationFixture(t);j.State="pending";j.ExpiresAt=time.Now().Add(-time.Hour)
 remote:=&fakeRegistrationRemote{states:[]string{"approved"}}
 err:=prepareSavedRegistration(context.Background(),j,remote,func(hostregistration.Status)error{t.Fatal("replaced approved request");return nil})
 if err!=nil||remote.gets!=1||remote.creates!=0{t.Fatal(err,remote)}
 if savedRegistrationExpired(j,time.Now()){t.Fatal("replaced from stale pending journal")}
}
func TestGuidedLaterStagesCarryPlatformReview(t *testing.T){
 g,_,_:=fixtureGuided(t,"y\ny\ny\ny\n")
 original:=g.run
 seen:=map[string]bool{}
 g.run=func(name string)guidedRunner{return func(ctx context.Context,args []string,out,diagnostic io.Writer)int{
  if name=="check-agent"||name=="plan-certification"||name=="certify-agent"||name=="activate-agent"{
   if _,ok:=ctx.Value(platformReviewPrompt{}).(func(string)error);!ok{t.Fatal("review unavailable",name)}
   if ctx.Value(platformReviewOutput{})==nil{t.Fatal("review changes cannot be displayed",name)}
   seen[name]=true
  }
  return original(name)(ctx,args,out,diagnostic)
 }}
 if err:=g.finish(context.Background());err!=nil{t.Fatal(err)}
 if len(seen)!=4{t.Fatal(seen)}
}
