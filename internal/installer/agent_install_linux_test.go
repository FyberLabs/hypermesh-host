//go:build linux

package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FyberLabs/hypermesh-host/internal/buildinfo"
)

func agentInstallFixture(t *testing.T)agentInstallPlan {
	t.Helper()
	oldC,oldV,oldB:=buildinfo.Commit,buildinfo.Version,buildinfo.Build
	buildinfo.Commit,buildinfo.Version,buildinfo.Build=strings.Repeat("a",40),"0.0.0","21"
	t.Cleanup(func(){buildinfo.Commit,buildinfo.Version,buildinfo.Build=oldC,oldV,oldB})
	f:=newApplyFixture(t,false,false)
	p:=agentInstallPlan{Schema:1,Setup:f.plan,ReleaseURL:"https://example.invalid/test/release",Channel:"test",
		PublicKey:"-----BEGIN PGP PUBLIC KEY BLOCK-----\nfixture\n-----END PGP PUBLIC KEY BLOCK-----",
		Fingerprints:[]string{strings.Repeat("A",40)},ControlPlane:"https://example.invalid",TenantID:strings.Repeat("a",8)+"-"+strings.Repeat("b",4)+"-"+strings.Repeat("c",4)+"-"+strings.Repeat("d",4)+"-"+strings.Repeat("e",12),HostName:"test-host"}
	p.Files=agentFileManifest(agentPayloads(p,[]byte("verified agent")))
	p.SHA256=agentPlanDigest(p)
	return p
}
func TestAgentInstallModeAndTrustPayloads(t *testing.T) {
	p:=agentInstallFixture(t)
	for _,mode:=range []string{"kernel","userspace"} {
		p.Setup.WireGuardMode=mode
		files:=agentPayloads(p,[]byte("agent"))
		foundWG:=false
		for _,f:=range files {
			if digestSetupBytes(f.Data)!=f.SHA256 {t.Fatal("payload hash mismatch")}
			switch f.Path {
			case "/etc/systemd/system/hypermesh-wg.service":foundWG=true
			case "/etc/systemd/system/hypermesh-agent.service":
				unit:=string(f.Data)
				if strings.Contains(unit,"hypermesh-wg.service")!=(mode=="userspace"){t.Fatal("wrong tunnel dependency")}
				if !strings.Contains(unit,"ConditionPathExists=/var/lib/hypermesh/activation-approved") ||
					!strings.Contains(unit,"ExecStartPre=/usr/local/lib/hypermesh/update-rollback") {t.Fatal("activation or rollback gate missing")}
			case "/etc/hypermesh/agent.env":
				env:=string(f.Data)
				want:="0";if mode=="userspace"{want="1"}
				if !strings.Contains(env,"HYPERMESH_WG_USERSPACE=\""+want+"\"") {t.Fatal("wrong WireGuard mode")}
				if !strings.Contains(env,"HYPERMESH_UPDATE_EVERY=\"1h\"") || !strings.Contains(env,"HYPERMESH_UPDATE_FINGERPRINTS=") {t.Fatal("update trust absent")}
				if strings.Contains(env,"ENROLL_TOKEN=")||strings.Contains(env,"DEVICE_SECRET="){t.Fatal("credential in plan")}
			}
		}
		if foundWG!=(mode=="userspace"){t.Fatal("kernel install includes userspace unit")}
	}
}
func TestAgentPlanValidationAndBinding(t *testing.T) {
	p:=agentInstallFixture(t)
	if err:=validateAgentInputs(p);err!=nil{t.Fatal(err)}
	for _,kind:=range []string{"control-plane","host-name","key","fingerprint","relative-model","newline","schema","setup","channel"} {
		t.Run(kind,func(t *testing.T){
			raw,_:=json.Marshal(p);var changed agentInstallPlan;_ = json.Unmarshal(raw,&changed)
			switch kind {
			case "control-plane":changed.ControlPlane="http://insecure.invalid"
			case "host-name":changed.HostName="x\nEVIL=1"
			case "key":changed.PublicKey="-----BEGIN PGP PRIVATE KEY BLOCK-----"
			case "fingerprint":changed.Fingerprints=[]string{"invalid"}
			case "relative-model":changed.Setup.Preflight.Resources.ModelDirectory="relative";changed.Setup.PlanSHA256=setupPlanDigest(changed.Setup)
			case "newline":changed.Setup.Preflight.Resources.ModelDirectory="/cache\nEVIL=1";changed.Setup.PlanSHA256=setupPlanDigest(changed.Setup)
			case "schema":changed.Schema=2
			case "setup":changed.Setup.WireGuardMode="auto"
			case "channel":changed.Channel="unknown"
			}
			if err:=validateAgentInputs(changed);err==nil{t.Fatal("invalid input accepted")}
			if agentPlanDigest(changed)==p.SHA256{t.Fatal("changed plan retains approval")}
		})
	}
	if got:=agentEnvValue("a\"b\\c $HOME");got!="\"a\\\"b\\\\c $HOME\"" {t.Fatal(got)}
}

func TestInstallAgentFilesResumeAndNoOverwrite(t *testing.T) {
	p:=agentInstallFixture(t)
	files:=agentPayloads(p,[]byte("verified binary"))
	root:=t.TempDir()
	j:=agentInstallJournal{Schema:1,PlanSHA256:p.SHA256}
	calls:=0
	save:=func(agentInstallJournal)error{calls++;if calls==3{return fmt.Errorf("disk full")};return nil}
	if err:=installAgentFiles(context.Background(),root,files,false,&j,save);err==nil{t.Fatal("ignored journal failure")}
	// Two files were installed before their completion could be persisted.
	if _,err:=os.Stat(agentDest(root,files[1].Path));err!=nil{t.Fatal(err)}
	save=func(agentInstallJournal)error{return nil}
	if err:=installAgentFiles(context.Background(),root,files,true,&j,save);err!=nil{t.Fatal(err)}
	if j.State!="installed-awaiting-enrollment"||len(j.Completed)!=len(files){t.Fatalf("%+v",j)}
	if err:=installAgentFiles(context.Background(),root,files,true,&j,save);err!=nil{t.Fatal(err)}
	for _,f:=range files {if ok,err:=agentFileMatches(agentDest(root,f.Path),f);err!=nil||!ok{t.Fatal(f.Path,err)}}
	// First-install cannot silently adopt an existing installation.
	if err:=installAgentFiles(context.Background(),root,files,false,&j,save);err==nil{t.Fatal("adopted existing files")}
	path:=agentDest(root,files[0].Path)
	if err:=os.WriteFile(path,[]byte("operator version"),0o755);err!=nil{t.Fatal(err)}
	if err:=installAgentFiles(context.Background(),root,files,true,&j,save);err==nil{t.Fatal("overwrote changed agent")}
	raw,_:=os.ReadFile(path);if string(raw)!="operator version"{t.Fatal("changed existing agent")}
}

func TestAgentInstallPrechecksBeforeWrites(t *testing.T) {
	p:=agentInstallFixture(t)
	files:=agentPayloads(p,[]byte("agent"))
	for _,kind:=range []string{"unit-conflict","enrolled","active","drop-in","symlink-parent","cancel","journal"} {
		t.Run(kind,func(t *testing.T){
			root:=t.TempDir()
			path:=""
			switch kind {
			case "unit-conflict":path="/usr/lib/systemd/system/hypermesh-agent.service"
			case "enrolled":path="/var/lib/hypermesh/device.json"
			case "active":path="/var/lib/hypermesh/activation-approved"
			case "drop-in":path="/etc/systemd/system/hypermesh-agent.service.d"
			}
			if path!="" {
				dest:=agentDest(root,path);if err:=os.MkdirAll(filepath.Dir(dest),0o755);err!=nil{t.Fatal(err)}
				if err:=os.WriteFile(dest,[]byte("existing"),0o600);err!=nil{t.Fatal(err)}
			}
			if kind=="symlink-parent" {if err:=os.Symlink(t.TempDir(),filepath.Join(root,"usr"));err!=nil{t.Fatal(err)}}
			ctx,cancel:=context.WithCancel(context.Background());defer cancel()
			if kind=="cancel"{cancel()}
			j:=agentInstallJournal{Schema:1,PlanSHA256:p.SHA256}
			save:=func(agentInstallJournal)error{if kind=="journal"{return fmt.Errorf("failed")};return nil}
			if err:=installAgentFiles(ctx,root,files,false,&j,save);err==nil{t.Fatal("unsafe installation accepted")}
			if _,err:=os.Stat(agentDest(root,files[0].Path));!os.IsNotExist(err){t.Fatal("wrote agent before validation")}
		})
	}
}
func TestAgentJournalPrivateAtomicPersistence(t *testing.T) {
	dir:=t.TempDir();path:=filepath.Join(dir,"agent.json")
	j:=agentInstallJournal{Schema:1,PlanSHA256:strings.Repeat("a",64),State:"installing"}
	if err:=saveAgentJournal(path,j);err!=nil{t.Fatal(err)}
	j.State="installed-awaiting-enrollment"
	if err:=saveAgentJournal(path,j);err!=nil{t.Fatal(err)}
	raw,err:=readSetupRegular(path,true);if err!=nil{t.Fatal(err)}
	var got agentInstallJournal
	if err=decodeSetupJSON(raw,&got);err!=nil||got.State!=j.State{t.Fatal(got,err)}
	files,_:=filepath.Glob(filepath.Join(dir,".agent-journal-*"));if len(files)!=0{t.Fatal("journal temp leaked")}
}

func TestRejectedPrecheckDoesNotAuthorizeAdoptionOnRetry(t *testing.T) {
	p:=agentInstallFixture(t);files:=agentPayloads(p,[]byte("agent"));root:=t.TempDir()
	if err:=writeAgentFile(agentDest(root,files[0].Path),files[0]);err!=nil{t.Fatal(err)}
	j:=agentInstallJournal{Schema:1,PlanSHA256:p.SHA256}
	save:=func(agentInstallJournal)error{return nil}
	for attempt:=0;attempt<2;attempt++ {
		if err:=installAgentFiles(context.Background(),root,files,j.Started,&j,save);err==nil{t.Fatal("adopted preexisting file")}
		j.State="failed" // The CLI persists the failure, without granting resume.
		if j.Started{t.Fatal("precheck failure authorized resume")}
	}
}
func TestAgentUnitSearchPathsAndInheritedOverrides(t *testing.T) {
	p:=agentInstallFixture(t);files:=agentPayloads(p,[]byte("agent"))
	for _,dir:=range []string{"/usr/local/lib/systemd/system","/usr/lib/systemd/system","/run/systemd/generator.early","/custom/manager-units"} {
		for _,name:=range []string{"hypermesh-agent.service","hypermesh-wg.service",
			"hypermesh-agent.service.d","hypermesh-wg.service.d","hypermesh-.service.d","service.d"} {
			root:=t.TempDir();path:=agentDest(root,filepath.Join(dir,name))
			if err:=os.MkdirAll(filepath.Dir(path),0o755);err!=nil{t.Fatal(err)}
			if err:=os.WriteFile(path,[]byte("override"),0o644);err!=nil{t.Fatal(err)}
			if err:=checkAgentUnitPathList(root,files,"/etc/systemd/system "+dir);err==nil{t.Fatal("ignored",path)}
		}
	}
	for _,bad:=range []string{"","relative","/path/../other","/path\\x20space"} {
		if err:=checkAgentUnitPathList(t.TempDir(),files,bad);err==nil{t.Fatal("accepted path list",bad)}
	}
	root:=t.TempDir()
	source:=setupSource{Output:func(_ context.Context,name string,args ...string)([]byte,error){
		if name=="systemd-analyze"{return []byte("/etc/systemd/system\n/usr/local/lib/systemd/system"),nil}
		return []byte("/custom/manager-units"),nil
	}}
	if err:=checkAgentUnitPaths(context.Background(),source,root,files);err!=nil{t.Fatal(err)}
	path:=agentDest(root,"/custom/manager-units/service.d")
	if err:=os.MkdirAll(path,0o755);err!=nil{t.Fatal(err)}
	if err:=checkAgentUnitPaths(context.Background(),source,root,files);err==nil{t.Fatal("ignored running manager path")}
}

func TestInstalledContinuationKeepsMutationBuildBinding(t *testing.T){
 p:=agentInstallFixture(t)
 buildinfo.Build="22";buildinfo.Commit=strings.Repeat("b",40)
 if err:=validateAgentInputs(p);err==nil{t.Fatal("new installer accepted old installation mutation approval")}
 if err:=validateInstalledAgentInputs(p);err!=nil{t.Fatal(err)}
 if err:=validateSetupApproval(p.Setup,p.Setup.PlanSHA256);err==nil{t.Fatal("old prerequisite mutation accepted")}
 changed:=p;changed.Setup.Profile=&SetupProfile{}
 if err:=validateInstalledAgentInputs(changed);err==nil{t.Fatal("changed policy accepted")}
 buildinfo.Build="20"
 if err:=validateInstalledAgentInputs(p);err==nil{t.Fatal("downgrade accepted")}
}
