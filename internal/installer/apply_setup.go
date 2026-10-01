package installer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/FyberLabs/hypermesh-host/internal/buildinfo"
)

func digestSetupBytes(raw []byte) string {
	sum:=sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

var setupDigestRE = regexp.MustCompile(`^[a-f0-9]{64}$`)

type setupJournal struct {
	Schema int `json:"schema"`
	PlanSHA256 string `json:"plan_sha256"`
	Stage string `json:"stage"`
	State string `json:"state"`
	OriginalTarget string `json:"original_target"`
	Error string `json:"error,omitempty"`
}

type setupApplyDeps struct {
	Source setupSource
	Preflight func(context.Context, SetupPlan) (Plan, error)
	Run func(context.Context, []string) error
	Save func(setupJournal) error
}

func freshSetupPreflight(ctx context.Context, p SetupPlan) (Plan, error) {
	var out, diagnostic bytes.Buffer
	r := p.Preflight.Resources
	args := []string{"--memory-mode", r.MemoryMode, "--model-dir", r.ModelDirectory,
		"--uma-gib", strconv.FormatInt(r.Resources.UMAAllocationBytes/GiB,10),
		"--aiuditor-check-url", p.ReadinessURL}
	if RunPlan(ctx,args,&out,&diagnostic) != 0 { return Plan{}, fmt.Errorf("fresh preflight failed; rerun plan-setup to inspect blockers") }
	var fresh Plan
	if json.Unmarshal(out.Bytes(),&fresh) != nil { return Plan{}, fmt.Errorf("cannot read fresh preflight") }
	return fresh,nil
}

func sameSetupJSON(a,b any) bool {
	x,_:=json.Marshal(a)
	y,_:=json.Marshal(b)
	return bytes.Equal(x,y)
}

func validateSetupApproval(p SetupPlan, approved string) error {
	if p.SchemaVersion!=2 || !setupDigestRE.MatchString(approved) || approved!=p.PlanSHA256 || setupPlanDigest(p)!=approved {
		return fmt.Errorf("approval does not match a valid schema-2 plan; regenerate and review the plan")
	}
	if !p.Reviewable || p.InstallAuthorized || len(p.Blockers)!=0 || !p.Preflight.PreliminaryChecksPassed || p.Profile==nil {
		return fmt.Errorf("only a reviewable prerequisite plan can be approved")
	}
	if p.InstallerCommit!=buildinfo.Current() || p.InstallerVersion!=buildinfo.CurrentVersion() || p.InstallerBuild!=buildinfo.CurrentBuild() {
		return fmt.Errorf("installer build changed; regenerate the plan with this installer")
	}
	if validateEndpoint(p.ReadinessURL)!=nil || p.Preflight.Resources.ModelDirectory=="" ||
		(p.Preflight.Resources.MemoryMode!="uma" && p.Preflight.Resources.MemoryMode!="discrete") {
		return fmt.Errorf("plan lacks valid preflight inputs; regenerate it")
	}
	if p.WireGuardMode!="kernel" && p.WireGuardMode!="userspace" { return fmt.Errorf("invalid WireGuard mode") }
	profile,err:=selectSetupProfile(p.Preflight,p.Platform,p.Profile.ID)
	if err!=nil { return err }
	if p.WireGuardMode=="userspace" {
		profile.RequiredPackages=append(profile.RequiredPackages,profile.UserspacePackages...)
		sort.Strings(profile.RequiredPackages)
	}
	if !sameSetupJSON(profile,p.Profile) { return fmt.Errorf("profile revision or policy changed; regenerate the plan") }
	if !setupDigestRE.MatchString(p.Platform.PackageStateSHA256) { return fmt.Errorf("invalid package-state binding") }
	seen:=map[string]bool{}
	for i,pkg:=range p.Packages {
		base:=packageBase(pkg.Name)
		if _,arch,qualified:=strings.Cut(pkg.Name,":"); qualified && arch!=normalizedSetupArch(p.Preflight.Hardware.Facts.Arch) { return fmt.Errorf("foreign package architecture is outside this plan") }
		if !setupPackageRE.MatchString(pkg.Name) || !setupVersionRE.MatchString(pkg.To) || pkg.From!="" ||
			protectedSetupPackage(pkg.Name) || seen[base] || (i>0 && p.Packages[i-1].Name>=pkg.Name) {
			return fmt.Errorf("plan contains an invalid, duplicate or protected package change")
		}
		seen[base]=true
	}
	// Never execute argv copied from JSON, even when its hash matches.
	expected:=[]SetupChange{}
	if len(p.Packages)>0 { expected=append(expected,SetupChange{ID:"prerequisite-packages",Kind:"packages",Required:true,Argv:setupInstallArgv(p.Packages)}) }
	if p.HeadlessRequested && p.Platform.DefaultTarget=="graphical.target" {
		expected=append(expected,SetupChange{ID:"headless-next-boot",Kind:"boot-target",Argv:[]string{"systemctl","set-default","multi-user.target"}})
	}
	if p.Platform.DefaultTarget!="graphical.target" && p.Platform.DefaultTarget!="multi-user.target" { return fmt.Errorf("invalid original boot target") }
	if len(expected)!=len(p.Changes) { return fmt.Errorf("unsupported plan changes") }
	for i,change:=range p.Changes {
		change.Reason=""
		if !sameSetupJSON(change,expected[i]) { return fmt.Errorf("plan action does not match the allowed prerequisite operation") }
	}
	return nil
}

func readSetupInventory(ctx context.Context, source setupSource) (map[string]string,error) {
	raw,err:=source.output(ctx,"dpkg-query","-W","-f=${binary:Package}\t${Version}\t${db:Status-Abbrev}\n")
	if err!=nil {return nil,fmt.Errorf("cannot read package state")}
	return parseInstalledPackages(string(raw))
}

// Reconcile exactly the approved additions, including successful installs
// whose completion was not journaled before an interruption. Other drift fails.
func remainingSetupPackages(p SetupPlan, installed map[string]string) ([]PackageChange,error) {
	baseline:=map[string]string{}
	for k,v:=range installed {baseline[k]=v}
	remaining:=[]PackageChange{}
	arch:=normalizedSetupArch(p.Preflight.Hardware.Facts.Arch)
	for _,pkg:=range p.Packages {
		found:=""
		for name,version:=range installed {
			if packageBase(name)!=packageBase(pkg.Name) {continue}
			_,qualified,hasArch:=strings.Cut(name,":")
			if hasArch && qualified!=arch {continue}
			if found!="" || version!=pkg.To {return nil,fmt.Errorf("approved package has a different version or ambiguous architecture; review dpkg state")}
			found=name
		}
		if found=="" {remaining=append(remaining,pkg)} else {delete(baseline,found)}
	}
	raw,_:=json.Marshal(baseline)
	if digestSetupBytes(raw)!=p.Platform.PackageStateSHA256 {
		return nil,fmt.Errorf("unapproved package-state drift; preserve the journal and regenerate a reviewed plan")
	}
	return remaining,nil
}

func validateSetupHost(ctx context.Context,p SetupPlan,deps setupApplyDeps) ([]PackageChange,string,error) {
	fresh,err:=deps.Preflight(ctx,p)
	if err!=nil {return nil,"",err}
	if !fresh.PreliminaryChecksPassed || len(fresh.Blockers)!=0 || !fresh.Resources.Admission.Eligible ||
		fresh.Hardware.Fingerprint!=p.Preflight.Hardware.Fingerprint || fresh.Hardware.Backend!=p.Preflight.Hardware.Backend ||
		fresh.Resources.MemoryMode!=p.Preflight.Resources.MemoryMode ||
		fresh.Resources.ModelDirectory!=p.Preflight.Resources.ModelDirectory ||
		fresh.Resources.Resources.UMAAllocationBytes!=p.Preflight.Resources.Resources.UMAAllocationBytes {
		return nil,"",fmt.Errorf("resource or hardware preflight changed or failed; regenerate the plan")
	}
	if _,err:=selectSetupProfile(fresh,p.Platform,p.Profile.ID);err!=nil {
		return nil,"",fmt.Errorf("fresh hardware no longer matches the approved profile")
	}
	if p.Preflight.Resources.Resources.UMA {
		old,now:=p.Preflight.Resources.Resources.UMARuntime,fresh.Resources.Resources.UMARuntime
		if !validUMARuntime(old) || !validUMARuntime(now) || old.Backend!=now.Backend || old.Device!=now.Device || old.TotalBytes!=now.TotalBytes {
			return nil,"",fmt.Errorf("UMA runtime device changed; regenerate the plan")
		}
	}
	raw,err:=deps.Source.read("/etc/os-release")
	if err!=nil {return nil,"",fmt.Errorf("cannot recheck OS")}
	id,version,err:=parseSetupOS(raw)
	if err!=nil || id!=p.Platform.OS || version!=p.Platform.Version {return nil,"",fmt.Errorf("OS changed; regenerate the plan")}
	raw,err=deps.Source.read("/proc/sys/kernel/osrelease")
	if err!=nil || strings.TrimSpace(string(raw))!=p.Platform.Kernel {return nil,"",fmt.Errorf("kernel changed; regenerate the plan")}
	raw,err=deps.Source.output(ctx,"systemctl","get-default")
	if err!=nil {return nil,"",fmt.Errorf("cannot recheck boot target")}
	target:=strings.TrimSpace(string(raw))
	if target!=p.Platform.DefaultTarget && !(p.HeadlessRequested && target=="multi-user.target") {
		return nil,"",fmt.Errorf("boot target changed outside the approved plan")
	}
	installed,err:=readSetupInventory(ctx,deps.Source)
	if err!=nil {return nil,"",err}
	remaining,err:=remainingSetupPackages(p,installed)
	if err!=nil {return nil,"",err}
	if len(remaining)>0 {
		args:=[]string{"--simulate","--no-install-recommends","--no-remove","install"}
		for _,pkg:=range remaining {args=append(args,pkg.Name+"="+pkg.To)}
		raw,err=deps.Source.output(ctx,"apt-get",args...)
		if err!=nil {return nil,"",fmt.Errorf("approved package versions cannot be resolved; regenerate the plan")}
		resolved,err:=parseSetupSimulation(string(raw),installed)
		if err!=nil || !sameSetupJSON(resolved,remaining) {
			return nil,"",fmt.Errorf("APT dependency transaction differs from approval; regenerate the plan")
		}
	}
	if ctx.Err()!=nil {return nil,"",fmt.Errorf("setup interrupted before mutation")}
	return remaining,target,nil
}

func applySetup(ctx context.Context,p SetupPlan,approved string,j *setupJournal,deps setupApplyDeps) error {
	if err:=validateSetupApproval(p,approved);err!=nil {return err}
	if j.Schema!=1 || j.PlanSHA256!=approved || j.OriginalTarget!=p.Platform.DefaultTarget {
		return fmt.Errorf("journal does not match the approved plan")
	}
	save:=func(stage,state,message string)error {
		j.Stage,j.State,j.Error=stage,state,message
		return deps.Save(*j)
	}
	fail:=func(err error)error {
		if saveErr:=save(j.Stage,"failed",err.Error());saveErr!=nil {return fmt.Errorf("%v; cannot persist failure journal",err)}
		return err
	}
	if err:=save("revalidate","running","");err!=nil {return err}
	remaining,target,err:=validateSetupHost(ctx,p,deps)
	if err!=nil {return fail(err)}
	if len(remaining)>0 {
		if err:=save("packages","running","");err!=nil {return err}
		if err:=deps.Run(ctx,setupInstallArgv(remaining));err!=nil {
			return fail(fmt.Errorf("package installation failed or was interrupted; inspect /var/log/apt and dpkg state, then rerun the same approved plan"))
		}
		installed,err:=readSetupInventory(ctx,deps.Source)
		if err!=nil {return fail(err)}
		left,err:=remainingSetupPackages(p,installed)
		if err!=nil {return fail(err)}
		if len(left)!=0 {return fail(fmt.Errorf("package command returned without all approved packages installed"))}
	}
	if err:=save("packages","complete","");err!=nil {return err}
	if p.HeadlessRequested && target!="multi-user.target" {
		// Recheck all invariants before the next mutation, including current
		// memory pressure and package changes introduced by maintainer scripts.
		_,target,err=validateSetupHost(ctx,p,deps)
		if err!=nil {return fail(err)}
		if target!="multi-user.target" {
			if err:=save("headless","running","");err!=nil {return err}
			if err:=deps.Run(ctx,[]string{"systemctl","set-default","multi-user.target"});err!=nil {
				return fail(fmt.Errorf("headless target update failed; rerun the same plan after checking systemd"))
			}
			raw,err:=deps.Source.output(ctx,"systemctl","get-default")
			if err!=nil || strings.TrimSpace(string(raw))!="multi-user.target" {return fail(fmt.Errorf("headless target verification failed"))}
		}
	}
	return save("prerequisites","complete","")
}

// Reject unknown fields, trailing values and duplicate keys in reviewed JSON.
// Duplicate-key rejection prevents different reviewers interpreting one file
// differently before its canonical digest is approved.
func decodeSetupJSON(raw []byte,value any)error {
	if len(raw)>2<<20 {return fmt.Errorf("setup JSON exceeds 2 MiB")}
	scan:=json.NewDecoder(bytes.NewReader(raw))
	scan.UseNumber()
	var walk func()error
	walk=func()error {
		token,err:=scan.Token();if err!=nil{return err}
		delim,ok:=token.(json.Delim);if !ok{return nil}
		switch delim {
		case '{':
			seen:=map[string]bool{}
			for scan.More() {
				key,err:=scan.Token();if err!=nil{return err}
				name,ok:=key.(string);name=strings.ToLower(name);if !ok || seen[name]{return fmt.Errorf("duplicate or invalid JSON key")}
				seen[name]=true
				if err:=walk();err!=nil{return err}
			}
		case '[':
			for scan.More(){if err:=walk();err!=nil{return err}}
		default:return fmt.Errorf("invalid JSON delimiter")
		}
		_,err=scan.Token();return err
	}
	if err:=walk();err!=nil{return fmt.Errorf("invalid setup JSON")}
	if _,err:=scan.Token();err!=io.EOF{return fmt.Errorf("trailing setup JSON")}
	decoder:=json.NewDecoder(bytes.NewReader(raw));decoder.DisallowUnknownFields()
	if err:=decoder.Decode(value);err!=nil{return fmt.Errorf("unrecognized setup JSON schema")}
	return nil
}
