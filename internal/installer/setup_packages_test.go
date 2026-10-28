package installer

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

const setupSimulation = "Reading package lists...\nInst wireguard-tools (1.0 Ubuntu:24.04/noble [arm64])\nInst helper-lib (2.0 Ubuntu:24.04/noble [arm64])\nConf wireguard-tools (1.0 Ubuntu:24.04/noble [arm64])\nConf helper-lib (2.0 Ubuntu:24.04/noble [arm64])\n"

func TestSetupDependencyClosure(t *testing.T) {
	calls:=[]string{}
	s:=setupSource{Output:func(ctx context.Context,name string,args ...string)([]byte,error) {
		calls=append(calls,name+" "+strings.Join(args," "))
		switch name {
		case "apt-cache": return []byte("wireguard-tools:\n  Installed: (none)\n  Candidate: 1.0\n"),nil
		case "apt-get":
			if len(args)==0 || args[0]!="--simulate" {t.Fatal("not a simulation")}
			return []byte(setupSimulation),nil
		}
		return nil,fmt.Errorf("unexpected command")
	}}
	got,err:=resolveSetupPackages(context.Background(),s,map[string]string{"python3":"3.12"},[]string{"python3","wireguard-tools"},"arm64")
	if err!=nil || len(got)!=2 || got[0].Name!="helper-lib" {t.Fatal(got,err)}
	if len(calls)!=3 || !strings.Contains(calls[2],"helper-lib=2.0 wireguard-tools=1.0") {t.Fatal("closure not pinned",calls)}
}

func TestRejectUnsafeSetupSimulation(t *testing.T) {
	for _,text:=range []string{
		"Remv old-package [1.0]",
		"Purg old-package [1.0]",
		"Inst python3 [3.11] (3.12 Ubuntu [arm64])",
		"Inst new-package (1.0 Ubuntu [arm64])", // no configure
		"Conf old-package (1.0 Ubuntu [arm64])",
		"Inst evil;command (1.0 Ubuntu [arm64])",
		"Inst new-package (bad/version Ubuntu [arm64])",
		"Inst malformed",
		"E: broken dependencies",
		setupSimulation+"Conf helper-lib (2.0 Ubuntu [arm64])",
		setupSimulation+"Inst helper-lib (2.0 Ubuntu [arm64])",
	} {
		if _,err:=parseSetupSimulation(text,map[string]string{"python3":"3.11"});err==nil {t.Fatalf("accepted %q",text)}
	}
	for _,name:=range []string{"radxa-system-config", "rockchip-multimedia-config", "libmali-g610", "cix-firmware", "axcl-runtime", "dxrt-driver", "dx-runtime", "libsophon", "libedgetpu1-std", "u-boot-radxa", "renesas-drpai", "linux-image-generic","nvidia-l4t-core","libnvidia-compute-550","cuda-toolkit","hip-runtime-amd","libhsa-runtime64","openssh-server","systemd","netplan.io","zfsutils-linux","samba"} {
		text:=fmt.Sprintf("Inst %s (1.0 Ubuntu [arm64])\nConf %s (1.0 Ubuntu [arm64])",name,name)
		if _,err:=parseSetupSimulation(text,map[string]string{});err==nil {t.Fatalf("changed protected package %s",name)}
	}
	if _,err:=parseSetupSimulation(setupSimulation,map[string]string{"helper-lib:arm64":"1.0"});err==nil {t.Fatal("upgrade without old-version bracket accepted")}
}

func TestSetupPackageInventory(t *testing.T) {
	got,err:=parseInstalledPackages("python3\t3.12\thi \nold-config\t1.0\trc \nlibc6:arm64\t2.39\tii \n")
	if err!=nil || len(got)!=2 || got["python3"]!="3.12" {t.Fatal(got,err)}
	if !hasNativePackage(got,"libc6","arm64") || hasNativePackage(got,"libc6","amd64") {t.Fatal("foreign architecture accepted")}
	for _,text:=range []string{"","python3\t3.12\tiU \n","python3\t3.12\tiiR\n","python3\t3.12\tii \npython3\t3.12\tii \n","bad;name\t1\tii \n"} {
		if _,err:=parseInstalledPackages(text);err==nil {t.Fatalf("accepted %q",text)}
	}
}

func TestSetupCandidateValidation(t *testing.T) {
	for _,text:=range []string{"","Candidate: (none)","Candidate: 1\nCandidate: 2","Candidate: 1;evil"} {
		if _,err:=candidateVersion(text);err==nil {t.Fatalf("accepted %q",text)}
	}
	if got,err:=candidateVersion("  Candidate: 1:2.0-1ubuntu0.2");err!=nil || got!="1:2.0-1ubuntu0.2" {t.Fatal(got,err)}
}

func TestSetupResolutionFailures(t *testing.T) {
	for _,kind:=range []string{"cache-failed","missing-candidate","simulate-failed","missing-root","changed-closure"} {
		t.Run(kind,func(t *testing.T) {
			count:=0
			source:=setupSource{Output:func(ctx context.Context,name string,args ...string)([]byte,error) {
				if name=="apt-cache" {
					if kind=="cache-failed" {return nil,fmt.Errorf("failed")}
					if kind=="missing-candidate" {return []byte("Candidate: (none)"),nil}
					return []byte("Candidate: 1.0"),nil
				}
				count++
				if kind=="simulate-failed" {return nil,fmt.Errorf("failed")}
				if kind=="missing-root" {return []byte("0 upgraded, 0 newly installed."),nil}
				text:=setupSimulation
				if kind=="changed-closure" && count==2 {text=strings.ReplaceAll(text,"2.0","2.1")}
				return []byte(text),nil
			}}
			if _,err:=resolveSetupPackages(context.Background(),source,map[string]string{"python3":"3.12"},[]string{"wireguard-tools"},"arm64");err==nil {t.Fatal("failure accepted")}
		})
	}
}

func TestInstalledSetupRequiresNoAPT(t *testing.T) {
	source:=setupSource{Output:func(context.Context,string,...string)([]byte,error){t.Fatal("APT called on no-op");return nil,nil}}
	changes,err:=resolveSetupPackages(context.Background(),source,map[string]string{"wireguard-tools:arm64":"1.0"},[]string{"wireguard-tools"},"arm64")
	if err!=nil || len(changes)!=0 {t.Fatal(changes,err)}
}
