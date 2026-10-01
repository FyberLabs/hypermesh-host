package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/FyberLabs/hypermesh-host/internal/hardware"
)

func setupFixture() (Plan, setupSource) {
	r := Resources{SystemBytes: 64*GiB, AvailableSystemBytes: 48*GiB,
		UMA: true, UMAAllocationBytes: 40*GiB, DiskFreeBytes: 100*GiB,
		UMARuntime: &UMARuntime{Backend: "cuda", FreeBytes: 48*GiB, TotalBytes: 64*GiB}}
	p := Plan{SchemaVersion: 1, PreliminaryChecksPassed: true, EndpointReachable: true,
		Resources: ResourceReport{Resources:r, Admission:CheckResources(r)},
		Hardware: hardware.Recommend(hardware.Facts{Arch:"arm64", Tegra:true, CUDA:true})}
	source := setupSource{
		ReadFile: func(path string) ([]byte,error) {
			switch path {
			case "/etc/os-release": return []byte("ID=ubuntu\nVERSION_ID=\"24.04\"\n"), nil
			case "/proc/sys/kernel/osrelease": return []byte("6.8.1-tegra\n"), nil
			}
			return nil, fmt.Errorf("unexpected read")
		},
		Output: func(ctx context.Context, name string, args ...string) ([]byte,error) {
			switch name {
			case "systemctl":
				if strings.Join(args," ") != "get-default" { return nil, fmt.Errorf("mutation attempted") }
				return []byte("graphical.target\n"), nil
			case "dpkg-query":
				return []byte("ca-certificates\t1.0\tii \ngnupg\t2.0\tii \npython3\t3.12\tii \nwireguard-go\t1.0\tii \nwireguard-tools\t1.0\tii \ndocker.io\t26.0\tii \nnvidia-l4t-core\t36.4.0\tii \n"), nil
			}
			return nil, fmt.Errorf("unexpected command %s",name)
		},
	}
	return p, source
}

func TestSetupPlanPinsStateAndHeadlessIntent(t *testing.T) {
	p, source := setupFixture()
	a := buildSetupPlan(context.Background(), p, "auto", "kernel", true, source)
	if !a.Reviewable || a.InstallAuthorized || a.Profile == nil || a.Profile.Revision != 2 {
		t.Fatalf("%+v",a)
	}
	if len(a.Changes) != 1 || a.Changes[0].ID != "headless-next-boot" || a.Changes[0].Required {
		t.Fatalf("wrong headless proposal: %+v",a.Changes)
	}
	if a.PlanSHA256 == "" || a.Platform.PackageStateSHA256 == "" || len(a.Platform.ProtectedPackages)==0 {
		t.Fatal("missing state binding")
	}
	b := buildSetupPlan(context.Background(), p, "auto", "kernel", true, source)
	if a.PlanSHA256 != b.PlanSHA256 { t.Fatal("identical plans differ") }
	b = buildSetupPlan(context.Background(), p, "auto", "kernel", false, source)
	if a.PlanSHA256 == b.PlanSHA256 || len(b.Changes) != 0 { t.Fatal("headless intent not bound") }
	oldRead := source.ReadFile
	source.ReadFile = func(path string)([]byte,error) {
		if path=="/proc/sys/kernel/osrelease" { return []byte("6.8.2-tegra"),nil }
		return oldRead(path)
	}
	b = buildSetupPlan(context.Background(), p, "auto", "kernel", true, source)
	if a.PlanSHA256 == b.PlanSHA256 { t.Fatal("kernel drift not bound") }
}

func TestSetupProfileSelection(t *testing.T) {
	p,_ := setupFixture()
	platform := SetupPlatform{OS:"ubuntu",Version:"24.04"}
	if _,err:=selectSetupProfile(p,platform,"ubuntu-amd-hip");err==nil {t.Fatal("operator override bypassed facts")}
	p.Hardware.Facts.Model="MINISFORUM N5 MAX"
	if got,err:=selectSetupProfile(p,platform,"auto");err!=nil || got.ID!="ubuntu-jetson-cuda" {t.Fatal("marketing name changed measured profile")}
	p.Hardware=hardware.Recommend(hardware.Facts{Arch:"amd64",HIP:true})
	p.Resources.Resources.UMARuntime.Backend="hip"
	if got,err:=selectSetupProfile(p,platform,"auto");err!=nil || got.ID!="ubuntu-amd-hip" {t.Fatal("HIP profile",err)}
	p.Resources.Resources.UMARuntime.Backend="cuda"
	if _,err:=selectSetupProfile(p,platform,"auto");err==nil {t.Fatal("conflicting runtime accepted")}
	p.Hardware=hardware.Recommend(hardware.Facts{Arch:"amd64",CUDA:true})
	p.Resources.Resources.UMA=false
	if got,err:=selectSetupProfile(p,platform,"auto");err!=nil || got.ID!="ubuntu-discrete-cuda" {t.Fatal("discrete profile",err)}
	platform.OS="debian"
	if _,err:=selectSetupProfile(p,platform,"auto");err==nil {t.Fatal("unsupported OS accepted")}
}

func TestSetupPlanBlockedCases(t *testing.T) {
	for _,kind:=range []string{"preflight","unknown-os","missing-kernel","bad-target","broken-dpkg","missing-vendor","missing-docker","cancel"} {
		t.Run(kind,func(t *testing.T) {
			p,s:=setupFixture()
			read,output:=s.ReadFile,s.Output
			s.ReadFile=func(path string)([]byte,error) {
				if kind=="unknown-os" && path=="/etc/os-release" {return []byte("ID=unknown\nVERSION_ID=1"),nil}
				if kind=="missing-kernel" && path=="/proc/sys/kernel/osrelease" {return nil,fmt.Errorf("missing")}
				return read(path)
			}
			s.Output=func(ctx context.Context,name string,args ...string)([]byte,error) {
				if kind=="bad-target" && name=="systemctl" {return []byte("rescue.target"),nil}
				raw,err:=output(ctx,name,args...)
				if name=="dpkg-query" {
					switch kind {
					case "broken-dpkg": return []byte("python3\t3.12\tiU \n"),nil
					case "missing-vendor": return []byte(strings.ReplaceAll(string(raw),"nvidia-l4t-core\t36.4.0\tii \n","")),nil
					case "missing-docker": return []byte(strings.ReplaceAll(string(raw),"docker.io\t26.0\tii \n","")),nil
					}
				}
				return raw,err
			}
			if kind=="preflight" {p.PreliminaryChecksPassed=false}
			ctx,cancel:=context.WithCancel(context.Background())
			defer cancel()
			if kind=="cancel" {cancel()}
			result:=buildSetupPlan(ctx,p,"auto","kernel",false,s)
			if result.Reviewable || result.InstallAuthorized || len(result.Blockers)==0 {t.Fatalf("%+v",result)}
		})
	}
}

func TestSetupCLI(t *testing.T) {
	p,s:=setupFixture()
	preflight:=func(_ context.Context,args []string,out,diagnostic io.Writer)int {
		if !strings.Contains(strings.Join(args," "),"--uma-gib 40") {t.Fatal("lost resource input")}
		_ = json.NewEncoder(out).Encode(p)
		return 0
	}
	var out,diagnostic bytes.Buffer
	code:=runSetupPlan(context.Background(),[]string{"--memory-mode","uma","--uma-gib","40",
		"--model-dir","/cache","--aiuditor-check-url","https://example.invalid/ready","--headless"},
		&out,&diagnostic,s,preflight)
	var result SetupPlan
	if err:=json.Unmarshal(out.Bytes(),&result);err!=nil {t.Fatal(err)}
	if code!=0 || !result.Reviewable || result.InstallAuthorized || result.WireGuardMode!="kernel" {t.Fatalf("%d %s",code,&out)}
	out.Reset()
	if code:=runSetupPlan(context.Background(),[]string{"--list-profiles"},&out,&diagnostic,setupSource{},nil);code!=0 {t.Fatal("profile listing probes system")}
	out.Reset()
	if code:=runSetupPlan(context.Background(),[]string{"--profile","bogus"},&out,&diagnostic,setupSource{},nil);code!=2 {t.Fatal("unknown profile accepted")}
}

func TestOSParsing(t *testing.T) {
	for _,text:=range []string{"ID=ubuntu\nVERSION_ID=\"24.04\"", "ID='ubuntu'\nVERSION_ID=24.04"} {
		id,version,err:=parseSetupOS([]byte(text))
		if err!=nil || id!="ubuntu" || version!="24.04" {t.Fatal(id,version,err)}
	}
	if _,_,err:=parseSetupOS([]byte("ID=ubuntu\nID=evil\nVERSION_ID=24.04"));err==nil {t.Fatal("duplicate identity")}
}

func TestSetupOutputBound(t *testing.T) {
	var out setupOutput
	if _,err:=out.Write(make([]byte,4<<20));err!=nil {t.Fatal(err)}
	if _,err:=out.Write([]byte("x"));err==nil {t.Fatal("unbounded tool output")}
}

func TestSetupWireGuardModePrerequisites(t *testing.T) {
	p, source := setupFixture()
	oldOutput := source.Output
	aptCalls := 0
	source.Output = func(ctx context.Context, name string, args ...string) ([]byte,error) {
		if name == "apt-cache" || name == "apt-get" {
			aptCalls++
			return nil, fmt.Errorf("wireguard-go unavailable")
		}
		raw, err := oldOutput(ctx,name,args...)
		if name == "dpkg-query" {
			raw = []byte(strings.ReplaceAll(string(raw),"wireguard-go\t1.0\tii \n",""))
		}
		return raw,err
	}
	kernel := buildSetupPlan(context.Background(),p,"auto","kernel",false,source)
	if !kernel.Reviewable || aptCalls != 0 || kernel.WireGuardMode != "kernel" {
		t.Fatalf("kernel host depended on wireguard-go: %+v calls=%d",kernel,aptCalls)
	}
	for _, pkg := range kernel.Profile.RequiredPackages {
		if pkg == "wireguard-go" { t.Fatal("userspace package required in kernel mode") }
	}
	userspace := buildSetupPlan(context.Background(),p,"auto","userspace",false,source)
	if userspace.Reviewable || aptCalls == 0 || !strings.Contains(strings.Join(userspace.Blockers," "), "wireguard-go") {
		t.Fatalf("userspace host ignored missing wireguard-go: %+v",userspace)
	}
	if userspace.PlanSHA256 == kernel.PlanSHA256 { t.Fatal("WireGuard mode not bound to plan") }

	// With the package already installed, userspace is a no-op package plan.
	_, complete := setupFixture()
	userspace = buildSetupPlan(context.Background(),p,"auto","userspace",false,complete)
	if !userspace.Reviewable || len(userspace.Packages) != 0 { t.Fatalf("%+v",userspace) }
	kernel = buildSetupPlan(context.Background(),p,"auto","kernel",false,complete)
	if userspace.PlanSHA256 == kernel.PlanSHA256 { t.Fatal("mode-only change did not change plan digest") }

	for _, profile := range setupProfiles() {
		for _, pkg := range profile.RequiredPackages {
			if pkg == "wireguard-go" { t.Fatalf("%s unconditionally requires wireguard-go",profile.ID) }
		}
		if len(profile.UserspacePackages)!=1 || profile.UserspacePackages[0]!="wireguard-go" {
			t.Fatalf("missing userspace prerequisites for %s",profile.ID)
		}
	}
}

func TestSetupCLIRejectsInvalidWireGuardMode(t *testing.T) {
	var out bytes.Buffer
	if code:=runSetupPlan(context.Background(),[]string{"--wireguard-mode","typo"},&out,&out,setupSource{},nil);code!=2 {
		t.Fatal("invalid mode did not fail before probing")
	}
}
