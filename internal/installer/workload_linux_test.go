//go:build linux

package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/agent"
	"github.com/FyberLabs/hypermesh-host/internal/pathb"
)

func workloadFixture(t *testing.T)(workloadPlan,agent.WorkloadReceipt){
	t.Helper()
	setup:=newApplyFixture(t,false,false).plan
	p:=workloadPlan{Schema:1,Setup:setup,Request:agent.WorkloadPreflight{
		CatalogID:"fixture-model",ImageDigest:"example.invalid/runtime@sha256:"+strings.Repeat("a",64),
		ModelPath:filepath.Join(setup.Preflight.Resources.ModelDirectory,"model.gguf"),
		FileHash:"sha256:"+strings.Repeat("b",64),ContextTokens:4096,
	}}
	p.SHA256=workloadDigest(p);p.Request.PlanSHA256=p.SHA256
	if err:=validateWorkloadPlan(p,p.SHA256);err!=nil{t.Fatal(err)}
	return p,agent.WorkloadReceipt{Request:p.Request,HostImageHash:"sha256:"+strings.Repeat("c",64),
		SampleMS:100,TokensPerSecond:10,CleanupComplete:true}
}

func TestWorkloadIntentFailureAndRetry(t *testing.T){
	for _,kind:=range []string{"pass","preflight","postflight","runtime","mismatch","cleanup","cancel","save-intent","save-final"}{
		t.Run(kind,func(t *testing.T){
			p,receipt:=workloadFixture(t)
			var j,durable workloadJournal
			preflights,runs:=0,0
			ctx,cancel:=context.WithCancel(context.Background());defer cancel()
			d:=workloadDeps{
				Preflight:func(context.Context,workloadPlan)error{preflights++;if kind=="preflight"||(kind=="postflight"&&preflights==2){return fmt.Errorf("PRIVATE_HOST")};return nil},
				Run:func(context.Context,agent.WorkloadPreflight)(agent.WorkloadReceipt,error){
					runs++
					if durable.State!="checking"||durable.Receipt!=nil{t.Fatal("execution preceded intent")}
					if kind=="runtime"{return receipt,fmt.Errorf("PRIVATE_RUNTIME")}
					if kind=="mismatch"{receipt.Request.ContextTokens++}
					if kind=="cleanup"{receipt.CleanupComplete=false}
					if kind=="cancel"{cancel()}
					return receipt,nil
				},
				Save:func(v workloadJournal)error{
					if !validWorkloadJournal(v){t.Fatal("invalid journal")}
					if kind=="save-intent"||(kind=="save-final"&&v.State=="verified"){return fmt.Errorf("PRIVATE_SAVE")}
					durable=v;return nil
				},
			}
			err:=checkWorkload(ctx,p,p.SHA256,&j,d)
			if (err==nil)!=(kind=="pass"){t.Fatalf("%s: %v",kind,err)}
			if (kind=="preflight"||kind=="save-intent")&&runs!=0{t.Fatal("unexpected execution")}
			if kind=="pass"{
				if preflights!=2||j.State!="verified"{t.Fatal("postflight missing")}
				d.Run=func(context.Context,agent.WorkloadPreflight)(agent.WorkloadReceipt,error){return receipt,fmt.Errorf("retry failure")}
				if checkWorkload(ctx,p,p.SHA256,&j,d)==nil||durable.Receipt!=nil||durable.State=="verified"{t.Fatal("retry retained stale success")}
			}else if j.Receipt!=nil||j.State=="verified"{t.Fatal("failure recorded as success")}
			raw,_:=json.Marshal(j)
			if strings.Contains(string(raw),"PRIVATE")||(err!=nil&&strings.Contains(err.Error(),"PRIVATE")){t.Fatal("raw diagnostics leaked")}
		})
	}
}

func TestWorkloadApprovalBindsEveryExecutionInput(t *testing.T){
	for _,kind:=range []string{"image","model","hash","context","catalog","build","outside","backend"}{
		t.Run(kind,func(t *testing.T){
			p,_:=workloadFixture(t);approved:=p.SHA256
			switch kind{
			case "image":p.Request.ImageDigest="example.invalid/mutable:latest"
			case "model":p.Request.ModelPath+=".other"
			case "hash":p.Request.FileHash="sha256:"+strings.Repeat("d",64)
			case "context":p.Request.ContextTokens++
			case "catalog":p.Request.CatalogID="another"
			case "build":p.Setup.InstallerBuild++
			case "outside":p.Request.ModelPath="/outside/model.gguf"
			case "backend":p.Setup.Preflight.Hardware.Backend="hip"
			}
			if validateWorkloadPlan(p,approved)==nil{t.Fatal("changed plan accepted")}
			if kind=="outside"||kind=="backend"||kind=="image"{
				p.Setup.PlanSHA256=setupPlanDigest(p.Setup);p.SHA256=workloadDigest(p);p.Request.PlanSHA256=p.SHA256
				if validateWorkloadPlan(p,p.SHA256)==nil{t.Fatal("unsafe plan accepted after rehash")}
			}
		})
	}
}

func TestWorkloadCLIDemandsExplicitRunAndRoot(t *testing.T){
	for _,args:=range [][]string{{},{"--plan","anything","--approve-plan",strings.Repeat("a",64)},{"--plan","anything","--approve-plan",strings.Repeat("a",64),"--run"}}{
		var out,diagnostic bytes.Buffer
		if runCheckWorkload(context.Background(),args,&out,&diagnostic,t.TempDir(),false,nil)==0{t.Fatal("unapproved or non-root execution")}
		if out.Len()!=0{t.Fatal("started execution")}
	}
}

func TestWorkloadPrivateJournalRejectsSymlink(t *testing.T){
	p,r:=workloadFixture(t);now:=time.Now().UTC()
	j:=workloadJournal{Schema:1,Plan:p,State:"verified",StartedAt:now,RecordedAt:now,Receipt:&r}
	state:=t.TempDir();if err:=os.Chmod(state,0700);err!=nil{t.Fatal(err)}
	path:=workloadJournalPath(state,p.SHA256)
	if err:=saveWorkloadJournal(path,j);err!=nil{t.Fatal(err)}
	info,err:=os.Stat(path);if err!=nil||info.Mode().Perm()!=0600{t.Fatal("journal not private")}
	if err:=os.Remove(path);err!=nil{t.Fatal(err)}
	target:=filepath.Join(t.TempDir(),"target");if err:=os.WriteFile(target,[]byte("unchanged"),0600);err!=nil{t.Fatal(err)}
	if err:=os.Symlink(target,path);err!=nil{t.Fatal(err)}
	if saveWorkloadJournal(path,j)==nil{t.Fatal("followed symlink")}
	raw,_:=os.ReadFile(target);if string(raw)!="unchanged"{t.Fatal("modified symlink target")}
}

func TestWorkloadHistorySurvivesInstallerBuildChange(t *testing.T){
	p,r:=workloadFixture(t)
	p.Setup.InstallerBuild++
	p.Setup.PlanSHA256=setupPlanDigest(p.Setup)
	p.SHA256=workloadDigest(p);p.Request.PlanSHA256=p.SHA256;r.Request=p.Request
	now:=time.Now().UTC()
	j:=workloadJournal{Schema:1,Plan:p,State:"verified",StartedAt:now,RecordedAt:now,Receipt:&r}
	if !validWorkloadJournal(j){t.Fatal("historical evidence rejected")}
	if validateWorkloadPlan(p,p.SHA256)==nil{t.Fatal("old-build approval accepted for fresh execution")}
}

func TestFullContextApprovalAndReceiptBinding(t *testing.T){
	p,r:=workloadFixture(t)
	old:=p.SHA256
	p.Request.FullContext=true
	if validateWorkloadPlan(p,old)==nil{t.Fatal("old short-check approval authorized full-context work")}
	p.SHA256=workloadDigest(p);p.Request.PlanSHA256=p.SHA256;r.Request=p.Request
	if validateWorkloadPlan(p,p.SHA256)!=nil{t.Fatal("valid full-context plan rejected")}
	if validWorkloadReceipt(p,r){t.Fatal("short receipt accepted as full-context evidence")}
	r.ContextFit=&pathb.ContextFit{Protocol:pathb.ContextFitProtocol,ContextTokens:4096,PromptTokens:4080,GeneratedTokens:16,ElapsedMS:100}
	if !validWorkloadReceipt(p,r){t.Fatal("valid context evidence rejected")}
	r.ContextFit.ContextTokens=8192
	if validWorkloadReceipt(p,r){t.Fatal("another context size accepted")}
}
