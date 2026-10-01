//go:build linux

package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"syscall"
	"time"
)

func supportFixture(t *testing.T)(string, string, string, []string) {
	t.Helper()
	dir:=t.TempDir()
	if err:=os.Chmod(dir,0o700);err!=nil{t.Fatal(err)}
	unlock,err:=lockSetupState(dir);if err!=nil{t.Fatal(err)};unlock()
	p,r:=registrationFixture(t)
	secret:="NEVER_EXPORT_raw_error_url_env_token"
	r.State="registered-inactive";r.Owner=secret;r.DeviceID=r.Request.RequestID
	r.Issuer="https://secret.invalid/"+secret;r.Dashboard=r.Issuer
	r.Request.HostName=secret;r.Request.Hardware.CPU=secret
	if err=saveRegistration(registrationPath(dir,p.SHA256),r);err!=nil{t.Fatal(err)}
	if err=writeSetupJournal(filepath.Join(dir,p.Setup.PlanSHA256+".json"),
		setupJournal{Schema:1,PlanSHA256:p.Setup.PlanSHA256,Stage:"packages",State:"failed",OriginalTarget:"graphical.target",Error:secret});err!=nil{t.Fatal(err)}
	if err=saveAgentJournal(filepath.Join(dir,"agent-"+p.SHA256+".json"),
		agentInstallJournal{Schema:1,PlanSHA256:p.SHA256,State:"installed-awaiting-enrollment",Started:true,
			Completed:[]string{"/usr/local/bin/hypermesh-agent", "/usr/local/lib/hypermesh/update-rollback",
				"/etc/hypermesh/update-pubkey.asc", "/etc/hypermesh/agent.env", "/etc/systemd/system/hypermesh-agent.service"},Error:secret});err!=nil{t.Fatal(err)}
	stamp:=time.Date(2026,10,1,10,0,0,0,time.FixedZone("local",-4*3600))
	j:=onboardingJournal{Schema:1,PlanSHA256:p.SHA256,RequestID:r.Request.RequestID,DeviceID:r.DeviceID,
		HostName:secret,State:"checked",RecordedAt:stamp,PlaneCheckedAt:&stamp,LastHeartbeatAt:&stamp,
		ProtocolVerified:true,LocalPreflightPassed:true,ReportedSellState:secret,NextAction:secret,
		Gates:[]onboardingGate{{"schedule","blocked",secret},{"activation","pending",secret},{"selling","blocked",secret}}}
	if err=saveOnboarding(onboardingPath(dir,p.SHA256),j);err!=nil{t.Fatal(err)}
	return dir,p.SHA256,p.Setup.PlanSHA256,[]string{secret,r.PrivateKey,r.DeviceSecret,r.Owner,r.Request.RequestID,
		r.Request.WGPublicKey,r.Request.DeviceSecretHash,r.Request.CredentialPrefix,r.Issuer}
}

func TestSupportBundleAllowlistAndNoMutation(t *testing.T) {
	dir,agentSHA,setupSHA,secrets:=supportFixture(t)
	entries,err:=os.ReadDir(dir);if err!=nil{t.Fatal(err)}
	before:=map[string][]byte{}
	for _,entry:=range entries {before[entry.Name()],err=os.ReadFile(filepath.Join(dir,entry.Name()));if err!=nil{t.Fatal(err)}}
	bundle,err:=collectSupport(context.Background(),dir,agentSHA,setupSHA);if err!=nil{t.Fatal(err)}
	if !bundle.CollectionComplete || !bundle.HistoricalOnly || len(bundle.Stages)!=4 {t.Fatal("incomplete summary")}
	if bundle.Stages[0].State!="failed" || !bundle.Stages[0].ErrorRecorded ||
		bundle.Stages[1].CompletedFiles!=5 || bundle.Stages[2].State!="registered-inactive" ||
		bundle.Stages[3].Gates[0].State!="blocked" {t.Fatal("lost useful diagnostics")}
	raw,err:=json.Marshal(bundle);if err!=nil{t.Fatal(err)}
	for _,secret:=range secrets {if strings.Contains(string(raw),secret){t.Fatal("private source escaped the allowlist")}}
	if strings.Contains(string(raw),"-04:00"){t.Fatal("timestamps not normalized")}
	after,err:=os.ReadDir(dir);if err!=nil{t.Fatal(err)}
	if len(after)!=len(before){t.Fatal("collector created state")}
	for _,entry:=range after {raw,err:=os.ReadFile(filepath.Join(dir,entry.Name()));if err!=nil{t.Fatal(err)}
		if !bytes.Equal(raw,before[entry.Name()]){t.Fatal("collector changed installer state")}}
}

func TestSupportBundleUnavailableStagesAreExplicit(t *testing.T) {
	for _,kind:=range []string{"missing","corrupt","duplicate","unknown-field","wrong-plan","future-schema","unsafe-mode","symlink","hardlink","fifo","oversized","unknown-state","unknown-gate","unknown-code"}{
		t.Run(kind,func(t *testing.T){
			dir,agentSHA,_,_:=supportFixture(t)
			path:=onboardingPath(dir,agentSHA)
			raw,err:=os.ReadFile(path);if err!=nil{t.Fatal(err)}
			var j onboardingJournal;if err=json.Unmarshal(raw,&j);err!=nil{t.Fatal(err)}
			want:="invalid"
			switch kind {
			case "missing": if err=os.Remove(path);err!=nil{t.Fatal(err)};want="missing"
			case "corrupt": raw=[]byte("PRIVATE_UNPARSEABLE")
			case "duplicate": raw=[]byte(`{"schema":1,"Schema":1}`)
			case "unknown-field": raw=append(raw[:len(raw)-1],[]byte(`,"new_secret":"PRIVATE_UNKNOWN"}`)...)
			case "wrong-plan":j.PlanSHA256=strings.Repeat("0",64);raw,_=json.Marshal(j)
			case "future-schema":j.Schema=99;raw,_=json.Marshal(j)
			case "unsafe-mode": if err=os.Chmod(path,0o644);err!=nil{t.Fatal(err)};want="unavailable"
			case "symlink":
				if err=os.Remove(path);err!=nil{t.Fatal(err)}
				if err=os.Symlink(registrationPath(dir,agentSHA),path);err!=nil{t.Fatal(err)};want="unavailable"
			case "hardlink": if err=os.Link(path,path+".copy");err!=nil{t.Fatal(err)};want="unavailable"
			case "fifo":
				if err=os.Remove(path);err!=nil{t.Fatal(err)}
				if err=syscall.Mkfifo(path,0o600);err!=nil{t.Fatal(err)};want="unavailable"
			case "oversized":raw=bytes.Repeat([]byte("x"),(2<<20)+1);want="unavailable"
			case "unknown-state":j.State="PRIVATE_STATE";raw,_=json.Marshal(j)
			case "unknown-gate":j.Gates[0].ID="PRIVATE_GATE";raw,_=json.Marshal(j)
			case "unknown-code":j.FailureCode="PRIVATE_FAILURE";raw,_=json.Marshal(j);want="collected"
			}
			if supportOneOf(kind,"corrupt","duplicate","unknown-field","wrong-plan","future-schema","oversized","unknown-state","unknown-gate","unknown-code") {
				if err=os.WriteFile(path,raw,0o600);err!=nil{t.Fatal(err)}
			}
			bundle,err:=collectSupport(context.Background(),dir,agentSHA,"");if err!=nil{t.Fatal(err)}
			if bundle.Stages[3].Collection!=want{t.Fatalf("got %s want %s",bundle.Stages[3].Collection,want)}
			if bundle.CollectionComplete!=(want=="collected"){t.Fatal("incorrect collection result")}
			export,_:=json.Marshal(bundle)
			if strings.Contains(string(export),"PRIVATE_"){t.Fatal("invalid journal text leaked")}
			if kind=="unknown-code"&&bundle.Stages[3].FailureCode!="unknown"{t.Fatal("unknown failure was hidden")}
		})
	}
}
func TestSupportBundleLockAndCancellation(t *testing.T) {
	dir,sha,_,_:=supportFixture(t)
	unlock,err:=lockSetupState(dir);if err!=nil{t.Fatal(err)}
	if _,err=collectSupport(context.Background(),dir,sha,"");err==nil{t.Fatal("read while installer writer was active")}
	unlock()
	ctx,cancel:=context.WithCancel(context.Background());cancel()
	if _,err=collectSupport(ctx,dir,sha,"");err==nil{t.Fatal("ignored cancellation")}
	if err=os.Remove(filepath.Join(dir,"apply.lock"));err!=nil{t.Fatal(err)}
	if _,err=collectSupport(context.Background(),dir,sha,"");err==nil{t.Fatal("created missing lock")}
	if _,err=os.Lstat(filepath.Join(dir,"apply.lock"));!os.IsNotExist(err){t.Fatal("lock recreated")}
	if _,err=collectSupport(context.Background(),dir,"../private","");err==nil{t.Fatal("accepted arbitrary input path")}
}
func TestSupportPublicationAndCLI(t *testing.T) {
	dir,agentSHA,setupSHA,secrets:=supportFixture(t)
	var out,diagnostic bytes.Buffer
	output:=filepath.Join(dir,"support.json")
	args:=[]string{"--plan-sha",agentSHA,"--setup-plan-sha",setupSHA,"--output",output}
	if code:=runSupportBundle(context.Background(),args,&out,&diagnostic,dir,true);code!=0{t.Fatalf("exit %d: %s",code,diagnostic.String())}
	info,err:=os.Stat(output);if err!=nil{t.Fatal(err)}
	if !privateSetupFile(info){t.Fatal("export must be 0600 with one link")}
	raw,err:=os.ReadFile(output);if err!=nil{t.Fatal(err)}
	for _,secret:=range secrets {if strings.Contains(string(raw)+out.String()+diagnostic.String(),secret){t.Fatal("secret in artifact or console")}}
	if code:=runSupportBundle(context.Background(),args,&out,&diagnostic,dir,true);code!=1{t.Fatal("overwrote existing bundle")}
	after,_:=os.ReadFile(output);if !bytes.Equal(raw,after){t.Fatal("changed existing bundle")}
	for _,kind:=range []string{"symlink","directory","cancelled","unsafe-parent"} {
		path:=filepath.Join(dir,kind)
		ctx:=context.Background()
		switch kind{
		case "symlink":if err=os.Symlink(output,path);err!=nil{t.Fatal(err)}
		case "directory":if err=os.Mkdir(path,0o700);err!=nil{t.Fatal(err)}
		case "cancelled":var cancel context.CancelFunc;ctx,cancel=context.WithCancel(ctx);cancel()
		case "unsafe-parent":if err=os.Mkdir(path,0o777);err!=nil{t.Fatal(err)};if err=os.Chmod(path,0o777);err!=nil{t.Fatal(err)};path=filepath.Join(path,"bundle.json")
		}
		if writeSupport(ctx,path,supportBundle{})==nil{t.Fatal("accepted unsafe publication",kind)}
	}
	if code:=runSupportBundle(context.Background(),nil,&out,&diagnostic,dir,true);code!=2{t.Fatal("missing args")}
	if code:=runSupportBundle(context.Background(),[]string{"--help"},&out,&diagnostic,dir,true);code!=0{t.Fatal("help")}
	if code:=runSupportBundle(context.Background(),args,&out,&diagnostic,dir,false);code!=1{t.Fatal("non-root accepted")}
	if err=os.Remove(onboardingPath(dir,agentSHA));err!=nil{t.Fatal(err)}
	args[len(args)-1]=filepath.Join(dir,"partial.json")
	if code:=runSupportBundle(context.Background(),args,&out,&diagnostic,dir,true);code!=1{t.Fatal("partial collection reported complete")}
	raw,err=os.ReadFile(args[len(args)-1]);if err!=nil{t.Fatal("partial bundle not saved")}
	if !bytes.Contains(raw,[]byte(`"collection_complete": false`)){t.Fatal("missing partial marker")}
}

func TestSupportRejectsInconsistentInstallCompletion(t *testing.T) {
	for _,kind:=range []string{"not-started","missing-agent","missing-rollback","missing-trust","missing-env","missing-unit","duplicate","pending-started","installing-not-started"} {
		t.Run(kind,func(t *testing.T){
			dir,sha,_,_:=supportFixture(t)
			path:=filepath.Join(dir,"agent-"+sha+".json")
			raw,err:=os.ReadFile(path);if err!=nil{t.Fatal(err)}
			var j agentInstallJournal;if err=json.Unmarshal(raw,&j);err!=nil{t.Fatal(err)}
			switch kind {
			case "not-started":j.Started=false
			case "missing-agent":j.Completed=append(j.Completed[:0],j.Completed[1:]...)
			case "missing-rollback":j.Completed=append(j.Completed[:1],j.Completed[2:]...)
			case "missing-trust":j.Completed=append(j.Completed[:2],j.Completed[3:]...)
			case "missing-env":j.Completed=append(j.Completed[:3],j.Completed[4:]...)
			case "missing-unit":j.Completed=j.Completed[:4]
			case "duplicate":j.Completed=append(j.Completed,j.Completed[0])
			case "pending-started":j.State="pending"
			case "installing-not-started":j.State="installing";j.Started=false;j.Completed=nil
			}
			if err=saveAgentJournal(path,j);err!=nil{t.Fatal(err)}
			bundle,err:=collectSupport(context.Background(),dir,sha,"");if err!=nil{t.Fatal(err)}
			if bundle.CollectionComplete || bundle.Stages[1].Collection!="invalid" ||
				strings.Contains(bundle.Stages[1].NextAction,"use register-agent"){t.Fatal("inconsistent journal recommended enrollment")}
		})
	}
}
func TestSupportSetupOnlyDoesNotCollectPrivateAgentState(t *testing.T) {
	dir,sha,setupSHA,_:=supportFixture(t)
	// Unsafe agent state is irrelevant to a setup-only request.
	if err:=os.Chmod(registrationPath(dir,sha),0o644);err!=nil{t.Fatal(err)}
	bundle,err:=collectSupport(context.Background(),dir,"",setupSHA);if err!=nil{t.Fatal(err)}
	if !bundle.CollectionComplete || bundle.Stages[0].Collection!="collected" {t.Fatal("setup-only collection failed")}
	for _,stage:=range bundle.Stages[1:] {if stage.Collection!="not-requested"{t.Fatal("collected an unrequested stage")}}
}
