//go:build linux

package installer

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/FyberLabs/hypermesh-host/internal/agent"
)

type workloadPlan struct {
	Schema int `json:"schema"`
	SHA256 string `json:"plan_sha256"`
	Setup SetupPlan `json:"setup"`
	Request agent.WorkloadPreflight `json:"request"`
}
type workloadJournal struct {
	Schema int `json:"schema"`
	Plan workloadPlan `json:"plan"`
	State string `json:"state"`
	StartedAt time.Time `json:"started_at"`
	RecordedAt time.Time `json:"recorded_at"`
	Receipt *agent.WorkloadReceipt `json:"receipt,omitempty"`
	FailureCode string `json:"failure_code,omitempty"`
	NextAction string `json:"next_action"`
}
func workloadDigest(p workloadPlan)string{
	p.SHA256="";p.Request.PlanSHA256=""
	raw,_:=json.Marshal(p);return digestSetupBytes(raw)
}
func validateWorkloadPlan(p workloadPlan,approved string)error{
	if err:=validateWorkloadShape(p,approved);err!=nil{return err}
	return validateSetupApproval(p.Setup,p.Setup.PlanSHA256)
}
func validateWorkloadShape(p workloadPlan,approved string)error{
	if p.Schema!=1||!setupDigestRE.MatchString(approved)||p.SHA256!=approved||
		p.Request.PlanSHA256!=approved||workloadDigest(p)!=approved{return fmt.Errorf("workload approval does not match the reviewed plan")}
	if p.Setup.SchemaVersion!=2||!setupDigestRE.MatchString(p.Setup.PlanSHA256)||setupPlanDigest(p.Setup)!=p.Setup.PlanSHA256{return fmt.Errorf("invalid embedded setup plan")}
	if p.Setup.Preflight.Hardware.Backend!="cuda"{return fmt.Errorf("pinned workload preflight currently requires the NVIDIA CUDA setup profile")}
	if err:=agent.ValidateWorkloadPreflight(p.Request);err!=nil{return err}
	dir:=p.Setup.Preflight.Resources.ModelDirectory
	rel,err:=filepath.Rel(dir,p.Request.ModelPath)
	if err!=nil||rel=="."||rel==".."||strings.HasPrefix(rel,".."+string(filepath.Separator))||
		strings.ContainsAny(p.Request.ModelPath,":\r\n\x00"){
		return fmt.Errorf("model must be a regular file inside the reviewed models directory")
	}
	return nil
}
func workloadNext(state string)string{
	switch state{
	case "verified":return "Dated model inference and cleanup succeeded. Inspect receipt.context_fit for full-window evidence when requested. This is not sustained benchmarking, certification, activation or permission to sell."
	case "checking":return "Check may still be running or have been interrupted. Inspect the named temporary container before retrying; do not treat this journal as success."
	default:return "Workload check failed or is unconfirmed. Inspect model pins, runtime health and temporary containers; preserve installer and enrollment state, then retry the reviewed plan."
	}
}
func validWorkloadReceipt(p workloadPlan,r agent.WorkloadReceipt)bool{
	fitOK:=r.ContextFit==nil
	if p.Request.FullContext {fitOK=r.ContextFit!=nil&&r.ContextFit.ValidFor(p.Request.ContextTokens)}
	return fitOK&&sameSetupJSON(r.Request,p.Request)&&r.CleanupComplete&&
		strings.HasPrefix(r.HostImageHash,"sha256:")&&setupDigestRE.MatchString(strings.TrimPrefix(r.HostImageHash,"sha256:"))&&
		r.SampleMS>0&&r.TokensPerSecond>0&&!math.IsInf(r.TokensPerSecond,0)&&!math.IsNaN(r.TokensPerSecond)
}
func validWorkloadJournal(j workloadJournal)bool{
	if j.Schema!=1||validateWorkloadShape(j.Plan,j.Plan.SHA256)!=nil||
		j.StartedAt.IsZero()||j.RecordedAt.Before(j.StartedAt){return false}
	switch j.State{
	case "checking":return j.Receipt==nil&&j.FailureCode==""
	case "failed":return j.Receipt==nil&&supportOneOf(j.FailureCode,"preflight","runtime","interrupted","status_persistence")
	case "verified":return j.FailureCode==""&&j.Receipt!=nil&&validWorkloadReceipt(j.Plan,*j.Receipt)
	}
	return false
}
func workloadJournalPath(state,sha string)string{return filepath.Join(state,"workload-"+sha+".json")}
func saveWorkloadJournal(path string,j workloadJournal)error{
	if !validWorkloadJournal(j){return fmt.Errorf("invalid workload journal")}
	if err:=secureSetupDirectory(filepath.Dir(path),false);err!=nil{return err}
	if _,err:=os.Lstat(path);err==nil{
		if _,err=readSetupRegular(path,true);err!=nil{return fmt.Errorf("unsafe workload journal")}
	}else if !os.IsNotExist(err){return fmt.Errorf("cannot inspect workload journal")}
	raw,err:=json.Marshal(j);if err!=nil{return fmt.Errorf("cannot encode workload journal")}
	f,err:=os.CreateTemp(filepath.Dir(path),".workload-");if err!=nil{return err}
	defer os.Remove(f.Name())
	if _,err=f.Write(raw);err!=nil{f.Close();return err}
	if err=f.Sync();err!=nil{f.Close();return err}
	if err=f.Close();err!=nil{return err}
	if err=os.Rename(f.Name(),path);err!=nil{return err}
	d,err:=os.Open(filepath.Dir(path));if err!=nil{return err};defer d.Close()
	return d.Sync()
}
type workloadDeps struct{
	Preflight func(context.Context,workloadPlan)error
	Run func(context.Context,agent.WorkloadPreflight)(agent.WorkloadReceipt,error)
	Save func(workloadJournal)error
}
func checkWorkload(ctx context.Context,p workloadPlan,approved string,j *workloadJournal,d workloadDeps)error{
	if err:=validateWorkloadPlan(p,approved);err!=nil{return err}
	now:=time.Now().UTC()
	*j=workloadJournal{Schema:1,Plan:p,State:"checking",StartedAt:now,RecordedAt:now,NextAction:workloadNext("checking")}
	if d.Save(*j)!=nil{return fmt.Errorf("cannot save workload intent")}
	fail:=func(code string)error{
		j.State="failed";j.Receipt=nil;j.FailureCode=code;j.RecordedAt=time.Now().UTC();j.NextAction=workloadNext("failed")
		if d.Save(*j)!=nil{return fmt.Errorf("workload failed and status could not be saved; preserve state")}
		return fmt.Errorf("workload check failed (%s); see next_action",code)
	}
	if ctx.Err()!=nil{return fail("interrupted")}
	if d.Preflight(ctx,p)!=nil{return fail("preflight")}
	r,err:=d.Run(ctx,p.Request)
	if err!=nil||!validWorkloadReceipt(p,r){return fail("runtime")}
	if ctx.Err()!=nil{return fail("interrupted")}
	if d.Preflight(ctx,p)!=nil{return fail("preflight")}
	if ctx.Err()!=nil{return fail("interrupted")}
	complete:=*j;complete.State="verified";complete.RecordedAt=time.Now().UTC();complete.Receipt=&r;complete.NextAction=workloadNext("verified")
	if d.Save(complete)!=nil{return fail("status_persistence")}
	*j=complete;return nil
}
func workloadPreflight(ctx context.Context,p workloadPlan)error{
	if err:=inactiveMemoryServices(ctx,setupSource{});err!=nil{return err}
	if _,err:=os.Lstat("/var/lib/hypermesh/lease.json");!os.IsNotExist(err){return fmt.Errorf("reconcile existing lease state before preflight")}
	resolved,err:=filepath.EvalSymlinks(p.Request.ModelPath)
	if err!=nil||resolved!=p.Request.ModelPath{return fmt.Errorf("model path must exist without symlinks")}
	info,err:=os.Lstat(resolved)
	if err!=nil||!info.Mode().IsRegular(){return fmt.Errorf("model is not a regular file")}
	remaining,target,err:=validateSetupHost(ctx,p.Setup,setupApplyDeps{Source:setupSource{},Preflight:freshSetupPreflight})
	if err!=nil{return err}
	if len(remaining)>0||(p.Setup.HeadlessRequested&&target!="multi-user.target"){return fmt.Errorf("apply approved prerequisites first")}
	return inactiveMemoryServices(ctx,setupSource{})
}

// Fixed local Docker daemon, isolated client configuration and no inherited
// proxy, credential-helper, PATH or DOCKER_HOST override enter root execution.
func workloadDocker(ctx context.Context,config string,args ...string)([]byte,error){
	ctx,cancel:=context.WithTimeout(ctx,2*time.Minute);defer cancel()
	cmd:=exec.CommandContext(ctx,"/usr/bin/docker",append([]string{"--config",config,"--host","unix:///var/run/docker.sock"},args...)...)
	cmd.Env=[]string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin","HOME=/nonexistent","DOCKER_CONFIG="+config,"LC_ALL=C"}
	cmd.SysProcAttr=&syscall.SysProcAttr{Setpgid:true}
	cmd.Cancel=func()error{return syscall.Kill(-cmd.Process.Pid,syscall.SIGKILL)}
	cmd.WaitDelay=time.Second
	var output boundedProbeOutput
	cmd.Stdout=&output;cmd.Stderr=io.Discard
	if err:=cmd.Run();err!=nil{return nil,fmt.Errorf("Docker operation failed")}
	return output.Bytes(),nil
}

func RunPlanWorkload(ctx context.Context,args []string,out,diagnostic io.Writer)int{
	f:=flag.NewFlagSet("plan-workload",flag.ContinueOnError);f.SetOutput(diagnostic)
	setup:=f.String("setup-plan","","completed reviewed prerequisite plan")
	catalog:=f.String("catalog-id","","catalog identity for the local model")
	image:=f.String("image-digest","","reviewed OCI repo@sha256 runtime pin")
	model:=f.String("model-file","","existing GGUF inside the reviewed models directory")
	hash:=f.String("model-sha256","","reviewed sha256:hex model pin")
	tokens:=f.Int("context-tokens",4096,"runtime context allocation")
	full:=f.Bool("full-context",false,"approve a full token-window exercise in addition to short inference")
	if err:=f.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
	if f.NArg()!=0||*setup==""{fmt.Fprintln(diagnostic,"require --setup-plan and explicit model/runtime pins");return 2}
	var p workloadPlan;p.Schema=1
	raw,err:=readSetupRegular(*setup,false)
	if err==nil{err=decodeSetupJSON(raw,&p.Setup)}
	if err!=nil{fmt.Fprintln(diagnostic,"cannot read setup plan");return 1}
	p.Request=agent.WorkloadPreflight{CatalogID:*catalog,ImageDigest:*image,ModelPath:*model,FileHash:*hash,ContextTokens:*tokens,FullContext:*full}
	p.SHA256=workloadDigest(p);p.Request.PlanSHA256=p.SHA256
	if err:=validateWorkloadPlan(p,p.SHA256);err!=nil{fmt.Fprintln(diagnostic,err);return 1}
	if json.NewEncoder(out).Encode(p)!=nil{return 1};return 0
}
func RunCheckWorkload(ctx context.Context,args []string,out,diagnostic io.Writer)int{
	return runCheckWorkload(ctx,args,out,diagnostic,defaultSetupState,os.Geteuid()==0,nil)
}
func runCheckWorkload(ctx context.Context,args []string,out,diagnostic io.Writer,state string,root bool,override *workloadDeps)int{
	f:=flag.NewFlagSet("check-workload",flag.ContinueOnError);f.SetOutput(diagnostic)
	path:=f.String("plan","","reviewed workload plan")
	approved:=f.String("approve-plan","","exact workload plan_sha256")
	run:=f.Bool("run",false,"approve pulling the pinned runtime and executing the local model")
	if err:=f.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
	if f.NArg()!=0||*path==""||!setupDigestRE.MatchString(*approved)||!*run{fmt.Fprintln(diagnostic,"require --plan FILE --approve-plan SHA256 --run");return 2}
	if !root{fmt.Fprintln(diagnostic,"workload execution requires root and inactive host services");return 1}
	var p workloadPlan
	raw,err:=readSetupRegular(*path,false)
	if err==nil{err=decodeSetupJSON(raw,&p)}
	if err==nil{err=validateWorkloadPlan(p,*approved)}
	if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
	unlock,err:=lockSetupState(state);if err!=nil{fmt.Fprintln(diagnostic,err);return 1};defer unlock()
	ctx,cancel:=context.WithTimeout(ctx,8*time.Minute);defer cancel()
	d:=workloadDeps{
		Preflight:workloadPreflight,
		Run:func(ctx context.Context,r agent.WorkloadPreflight)(agent.WorkloadReceipt,error){
			config,err:=os.MkdirTemp(state,".workload-docker-")
			if err!=nil{return agent.WorkloadReceipt{},fmt.Errorf("cannot create private Docker configuration")}
			defer os.RemoveAll(config)
			return agent.RunWorkloadPreflight(ctx,r,"/var/lib/hypermesh",func(ctx context.Context,_ string,args ...string)([]byte,error){
				return workloadDocker(ctx,config,args...)
			})
		},
		Save:func(j workloadJournal)error{return saveWorkloadJournal(workloadJournalPath(state,p.SHA256),j)},
	}
	if override!=nil{d.Preflight=override.Preflight;d.Run=override.Run}
	var j workloadJournal
	err=checkWorkload(ctx,p,*approved,&j,d)
	if json.NewEncoder(out).Encode(j)!=nil{return 1}
	if err!=nil{fmt.Fprintln(diagnostic,err);return 1};return 0
}
func RunWorkloadStatus(_ context.Context,args []string,out,diagnostic io.Writer)int{
	f:=flag.NewFlagSet("workload-status",flag.ContinueOnError);f.SetOutput(diagnostic)
	sha:=f.String("plan-sha","","workload plan_sha256")
	if err:=f.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
	if f.NArg()!=0||!setupDigestRE.MatchString(*sha){fmt.Fprintln(diagnostic,"require --plan-sha SHA256");return 2}
	if err:=secureSetupDirectory(defaultSetupState,false);err!=nil{fmt.Fprintln(diagnostic,err);return 1}
	var j workloadJournal
	raw,err:=readSetupRegular(workloadJournalPath(defaultSetupState,*sha),true)
	if err==nil{err=decodeSetupJSON(raw,&j)}
	if err!=nil||!validWorkloadJournal(j)||j.Plan.SHA256!=*sha{fmt.Fprintln(diagnostic,"workload status unavailable or invalid");return 1}
	j.NextAction=workloadNext(j.State)
	if json.NewEncoder(out).Encode(j)!=nil{return 1};return 0
}
