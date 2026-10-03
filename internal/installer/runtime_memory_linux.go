//go:build linux

package installer

import (
 "bytes"
 "context"
 "encoding/json"
 "flag"
 "fmt"
 "io"
 "os"
 "os/exec"
 "path/filepath"
 "strings"
 "syscall"
 "time"
)

type runtimeMemoryRequest struct {
 Schema int `json:"schema"`
 Backend string `json:"backend"`
 Device int `json:"device"`
 AllocationBytes int64 `json:"allocation_bytes"`
 TotalBytes int64 `json:"total_bytes"`
 SystemBytes int64 `json:"system_bytes"`
}
type runtimeMemoryReceipt struct {
 Schema int `json:"schema"`
 Backend string `json:"backend"`
 Device int `json:"device"`
 AllocationBytes int64 `json:"allocation_bytes"`
 TouchedBytes int64 `json:"touched_bytes"`
 SampledBytes int64 `json:"sampled_bytes"`
 CleanupComplete bool `json:"cleanup_complete"`
}
type runtimeMemoryJournal struct {
 Schema int `json:"schema"`
 SetupPlanSHA256 string `json:"setup_plan_sha256"`
 State string `json:"state"`
 StartedAt time.Time `json:"started_at"`
 RecordedAt time.Time `json:"recorded_at"`
 Request runtimeMemoryRequest `json:"request"`
 Receipt *runtimeMemoryReceipt `json:"receipt,omitempty"`
 FailureCode string `json:"failure_code,omitempty"`
 NextAction string `json:"next_action"`
}

func memoryRequest(p SetupPlan)(runtimeMemoryRequest,error) {
 r:=p.Preflight.Resources.Resources
 if r.UMAAllocationBytes==0 {return runtimeMemoryRequest{},fmt.Errorf("installation has no reserved memory budget; use a separately reviewed explicit diagnostic allocation or model workload test")}
 if p.Preflight.Resources.MemoryMode!="uma"||!r.UMA||!validUMARuntime(r.UMARuntime)||
  !CheckResources(r).Eligible||r.UMARuntime.Backend!=p.Preflight.Hardware.Backend {
  return runtimeMemoryRequest{},fmt.Errorf("runtime-memory check requires an eligible single CUDA/HIP UMA plan; discrete and multi-GPU workload validation remain pending")
 }
 return runtimeMemoryRequest{1,r.UMARuntime.Backend,r.UMARuntime.Device,r.UMAAllocationBytes,r.UMARuntime.TotalBytes,r.SystemBytes},nil
}
func validMemoryRequest(r runtimeMemoryRequest)bool {
 return r.Schema==1&&(r.Backend=="cuda"||r.Backend=="hip")&&r.Device>=0&&r.Device<64&&
  r.SystemBytes>=16*GiB&&r.AllocationBytes>=12*GiB&&r.AllocationBytes<r.TotalBytes
}
func validMemoryReceipt(r runtimeMemoryRequest,v runtimeMemoryReceipt)bool {
 return validMemoryRequest(r)&&v.Schema==1&&v.Backend==r.Backend&&v.Device==r.Device&&
  v.AllocationBytes==r.AllocationBytes&&v.TouchedBytes==r.AllocationBytes&&v.SampledBytes==3*4096&&v.CleanupComplete
}
func validMemoryJournal(j runtimeMemoryJournal)bool {
 if j.Schema!=1||!setupDigestRE.MatchString(j.SetupPlanSHA256)||!validMemoryRequest(j.Request)||
  j.StartedAt.IsZero()||j.RecordedAt.Before(j.StartedAt){return false}
 switch j.State {
 case "checking":return j.Receipt==nil&&j.FailureCode==""
 case "failed":return j.Receipt==nil&&supportOneOf(j.FailureCode,"preflight","runtime","interrupted","status_persistence")
 case "verified":return j.FailureCode==""&&j.Receipt!=nil&&validMemoryReceipt(j.Request,*j.Receipt)
 }
 return false
}
func memoryNextAction(state string)string {
 switch state {
 case "verified":return "Dated UMA allocation/fill/sample/readback/free check passed. Model/context fit, benchmarking, certification and service activation still require separate checks."
 case "checking":return "Memory check is in progress or was interrupted. Confirm the prior child has exited and inspect driver health before retrying the same approved plan; no success is recorded."
 default:return "Memory check is unconfirmed. Inspect memory pressure, installed CUDA/HIP libraries and driver health; rerun preflight before retrying. Preserve enrollment and installer state."
 }
}
func memoryJournalPath(dir,sha string)string{return filepath.Join(dir,"runtime-memory-"+sha+".json")}
func saveMemoryJournal(path string,j runtimeMemoryJournal)error {
 if !validMemoryJournal(j){return fmt.Errorf("invalid runtime-memory journal")}
 if err:=secureSetupDirectory(filepath.Dir(path),false);err!=nil{return err}
 if _,err:=os.Lstat(path);err==nil {
  if _,err=readSetupRegular(path,true);err!=nil{return fmt.Errorf("unsafe runtime-memory journal")}
 }else if !os.IsNotExist(err){return fmt.Errorf("cannot inspect runtime-memory journal")}
 raw,err:=json.Marshal(j);if err!=nil{return fmt.Errorf("cannot encode runtime-memory journal")}
 f,err:=os.CreateTemp(filepath.Dir(path),".runtime-memory-");if err!=nil{return fmt.Errorf("cannot stage runtime-memory journal")}
 defer os.Remove(f.Name())
 if _,err=f.Write(raw);err!=nil{f.Close();return fmt.Errorf("cannot write runtime-memory journal")}
 if err=f.Sync();err!=nil{f.Close();return fmt.Errorf("cannot sync runtime-memory journal")}
 if err=f.Close();err!=nil{return fmt.Errorf("cannot close runtime-memory journal")}
 if err=os.Rename(f.Name(),path);err!=nil{return fmt.Errorf("cannot publish runtime-memory journal")}
 d,err:=os.Open(filepath.Dir(path));if err!=nil{return fmt.Errorf("cannot open runtime-memory state")};defer d.Close()
 if d.Sync()!=nil{return fmt.Errorf("cannot sync runtime-memory state")};return nil
}

// No inherited loader/Python/GPU-visibility overrides enter this privileged probe.
// The fixed script receives only a public resource request, never credentials.
func memoryProbeCommand(ctx context.Context,r runtimeMemoryRequest)*exec.Cmd {
 raw,_:=json.Marshal(r)
 cmd:=exec.CommandContext(ctx,"/usr/bin/python3","-I","-c",umaProbeScript,"--allocate")
 cmd.Env=[]string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin","HOME=/root","LC_ALL=C"}
 cmd.Stdin=bytes.NewReader(raw)
 cmd.SysProcAttr=&syscall.SysProcAttr{Setpgid:true}
 cmd.Cancel=func()error{return syscall.Kill(-cmd.Process.Pid,syscall.SIGKILL)}
 cmd.WaitDelay=time.Second
 return cmd
}
func runMemoryProbe(ctx context.Context,r runtimeMemoryRequest)(runtimeMemoryReceipt,error) {
 return executeMemoryProbe(ctx,r,45*time.Second,umaProbeScript)
}
func executeMemoryProbe(ctx context.Context,r runtimeMemoryRequest,budget time.Duration,script string)(runtimeMemoryReceipt,error) {
 var receipt runtimeMemoryReceipt
 if !validMemoryRequest(r){return receipt,fmt.Errorf("invalid memory allocation request")}
 child,cancel:=context.WithTimeout(ctx,budget);defer cancel()
 cmd:=memoryProbeCommand(child,r)
 cmd.Args[3]=script // fixed embedded script in production; tests exercise child deadlines without a GPU.
 var output boundedProbeOutput
 cmd.Stdout=&output;cmd.Stderr=io.Discard
 if cmd.Run()!=nil||child.Err()!=nil{return receipt,fmt.Errorf("runtime memory operation failed or timed out; no cleanup or allocation success is confirmed")}
 if decodeSetupJSON(output.Bytes(),&receipt)!=nil||!validMemoryReceipt(r,receipt){
  return runtimeMemoryReceipt{},fmt.Errorf("runtime returned incomplete or mismatched memory evidence")
 }
 return receipt,nil
}
func inactiveMemoryServices(ctx context.Context,source setupSource)error {
 for _,unit:=range []string{"hypermesh-agent.service","hypermesh-wg.service"} {
  raw,err:=source.output(ctx,"systemctl","--system","show",unit,"-p","ActiveState","--value")
  state:=strings.TrimSpace(string(raw))
  if err!=nil||(state!="inactive"&&state!="failed"){return fmt.Errorf("Hypermesh services must be inactive before runtime memory allocation")}
 }
 return nil
}
type memoryCheckDeps struct {
 Preflight func(context.Context)error
 Probe func(context.Context,runtimeMemoryRequest)(runtimeMemoryReceipt,error)
 Save func(runtimeMemoryJournal)error
}
func checkRuntimeMemory(ctx context.Context,p SetupPlan,approved string,j *runtimeMemoryJournal,deps memoryCheckDeps)error {
 if err:=validateSetupApproval(p,approved);err!=nil{return err}
 request,err:=memoryRequest(p);if err!=nil{return err}
 now:=time.Now().UTC()
 *j=runtimeMemoryJournal{Schema:1,SetupPlanSHA256:approved,State:"checking",StartedAt:now,RecordedAt:now,Request:request,NextAction:memoryNextAction("checking")}
 // Invalidate prior success durably before any fresh runtime work.
 if deps.Save(*j)!=nil{return fmt.Errorf("cannot save runtime-memory intent")}
 fail:=func(code string)error{
  j.State="failed";j.Receipt=nil;j.FailureCode=code;j.RecordedAt=time.Now().UTC();j.NextAction=memoryNextAction("failed")
  if deps.Save(*j)!=nil{return fmt.Errorf("memory check failed and status could not be saved; preserve state")}
  return fmt.Errorf("runtime memory check failed (%s); see next_action",code)
 }
 if deps.Preflight(ctx)!=nil{return fail("preflight")}
 if ctx.Err()!=nil{return fail("interrupted")}
 receipt,err:=deps.Probe(ctx,request)
 if err!=nil||!validMemoryReceipt(request,receipt){return fail("runtime")}
 if ctx.Err()!=nil{return fail("interrupted")}
 // Recheck service inactivity and the approved machine after the child exits.
 if deps.Preflight(ctx)!=nil{return fail("preflight")}
 if ctx.Err()!=nil{return fail("interrupted")}
 complete:=*j;complete.State="verified";complete.RecordedAt=time.Now().UTC();complete.Receipt=&receipt;complete.NextAction=memoryNextAction("verified")
 if deps.Save(complete)!=nil{return fail("status_persistence")}
 *j=complete;return nil
}
func RunRuntimeMemory(ctx context.Context,args []string,out,diagnostic io.Writer)int {
 return runRuntimeMemory(ctx,args,out,diagnostic,defaultSetupState,os.Geteuid()==0,nil)
}
func runRuntimeMemory(ctx context.Context,args []string,out,diagnostic io.Writer,state string,root bool,override *memoryCheckDeps)int {
 flags:=flag.NewFlagSet("check-runtime-memory",flag.ContinueOnError);flags.SetOutput(diagnostic)
 path:=flags.String("setup-plan","","reviewed prerequisite plan")
 approved:=flags.String("approve-plan","","exact setup plan_sha256")
 allocate:=flags.Bool("allocate",false,"explicitly allocate/fill/sample/free the plan's UMA memory budget")
 if err:=flags.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
 if flags.NArg()!=0||*path==""||!setupDigestRE.MatchString(*approved)||!*allocate {fmt.Fprintln(diagnostic,"require --setup-plan FILE --approve-plan SHA256 --allocate");return 2}
 if !root{fmt.Fprintln(diagnostic,"runtime-memory check requires root and inactive host services");return 1}
 var p SetupPlan
 raw,err:=readSetupRegular(*path,false)
 if err==nil{err=decodeSetupJSON(raw,&p)}
 if err==nil{err=validateSetupApproval(p,*approved)}
 if err==nil{_,err=memoryRequest(p)}
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 unlock,err:=lockSetupState(state);if err!=nil{fmt.Fprintln(diagnostic,err);return 1};defer unlock()
 ctx,cancel:=context.WithTimeout(ctx,3*time.Minute);defer cancel()
 deps:=memoryCheckDeps{
  Preflight:func(ctx context.Context)error{
   if err:=inactiveMemoryServices(ctx,setupSource{});err!=nil{return err}
   remaining,target,err:=validateSetupHost(ctx,p,setupApplyDeps{Source:setupSource{},Preflight:freshSetupPreflight})
   if err!=nil{return err}
   if len(remaining)>0||(p.HeadlessRequested&&target!="multi-user.target"){return fmt.Errorf("apply approved prerequisites first")}
   return inactiveMemoryServices(ctx,setupSource{})
  },
  Probe:runMemoryProbe,
  Save:func(j runtimeMemoryJournal)error{return saveMemoryJournal(memoryJournalPath(state,p.PlanSHA256),j)},
 }
 if override!=nil{deps.Preflight=override.Preflight;deps.Probe=override.Probe}
 var j runtimeMemoryJournal
 err=checkRuntimeMemory(ctx,p,*approved,&j,deps)
 if json.NewEncoder(out).Encode(j)!=nil{return 1}
 if err!=nil{fmt.Fprintln(diagnostic,err);return 1};return 0
}
func RunRuntimeMemoryStatus(_ context.Context,args []string,out,diagnostic io.Writer)int {
 flags:=flag.NewFlagSet("runtime-memory-status",flag.ContinueOnError);flags.SetOutput(diagnostic)
 sha:=flags.String("setup-plan-sha","","setup plan_sha256")
 if err:=flags.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
 if flags.NArg()!=0||!setupDigestRE.MatchString(*sha){fmt.Fprintln(diagnostic,"require --setup-plan-sha SHA256");return 2}
 if err:=secureSetupDirectory(defaultSetupState,false);err!=nil{fmt.Fprintln(diagnostic,err);return 1}
 var j runtimeMemoryJournal
 raw,err:=readSetupRegular(memoryJournalPath(defaultSetupState,*sha),true)
 if err==nil{err=decodeSetupJSON(raw,&j)}
 if err!=nil||!validMemoryJournal(j)||j.SetupPlanSHA256!=*sha{fmt.Fprintln(diagnostic,"runtime-memory status unavailable or invalid");return 1}
 j.NextAction=memoryNextAction(j.State)
 if json.NewEncoder(out).Encode(j)!=nil{return 1};return 0
}
