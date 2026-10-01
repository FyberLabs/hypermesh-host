//go:build linux

package installer

import (
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

const defaultSetupState = "/var/lib/hypermesh/installer"

func RunApplySetup(ctx context.Context,args []string,out,diagnostic io.Writer)int {
	flags:=flag.NewFlagSet("apply-setup",flag.ContinueOnError)
	flags.SetOutput(diagnostic)
	planPath:=flags.String("plan","","reviewed schema-2 plan JSON")
	approved:=flags.String("approve-plan","","exact reviewed plan_sha256; approves only its prerequisite changes")
	state:=flags.String("state-dir",defaultSetupState,"private root-owned journal directory")
	if err:=flags.Parse(args);err!=nil {if err==flag.ErrHelp{return 0};return 2}
	if flags.NArg()!=0 || *planPath=="" || !setupDigestRE.MatchString(*approved) {fmt.Fprintln(diagnostic,"require --plan FILE and --approve-plan SHA256");return 2}
	if os.Geteuid()!=0 {fmt.Fprintln(diagnostic,"apply-setup requires root to install packages and update systemd");return 1}
	var p SetupPlan
	raw,err:=readSetupRegular(*planPath,false)
	if err==nil {err=decodeSetupJSON(raw,&p)}
	if err==nil {err=validateSetupApproval(p,*approved)}
	if err!=nil {fmt.Fprintln(diagnostic,err);return 1}
	unlock,err:=lockSetupState(*state)
	if err!=nil {fmt.Fprintln(diagnostic,err);return 1}
	defer unlock()
	path:=filepath.Join(*state,*approved+".json")
	j:=setupJournal{Schema:1,PlanSHA256:*approved,Stage:"pending",State:"pending",OriginalTarget:p.Platform.DefaultTarget}
	if _,statErr:=os.Lstat(path);statErr==nil {
		raw,err=readSetupRegular(path,true)
		if err==nil {err=decodeSetupJSON(raw,&j)}
		if err==nil {err=validateSetupJournal(j)}
		if err!=nil {fmt.Fprintln(diagnostic,"invalid existing setup journal; preserve it for recovery");return 1}
	} else if !os.IsNotExist(statErr) {fmt.Fprintln(diagnostic,"cannot inspect setup journal");return 1}
	deps:=setupApplyDeps{Source:setupSource{},Preflight:freshSetupPreflight,Run:runSetupMutation,
		Save:func(value setupJournal)error{return writeSetupJournal(path,value)}}
	err=applySetup(ctx,p,*approved,&j,deps)
	encodeErr:=json.NewEncoder(out).Encode(j)
	if err!=nil {fmt.Fprintln(diagnostic,err);return 1}
	if encodeErr!=nil {fmt.Fprintln(diagnostic,"cannot write setup status");return 1}
	return 0
}

func RunSetupStatus(_ context.Context,args []string,out,diagnostic io.Writer)int {
	flags:=flag.NewFlagSet("setup-status",flag.ContinueOnError)
	flags.SetOutput(diagnostic)
	digest:=flags.String("plan-sha","","plan_sha256 of the application to inspect")
	state:=flags.String("state-dir",defaultSetupState,"private root-owned journal directory")
	if err:=flags.Parse(args);err!=nil {if err==flag.ErrHelp{return 0};return 2}
	if flags.NArg()!=0 || !setupDigestRE.MatchString(*digest) {fmt.Fprintln(diagnostic,"require --plan-sha SHA256");return 2}
	if err:=secureSetupDirectory(*state,false);err!=nil {fmt.Fprintln(diagnostic,err);return 1}
	raw,err:=readSetupRegular(filepath.Join(*state,*digest+".json"),true)
	var j setupJournal
	if err==nil {err=decodeSetupJSON(raw,&j)}
	if err==nil {err=validateSetupJournal(j)}
	if err!=nil || j.PlanSHA256!=*digest {fmt.Fprintln(diagnostic,"setup journal unavailable or invalid");return 1}
	if err:=json.NewEncoder(out).Encode(j);err!=nil {return 1}
	return 0
}

func validateSetupJournal(j setupJournal)error {
	if j.Schema!=1 || !setupDigestRE.MatchString(j.PlanSHA256) ||
		(j.OriginalTarget!="graphical.target" && j.OriginalTarget!="multi-user.target") {return fmt.Errorf("invalid journal")}
	switch j.Stage {case "pending","revalidate","packages","headless","prerequisites":default:return fmt.Errorf("invalid journal stage")}
	switch j.State {case "pending","running","complete","failed":default:return fmt.Errorf("invalid journal state")}
	return nil
}

func runSetupMutation(ctx context.Context,args []string)error {
	if len(args)<2 || (args[0]!="apt-get" && args[0]!="systemctl") {return fmt.Errorf("unsupported setup command")}
	ctx,cancel:=context.WithTimeout(ctx,20*time.Minute)
	defer cancel()
	cmd:=exec.CommandContext(ctx,"/usr/bin/"+args[0],args[1:]...)
	// Do not inherit shell hooks, language settings or frontend overrides.
	// Root's configured APT sources/auth files remain authoritative.
	cmd.Env=[]string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin","HOME=/root","LC_ALL=C",
		"DEBIAN_FRONTEND=noninteractive","APT_LISTCHANGES_FRONTEND=none"}
	cmd.Stdout=io.Discard
	cmd.Stderr=io.Discard
	cmd.SysProcAttr=&syscall.SysProcAttr{Setpgid:true}
	cmd.Cancel=func()error {return syscall.Kill(-cmd.Process.Pid,syscall.SIGKILL)}
	cmd.WaitDelay=5*time.Second
	if err:=cmd.Run();err!=nil {return fmt.Errorf("setup command failed; child output is available only in system package logs")}
	return nil
}

// Check each path component before using it. Sticky, root-owned /tmp is
// permitted, but the final state directory must be private and owner-controlled.
func secureSetupDirectory(path string,create bool)error {
	if !filepath.IsAbs(path) || filepath.Clean(path)!=path || path=="/" {return fmt.Errorf("state directory must be a clean absolute path")}
	current:="/"
	for _,part:=range strings.Split(strings.TrimPrefix(path,"/"),"/") {
		current=filepath.Join(current,part)
		info,err:=os.Lstat(current)
		if os.IsNotExist(err) && create {
			if err=os.Mkdir(current,0o700);err!=nil && !os.IsExist(err) {return fmt.Errorf("cannot create private setup state directory")}
			info,err=os.Lstat(current)
		}
		if err!=nil || !info.IsDir() || info.Mode()&os.ModeSymlink!=0 {return fmt.Errorf("state path must contain real directories only")}
		stat,ok:=info.Sys().(*syscall.Stat_t)
		if !ok || (stat.Uid!=0 && stat.Uid!=uint32(os.Geteuid())) {return fmt.Errorf("state path is not owner-controlled")}
		stickyRoot:=stat.Uid==0 && info.Mode()&os.ModeSticky!=0 && current!=path
		if info.Mode().Perm()&0o022!=0 && !stickyRoot {return fmt.Errorf("state path is writable by other users")}
		if current==path && (stat.Uid!=uint32(os.Geteuid()) || info.Mode().Perm()!=0o700) {return fmt.Errorf("state directory must be owned by the current user with mode 0700")}
	}
	return nil
}

func lockSetupState(dir string)(func(),error) {
	if err:=secureSetupDirectory(dir,true);err!=nil {return nil,err}
	file,err:=os.OpenFile(filepath.Join(dir,"apply.lock"),os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW,0o600)
	if err!=nil {return nil,fmt.Errorf("cannot open setup lock")}
	info,err:=file.Stat()
	if err!=nil || !privateSetupFile(info) {file.Close();return nil,fmt.Errorf("invalid setup lock permissions or ownership")}
	if err:=syscall.Flock(int(file.Fd()),syscall.LOCK_EX|syscall.LOCK_NB);err!=nil {
		file.Close();return nil,fmt.Errorf("another setup application is running")
	}
	return func(){_ = syscall.Flock(int(file.Fd()),syscall.LOCK_UN);_ = file.Close()},nil
}

func privateSetupFile(info os.FileInfo)bool {
	if info==nil || !info.Mode().IsRegular() || info.Mode().Perm()!=0o600 {return false}
	stat,ok:=info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid==uint32(os.Geteuid()) && stat.Nlink==1
}

func readSetupRegular(path string,private bool)([]byte,error) {
	before,err:=os.Lstat(path)
	if err!=nil || !before.Mode().IsRegular() || (private && !privateSetupFile(before)) {return nil,fmt.Errorf("setup input must be a readable regular file with appropriate permissions")}
	file,err:=os.OpenFile(path,os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK,0)
	if err!=nil {return nil,fmt.Errorf("cannot open setup input")}
	defer file.Close()
	after,err:=file.Stat()
	if err!=nil || !os.SameFile(before,after) {return nil,fmt.Errorf("setup input changed while opening")}
	raw,err:=io.ReadAll(io.LimitReader(file,(2<<20)+1))
	if err!=nil || len(raw)>2<<20 {return nil,fmt.Errorf("cannot read setup input within the size limit")}
	return raw,nil
}

func writeSetupJournal(path string,j setupJournal)error {
	if err:=validateSetupJournal(j);err!=nil {return err}
	raw,err:=json.Marshal(j);if err!=nil{return err}
	file,err:=os.CreateTemp(filepath.Dir(path),".setup-journal-")
	if err!=nil {return fmt.Errorf("cannot stage setup journal")}
	temp:=file.Name()
	defer os.Remove(temp)
	if _,err=file.Write(raw);err!=nil {file.Close();return fmt.Errorf("cannot write setup journal")}
	if err=file.Sync();err!=nil {file.Close();return fmt.Errorf("cannot sync setup journal")}
	if err=file.Close();err!=nil {return fmt.Errorf("cannot close setup journal")}
	if err=os.Rename(temp,path);err!=nil {return fmt.Errorf("cannot publish setup journal")}
	dir,err:=os.Open(filepath.Dir(path));if err!=nil{return fmt.Errorf("cannot open journal directory")}
	defer dir.Close()
	if err=dir.Sync();err!=nil {return fmt.Errorf("cannot sync journal directory")}
	return nil
}
