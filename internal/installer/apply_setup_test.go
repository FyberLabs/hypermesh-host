package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type applyFixture struct {
	plan SetupPlan
	installed map[string]string
	target string
	calls []string
	saves []setupJournal
	deps setupApplyDeps
	journal setupJournal
}

func newApplyFixture(t *testing.T, packages, headless bool)*applyFixture {
	t.Helper()
	pre,source:=setupFixture()
	pre.Resources.SchemaVersion=1
	pre.Resources.MemoryMode="uma"
	pre.Resources.ModelDirectory="/cache"
	f:=&applyFixture{installed:map[string]string{
		"ca-certificates":"1.0","gnupg":"2.0","python3":"3.12",
		"wireguard-tools":"1.0","docker.io":"26.0","nvidia-l4t-core":"36.4.0"},
		target:"graphical.target"}
	if !packages {f.installed["wireguard-go"]="1.0"}
	simulation:=func()string {
		if _,ok:=f.installed["wireguard-go"];ok{return ""}
		return "Inst wireguard-go (1.0 Ubuntu [arm64])\nConf wireguard-go (1.0 Ubuntu [arm64])\n"
	}
	source.Output=func(ctx context.Context,name string,args ...string)([]byte,error) {
		switch name {
		case "systemctl":return []byte(f.target),nil
		case "dpkg-query":
			var lines []string
			for name,version:=range f.installed {lines=append(lines,name+"\t"+version+"\tii \n")}
			return []byte(strings.Join(lines,"")),nil
		case "apt-cache":return []byte("Candidate: 1.0"),nil
		case "apt-get":return []byte(simulation()),nil
		}
		return nil,fmt.Errorf("unexpected probe")
	}
	f.plan=buildSetupPlan(context.Background(),pre,"auto","userspace",headless,source)
	f.plan.ReadinessURL="https://example.invalid/ready"
	f.plan.PlanSHA256=setupPlanDigest(f.plan)
	if !f.plan.Reviewable {t.Fatalf("fixture blocked: %+v",f.plan)}
	f.journal=setupJournal{Schema:1,PlanSHA256:f.plan.PlanSHA256,Stage:"pending",State:"pending",OriginalTarget:f.target}
	f.deps=setupApplyDeps{Source:source,
		Preflight:func(context.Context,SetupPlan)(Plan,error){return pre,nil},
		Run:func(ctx context.Context,args []string)error{
			f.calls=append(f.calls,strings.Join(args," "))
			switch args[0] {
			case "apt-get":f.installed["wireguard-go"]="1.0"
			case "systemctl":f.target="multi-user.target"
			default:t.Fatal("unexpected mutation")
			}
			return nil
		},
		Save:func(j setupJournal)error{f.saves=append(f.saves,j);return nil}}
	return f
}

func (f *applyFixture) apply()error {
	return applySetup(context.Background(),f.plan,f.plan.PlanSHA256,&f.journal,f.deps)
}

func TestApplySetupAndNoopResume(t *testing.T) {
	f:=newApplyFixture(t,true,true)
	if err:=f.apply();err!=nil {t.Fatal(err)}
	if len(f.calls)!=2 || f.target!="multi-user.target" || f.journal.State!="complete" || f.journal.Stage!="prerequisites" {
		t.Fatalf("%+v calls=%v",f.journal,f.calls)
	}
	if !strings.Contains(f.calls[0],"wireguard-go=1.0") || !strings.Contains(f.calls[0],"--no-upgrade") {t.Fatal("package constraints lost")}
	if err:=f.apply();err!=nil {t.Fatal(err)}
	if len(f.calls)!=2 {t.Fatal("completed work ran twice")}
}

func TestApplySetupResumesSuccessfulInstallAfterCommandError(t *testing.T) {
	f:=newApplyFixture(t,true,true)
	run:=f.deps.Run
	first:=true
	f.deps.Run=func(ctx context.Context,args []string)error {
		err:=run(ctx,args)
		if first {first=false;return fmt.Errorf("interrupted after installation")}
		return err
	}
	if err:=f.apply();err==nil || f.journal.State!="failed" || len(f.calls)!=1 {t.Fatal("failure not recorded")}
	if err:=f.apply();err!=nil {t.Fatal(err)}
	if len(f.calls)!=2 || !strings.HasPrefix(f.calls[1],"systemctl ") {t.Fatal("installed package was repeated",f.calls)}
}

func TestApplySetupPartialApprovedPackages(t *testing.T) {
	f:=newApplyFixture(t,true,false)
	f.plan.Packages=append(f.plan.Packages,PackageChange{Name:"z-helper",To:"2.0"})
	f.plan.Changes[0].Argv=setupInstallArgv(f.plan.Packages)
	f.plan.PlanSHA256=setupPlanDigest(f.plan)
	f.installed["z-helper"]="2.0"
	remaining,err:=remainingSetupPackages(f.plan,f.installed)
	if err!=nil || len(remaining)!=1 || remaining[0].Name!="wireguard-go" {t.Fatal(remaining,err)}
}

func TestApplySetupRejectsApprovalAndCommandTampering(t *testing.T) {
	for _,kind:=range []string{"wrong-approval","modified-hash","command","profile","old-schema","build","blocked","foreign-arch"} {
		t.Run(kind,func(t *testing.T){
			f:=newApplyFixture(t,true,true)
			approved:=f.plan.PlanSHA256
			switch kind {
			case "wrong-approval":approved=strings.Repeat("0",64)
			case "modified-hash":f.plan.HeadlessRequested=false
			case "command":f.plan.Changes[0].Argv=[]string{"sh","-c","bad"}
			case "profile":f.plan.Profile.Revision++
			case "old-schema":f.plan.SchemaVersion=1
			case "build":f.plan.InstallerBuild++
			case "blocked":f.plan.Reviewable=false
			case "foreign-arch":f.plan.Packages[0].Name="wireguard-go:amd64"
			}
			if kind!="wrong-approval" && kind!="modified-hash" {
				f.plan.PlanSHA256=setupPlanDigest(f.plan);approved=f.plan.PlanSHA256
			}
			if err:=validateSetupApproval(f.plan,approved);err==nil {t.Fatal("accepted invalid approval")}
			if len(f.calls)!=0 {t.Fatal("mutation on invalid approval")}
		})
	}
}

func TestApplySetupBlocksDriftBeforeMutation(t *testing.T) {
	for _,kind:=range []string{"package","approved-version","target","kernel","hardware","memory","dependencies","preflight","cancel"} {
		t.Run(kind,func(t *testing.T) {
			f:=newApplyFixture(t,true,true)
			switch kind {
			case "package":f.installed["python3"]="3.13"
			case "approved-version":f.installed["wireguard-go"]="2.0"
			case "target":f.target="rescue.target"
			case "kernel":
				read:=f.deps.Source.ReadFile
				f.deps.Source.ReadFile=func(path string)([]byte,error){if path=="/proc/sys/kernel/osrelease"{return []byte("different"),nil};return read(path)}
			case "hardware","memory":
				p,_:=f.deps.Preflight(context.Background(),f.plan)
				if kind=="hardware"{p.Hardware.Fingerprint="different"}else{p.Resources.Admission.Eligible=false}
				f.deps.Preflight=func(context.Context,SetupPlan)(Plan,error){return p,nil}
			case "preflight":f.deps.Preflight=func(context.Context,SetupPlan)(Plan,error){return Plan{},fmt.Errorf("failed")}
			case "dependencies":
				out:=f.deps.Source.Output
				f.deps.Source.Output=func(ctx context.Context,name string,args ...string)([]byte,error){
					if name=="apt-get"{return []byte("Inst unexpected (1.0 Ubuntu [arm64])\nConf unexpected (1.0 Ubuntu [arm64])"),nil}
					return out(ctx,name,args...)
				}
			}
			ctx,cancel:=context.WithCancel(context.Background())
			defer cancel()
			if kind=="cancel"{cancel()}
			if err:=applySetup(ctx,f.plan,f.plan.PlanSHA256,&f.journal,f.deps);err==nil {t.Fatal("drift accepted")}
			if len(f.calls)!=0 || f.journal.State!="failed" {t.Fatalf("mutation before validation: %v %+v",f.calls,f.journal)}
		})
	}
}

func TestApplySetupJournalFailureAndPostconditions(t *testing.T) {
	for _,kind:=range []string{"save-before","no-package","unapproved-dependency","no-headless","state-after"} {
		t.Run(kind,func(t *testing.T) {
			f:=newApplyFixture(t,true,true)
			run:=f.deps.Run
			switch kind {
			case "save-before":
				f.deps.Save=func(setupJournal)error{return fmt.Errorf("disk full")}
			case "state-after":
				f.deps.Save=func(j setupJournal)error{
					if j.Stage=="packages" && j.State=="complete"{return fmt.Errorf("disk full")}
					return nil
				}
			default:
				f.deps.Run=func(ctx context.Context,args []string)error{
					if kind=="no-package" || (kind=="no-headless" && args[0]=="systemctl"){return nil}
					err:=run(ctx,args)
					if kind=="unapproved-dependency"{f.installed["unexpected"]="1.0"}
					return err
				}
			}
			if err:=f.apply();err==nil{t.Fatal("failure ignored")}
			if kind=="save-before" && len(f.calls)!=0{t.Fatal("mutation before durable journal")}
			if kind=="state-after" {
				f.deps.Save=func(setupJournal)error{return nil}
				if err:=f.apply();err!=nil{t.Fatal(err)}
				if len(f.calls)!=2{t.Fatal("reran completed packages",f.calls)}
			}
		})
	}
}

func TestSetupJSONRejectsAmbiguity(t *testing.T) {
	f:=newApplyFixture(t,false,false)
	raw,_:=json.Marshal(f.plan)
	var parsed SetupPlan
	if err:=decodeSetupJSON(raw,&parsed);err!=nil{t.Fatal(err)}
	for _,bad:=range []string{
		string(raw)+" {}",
		strings.Replace(string(raw),"{",`{"schema_version":2,`,1),
		strings.Replace(string(raw),"{",`{"SCHEMA_VERSION":2,`,1),
		strings.Replace(string(raw),"{",`{"unknown_field":true,`,1),
		`{"nested":{"a":1,"a":2}}`,
		strings.Repeat(" ",(2<<20)+1),
	} {
		if err:=decodeSetupJSON([]byte(bad),&parsed);err==nil{t.Fatal("accepted ambiguous JSON")}
	}
}

func TestApplyDoesNotTreatChangingFreeMemoryAsIdentityDrift(t *testing.T) {
	f:=newApplyFixture(t,false,false)
	p,_:=f.deps.Preflight(context.Background(),f.plan)
	p.Resources.Resources.AvailableSystemBytes--
	f.deps.Preflight=func(context.Context,SetupPlan)(Plan,error){return p,nil}
	if err:=f.apply();err!=nil{t.Fatal(err)}
}

