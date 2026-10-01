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
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/FyberLabs/hypermesh-host/deploy/agx"
	hostlib "github.com/FyberLabs/hypermesh-host/deploy/lib"
	"github.com/FyberLabs/hypermesh-host/internal/update"
)

type agentFile struct {
	Path string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode uint32 `json:"mode"`
}
type agentInstallPlan struct {
	Schema int `json:"schema"`
	SHA256 string `json:"plan_sha256"`
	Setup SetupPlan `json:"setup"`
	ReleaseURL string `json:"release_url"`
	Channel string `json:"channel"`
	PublicKey string `json:"public_key"`
	Fingerprints []string `json:"fingerprints"`
	ControlPlane string `json:"control_plane"`
	TenantID string `json:"tenant_id"`
	HostName string `json:"host_name"`
	Release releaseMetadata `json:"release"`
	Files []agentFile `json:"files"`
}
type agentPayload struct { agentFile; Data []byte }
type agentInstallJournal struct {
	Schema int `json:"schema"`
	PlanSHA256 string `json:"plan_sha256"`
	State string `json:"state"`
	Completed []string `json:"completed"`
	Error string `json:"error,omitempty"`
}
func agentPlanDigest(p agentInstallPlan)string {
	p.SHA256=""
	raw,_:=json.Marshal(p)
	return digestSetupBytes(raw)
}
var agentTenantRE=regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var agentNameRE=regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

func validateAgentInputs(p agentInstallPlan)error {
	if p.Schema!=1 {return fmt.Errorf("unsupported agent plan schema")}
	if err:=validateSetupApproval(p.Setup,p.Setup.PlanSHA256);err!=nil{return err}
	if validateEndpoint(p.ControlPlane)!=nil || !agentTenantRE.MatchString(p.TenantID) || !agentNameRE.MatchString(p.HostName) {
		return fmt.Errorf("require an HTTPS control plane, tenant UUID and a 1-63 character host name")
	}
	if !strings.Contains(p.PublicKey,"-----BEGIN PGP PUBLIC KEY BLOCK-----") || strings.Contains(p.PublicKey,"PRIVATE KEY") || len(p.PublicKey)>1<<20 {return fmt.Errorf("trusted public key is empty or too large")}
	fps,err:=update.ParseFingerprints(strings.Join(p.Fingerprints,","))
	if err!=nil || len(fps)==0 || !sameSetupJSON(fps,p.Fingerprints) {return fmt.Errorf("invalid signing fingerprints")}
	if p.Setup.InstallerBuild<=0 {return fmt.Errorf("agent installation requires a release-built installer")}
	dir:=p.Setup.Preflight.Resources.ModelDirectory
	if !filepath.IsAbs(dir) || filepath.Clean(dir)!=dir {return fmt.Errorf("model directory must be a clean absolute path")}
	for _,s:=range []string{p.ControlPlane,dir} {
		for _,r:=range s {if r<32 || r>126 {return fmt.Errorf("configuration must use printable ASCII")}}
	}
	return validateDownload(agentDownloadOptions(p,"unused"))
}
func agentDownloadOptions(p agentInstallPlan,output string)downloadOptions {
	return downloadOptions{BaseURL:p.ReleaseURL,Channel:p.Channel,Arch:normalizedSetupArch(p.Setup.Preflight.Hardware.Facts.Arch),
		Output:output,Artifact:"agent",MatchInstaller:true,MinimumBuild:int64(p.Setup.InstallerBuild),
		Key:[]byte(p.PublicKey),Fingerprints:p.Fingerprints}
}
func fetchAgentPayload(ctx context.Context,p agentInstallPlan)(releaseMetadata,[]byte,error) {
	dir,err:=os.MkdirTemp("","hypermesh-agent-")
	if err!=nil{return releaseMetadata{},nil,fmt.Errorf("cannot create private download directory")}
	defer os.RemoveAll(dir)
	path:=filepath.Join(dir,"agent")
	meta,err:=downloadCLI(ctx,agentDownloadOptions(p,path),fetchArtifact,update.GPGVerify)
	if err!=nil{return meta,nil,err}
	raw,err:=os.ReadFile(path)
	return meta,raw,err
}
func agentEnvValue(s string)string {
	return "\""+strings.NewReplacer("\\","\\\\","\"","\\\"").Replace(s)+"\""
}
func agentPayloads(p agentInstallPlan, binary []byte)[]agentPayload {
	env:="# Written by hypermesh-deploy. Enrollment credentials are not stored here.\n"
	values:=[][2]string{
		{"HYPERMESH_CONTROL_PLANE_URL",p.ControlPlane},{"HYPERMESH_TENANT_ID",p.TenantID},
		{"HYPERMESH_HOST_NAME",p.HostName},{"HYPERMESH_STATE_DIR","/var/lib/hypermesh"},
		{"HYPERMESH_GGUF_PATH",filepath.Join(p.Setup.Preflight.Resources.ModelDirectory,"model.gguf")},
		{"HYPERMESH_WG_USERSPACE","0"},{"HYPERMESH_UPDATE_CHANNEL",p.Channel},
		{"HYPERMESH_UPDATE_EVERY","1h"},{"HYPERMESH_UPDATE_PUBKEY","/etc/hypermesh/update-pubkey.asc"},
		{"HYPERMESH_UPDATE_FINGERPRINTS",strings.Join(p.Fingerprints,",")},
		{"HYPERMESH_UPDATE_BINARY","/usr/local/bin/hypermesh-agent"},
	}
	if p.Setup.WireGuardMode=="userspace" {values[5][1]="1"}
	for _,v:=range values {env+=v[0]+"="+agentEnvValue(v[1])+"\n"}
	unit:="[Unit]\nDescription=Hypermesh host agent\nAfter=network-online.target docker.service\nWants=network-online.target\n"
	if p.Setup.WireGuardMode=="userspace" {unit+="Requires=hypermesh-wg.service\nAfter=hypermesh-wg.service\n"}
	unit+="ConditionPathExists=/var/lib/hypermesh/activation-approved\n\n[Service]\nType=simple\nUser=root\nEnvironmentFile=/etc/hypermesh/agent.env\nExecStartPre=/usr/local/lib/hypermesh/update-rollback\nExecStart=/usr/local/bin/hypermesh-agent\nRestart=on-failure\nRestartSec=5\nKillMode=control-group\n\n[Install]\nWantedBy=multi-user.target\n"
	var files []agentPayload
	add:=func(path string,data []byte,mode uint32){files=append(files,agentPayload{agentFile{path,digestSetupBytes(data),mode},data})}
	add("/usr/local/bin/hypermesh-agent",binary,0o755)
	add("/usr/local/lib/hypermesh/update-rollback",hostlib.Rollback,0o755)
	add("/etc/hypermesh/update-pubkey.asc",[]byte(p.PublicKey),0o644)
	add("/etc/hypermesh/agent.env",[]byte(env),0o600)
	if p.Setup.WireGuardMode=="userspace" {
		wg:=strings.Replace(string(agx.WGUnit),"[Service]","ConditionPathExists=/var/lib/hypermesh/activation-approved\n\n[Service]",1)
		add("/etc/systemd/system/hypermesh-wg.service",[]byte(wg),0o644)
	}
	add("/etc/systemd/system/hypermesh-agent.service",[]byte(unit),0o644)
	return files
}
func agentFileManifest(files []agentPayload)[]agentFile {
	out:=make([]agentFile,0,len(files))
	for _,f:=range files {out=append(out,f.agentFile)}
	return out
}

func RunPlanAgent(ctx context.Context,args []string,out,diagnostic io.Writer)int {
	flags:=flag.NewFlagSet("plan-agent",flag.ContinueOnError);flags.SetOutput(diagnostic)
	setup:=flags.String("setup-plan","","completed prerequisite plan")
	key:=flags.String("trusted-key","","independently trusted public key")
	fingerprints:=flags.String("fingerprints","","trusted signing fingerprints")
	p:=agentInstallPlan{Schema:1}
	flags.StringVar(&p.ReleaseURL,"release-url","","immutable HTTPS release directory")
	flags.StringVar(&p.Channel,"channel","","test or prod")
	flags.StringVar(&p.ControlPlane,"control-plane-url","","HTTPS control plane")
	flags.StringVar(&p.TenantID,"tenant-id","","tenant UUID")
	flags.StringVar(&p.HostName,"host-name","","reported enrollment name")
	if err:=flags.Parse(args);err!=nil {if err==flag.ErrHelp{return 0};return 2}
	if flags.NArg()!=0 || *setup=="" || *key=="" {fmt.Fprintln(diagnostic,"require --setup-plan and --trusted-key");return 2}
	raw,err:=readSetupRegular(*setup,false)
	if err==nil {err=decodeSetupJSON(raw,&p.Setup)}
	if err==nil {raw,err=readSetupRegular(*key,false);p.PublicKey=string(raw)}
	if err==nil {p.Fingerprints,err=update.ParseFingerprints(*fingerprints)}
	if err==nil {err=validateAgentInputs(p)}
	if err!=nil {fmt.Fprintln(diagnostic,err);return 1}
	ctx,cancel:=context.WithTimeout(ctx,10*time.Minute);defer cancel()
	deps:=setupApplyDeps{Source:setupSource{},Preflight:freshSetupPreflight}
	remaining,_,err:=validateSetupHost(ctx,p.Setup,deps)
	if err==nil && len(remaining)>0 {err=fmt.Errorf("apply prerequisite packages before planning agent installation")}
	if err==nil && p.Setup.HeadlessRequested {
		raw,err=deps.Source.output(ctx,"systemctl","get-default")
		if err==nil && strings.TrimSpace(string(raw))!="multi-user.target" {err=fmt.Errorf("apply the approved headless target first")}
	}
	if err!=nil {fmt.Fprintln(diagnostic,err);return 1}
	meta,binary,err:=fetchAgentPayload(ctx,p)
	if err!=nil {fmt.Fprintln(diagnostic,err);return 1}
	p.Release=meta;p.Files=agentFileManifest(agentPayloads(p,binary));p.SHA256=agentPlanDigest(p)
	encoder:=json.NewEncoder(out);encoder.SetIndent("","  ")
	if err=encoder.Encode(p);err!=nil{return 1}
	return 0
}

// All production destinations are fixed. root is used only by filesystem tests.
func agentDest(root,path string)string {
	if root=="" {return path}
	return filepath.Join(root,strings.TrimPrefix(path,"/"))
}
func agentDirectory(path string,create bool)error {
	if !filepath.IsAbs(path)||filepath.Clean(path)!=path{return fmt.Errorf("invalid installation directory")}
	current:="/"
	for _,part:=range strings.Split(strings.TrimPrefix(path,"/"),"/") {
		current=filepath.Join(current,part)
		info,err:=os.Lstat(current)
		if os.IsNotExist(err)&&create {err=os.Mkdir(current,0o755);if err==nil {info,err=os.Lstat(current)}}
		if err!=nil || !info.IsDir() || info.Mode()&os.ModeSymlink!=0{return fmt.Errorf("installation path requires real directories")}
		st,ok:=info.Sys().(*syscall.Stat_t)
		if !ok || (st.Uid!=0&&st.Uid!=uint32(os.Geteuid())) {return fmt.Errorf("installation directory ownership is unsafe")}
		sticky:=st.Uid==0&&info.Mode()&os.ModeSticky!=0&&current!=path
		if info.Mode().Perm()&0o022!=0&&!sticky {return fmt.Errorf("installation directory is writable by others")}
	}
	return nil
}
func agentFileMatches(path string,f agentPayload)(bool,error) {
	info,err:=os.Lstat(path)
	if os.IsNotExist(err){return false,nil}
	if err!=nil || !info.Mode().IsRegular() || uint32(info.Mode().Perm())!=f.Mode {return false,fmt.Errorf("existing destination has incompatible type or mode: %s",f.Path)}
	st,ok:=info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid!=uint32(os.Geteuid()) || st.Nlink!=1 {return false,fmt.Errorf("existing destination ownership or links are unsafe: %s",f.Path)}
	file,err:=os.OpenFile(path,os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK,0)
	if err!=nil{return false,err};defer file.Close()
	after,err:=file.Stat()
	if err!=nil || !os.SameFile(info,after){return false,fmt.Errorf("destination changed while opening")}
	raw,err:=io.ReadAll(io.LimitReader(file,int64(len(f.Data))+1))
	if err!=nil||!bytes.Equal(raw,f.Data){return false,fmt.Errorf("existing destination differs; repair is required: %s",f.Path)}
	return true,nil
}
func writeAgentFile(path string,f agentPayload)error {
	if err:=agentDirectory(filepath.Dir(path),true);err!=nil{return err}
	file,err:=os.CreateTemp(filepath.Dir(path),".agent-install-")
	if err!=nil{return err};defer os.Remove(file.Name())
	if _,err=file.Write(f.Data);err!=nil{file.Close();return err}
	if err=file.Chmod(os.FileMode(f.Mode));err!=nil{file.Close();return err}
	if err=file.Sync();err!=nil{file.Close();return err}
	if err=file.Close();err!=nil{return err}
	// A hard link is atomic and refuses an existing destination.
	if err=os.Link(file.Name(),path);err!=nil{return fmt.Errorf("destination appeared during installation")}
	if err=os.Remove(file.Name());err!=nil{return err}
	dir,err:=os.Open(filepath.Dir(path));if err!=nil{return err};defer dir.Close()
	return dir.Sync()
}
func checkAgentConflicts(root string,files []agentPayload,resume bool)error {
	for _,path:=range []string{
		"/var/lib/hypermesh/device.json","/var/lib/hypermesh/activation-approved",
		"/etc/systemd/system/hypermesh-agent.service.d","/etc/systemd/system/hypermesh-wg.service.d",
		"/run/systemd/system/hypermesh-agent.service","/run/systemd/system/hypermesh-wg.service",
		"/run/systemd/system/hypermesh-agent.service.d","/run/systemd/system/hypermesh-wg.service.d",
		"/usr/lib/systemd/system/hypermesh-agent.service","/usr/lib/systemd/system/hypermesh-wg.service",
		"/lib/systemd/system/hypermesh-agent.service","/lib/systemd/system/hypermesh-wg.service",
	} {
		if _,err:=os.Lstat(agentDest(root,path));!os.IsNotExist(err){return fmt.Errorf("existing host or service configuration requires repair, not first install: %s",path)}
	}
	usesWG:=false
	for _,f:=range files {
		if strings.HasSuffix(f.Path,"/hypermesh-wg.service"){usesWG=true}
		path:=agentDest(root,f.Path)
		// Check ancestors that exist without creating anything.
		parent:=filepath.Dir(path)
		for {if _,err:=os.Lstat(parent);!os.IsNotExist(err){break};parent=filepath.Dir(parent)}
		if err:=agentDirectory(parent,false);err!=nil{return err}
		exists,err:=agentFileMatches(path,f)
		if err!=nil{return err}
		if exists&&!resume{return fmt.Errorf("existing installation requires repair, not first install: %s",f.Path)}
	}
	if !usesWG {if _,err:=os.Lstat(agentDest(root,"/etc/systemd/system/hypermesh-wg.service"));!os.IsNotExist(err){return fmt.Errorf("kernel-mode install refuses an existing userspace unit")}}
	return nil
}
func installAgentFiles(ctx context.Context,root string,files []agentPayload,resume bool,j *agentInstallJournal,save func(agentInstallJournal)error)error {
	if err:=checkAgentConflicts(root,files,resume);err!=nil{return err}
	j.State="installing";j.Error=""
	if err:=save(*j);err!=nil{return err}
	for _,f:=range files {
		if err:=ctx.Err();err!=nil{return err}
		path:=agentDest(root,f.Path)
		exists,err:=agentFileMatches(path,f)
		if err!=nil{return err}
		if !exists {if err=writeAgentFile(path,f);err!=nil{return err}}
		found:=false;for _,p:=range j.Completed {found=found||p==f.Path}
		if !found {j.Completed=append(j.Completed,f.Path)}
		if err=save(*j);err!=nil{return err}
	}
	j.State="installed-awaiting-enrollment"
	return save(*j)
}
func saveAgentJournal(path string,j agentInstallJournal)error {
	raw,err:=json.Marshal(j);if err!=nil{return err}
	f,err:=os.CreateTemp(filepath.Dir(path),".agent-journal-");if err!=nil{return err}
	defer os.Remove(f.Name())
	if _,err=f.Write(raw);err!=nil{f.Close();return err}
	if err=f.Sync();err!=nil{f.Close();return err}
	if err=f.Close();err!=nil{return err}
	if err=os.Rename(f.Name(),path);err!=nil{return err}
	d,err:=os.Open(filepath.Dir(path));if err!=nil{return err};defer d.Close()
	return d.Sync()
}
func RunInstallAgent(ctx context.Context,args []string,out,diagnostic io.Writer)int {
	flags:=flag.NewFlagSet("install-agent",flag.ContinueOnError);flags.SetOutput(diagnostic)
	path:=flags.String("plan","","reviewed agent plan")
	approved:=flags.String("approve-plan","","exact agent plan_sha256")
	if err:=flags.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
	if flags.NArg()!=0||*path==""||!setupDigestRE.MatchString(*approved){fmt.Fprintln(diagnostic,"require --plan and --approve-plan");return 2}
	if os.Geteuid()!=0{fmt.Fprintln(diagnostic,"install-agent requires root");return 1}
	var p agentInstallPlan
	raw,err:=readSetupRegular(*path,false)
	if err==nil{err=decodeSetupJSON(raw,&p)}
	if err==nil && (p.SHA256!=*approved||agentPlanDigest(p)!=*approved){err=fmt.Errorf("agent approval digest does not match")}
	if err==nil{err=validateAgentInputs(p)}
	if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
	unlock,err:=lockSetupState(defaultSetupState)
	if err!=nil{fmt.Fprintln(diagnostic,err);return 1};defer unlock()
	ctx,cancel:=context.WithTimeout(ctx,10*time.Minute);defer cancel()
	deps:=setupApplyDeps{Source:setupSource{},Preflight:freshSetupPreflight}
	remaining,target,err:=validateSetupHost(ctx,p.Setup,deps)
	if err==nil && (len(remaining)>0 || (p.Setup.HeadlessRequested&&target!="multi-user.target")){err=fmt.Errorf("approved prerequisites are incomplete")}
	if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
	meta,binary,err:=fetchAgentPayload(ctx,p)
	if err==nil {remaining,target,err=validateSetupHost(ctx,p.Setup,deps)}
	if err==nil && (len(remaining)>0 || (p.Setup.HeadlessRequested&&target!="multi-user.target")) {err=fmt.Errorf("prerequisites changed during download")}
	if err==nil && (!sameSetupJSON(meta,p.Release)||!sameSetupJSON(agentFileManifest(agentPayloads(p,binary)),p.Files)){err=fmt.Errorf("verified payloads differ from the approved plan")}
	if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
	journalPath:=filepath.Join(defaultSetupState,"agent-"+p.SHA256+".json")
	j:=agentInstallJournal{Schema:1,PlanSHA256:p.SHA256,State:"pending"}
	resume:=false
	if _,statErr:=os.Lstat(journalPath);statErr==nil {
		raw,err=readSetupRegular(journalPath,true)
		if err==nil{err=decodeSetupJSON(raw,&j)}
		if err==nil&&(j.Schema!=1||j.PlanSHA256!=p.SHA256){err=fmt.Errorf("agent journal does not match")}
		if err!=nil{fmt.Fprintln(diagnostic,"invalid agent journal; preserve it for recovery");return 1}
		resume=true
	} else if !os.IsNotExist(statErr){fmt.Fprintln(diagnostic,"cannot inspect agent journal");return 1}
	save:=func(value agentInstallJournal)error{return saveAgentJournal(journalPath,value)}
	err=secureSetupDirectory("/var/lib/hypermesh",false)
	if err==nil {err=installAgentFiles(ctx,"",agentPayloads(p,binary),resume,&j,save)}
	if err!=nil {j.State="failed";j.Error=err.Error();if saveErr:=save(j);saveErr!=nil{fmt.Fprintln(diagnostic,"cannot persist failure journal")}}
	if encodeErr:=json.NewEncoder(out).Encode(j);encodeErr!=nil{return 1}
	if err!=nil{fmt.Fprintln(diagnostic,err);return 1}
	return 0
}

func RunAgentInstallStatus(_ context.Context,args []string,out,diagnostic io.Writer)int {
	flags:=flag.NewFlagSet("agent-install-status",flag.ContinueOnError);flags.SetOutput(diagnostic)
	digest:=flags.String("plan-sha","","agent plan_sha256")
	if err:=flags.Parse(args);err!=nil{if err==flag.ErrHelp{return 0};return 2}
	if flags.NArg()!=0||!setupDigestRE.MatchString(*digest){fmt.Fprintln(diagnostic,"require --plan-sha SHA256");return 2}
	if err:=secureSetupDirectory(defaultSetupState,false);err!=nil{fmt.Fprintln(diagnostic,err);return 1}
	raw,err:=readSetupRegular(filepath.Join(defaultSetupState,"agent-"+*digest+".json"),true)
	var j agentInstallJournal
	if err==nil{err=decodeSetupJSON(raw,&j)}
	if err!=nil||j.Schema!=1||j.PlanSHA256!=*digest{fmt.Fprintln(diagnostic,"agent installation journal unavailable or invalid");return 1}
	if err=json.NewEncoder(out).Encode(j);err!=nil{return 1}
	return 0
}
