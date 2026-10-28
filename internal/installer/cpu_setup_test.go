package installer

import (
	"context"
	"github.com/FyberLabs/hypermesh-host/internal/hardware"
	"testing"
)

func TestCPUAdmissionDoesNotNeedOrClaimGPU(t *testing.T) {
	for _, gib := range []int64{16, 64, 128, 192, 512, 1024} {
		r := Resources{CPU: true, SystemBytes: gib * GiB, AvailableSystemBytes: (gib - 2) * GiB, DiskFreeBytes: 27 * GiB}
		if a := CheckResources(r); !a.Eligible || a.AcceleratorBytes != 0 {
			t.Fatal("CPU blocked or claimed GPU", a)
		}
		r.UMARuntime = &UMARuntime{Backend: "hip", FreeBytes: 48 * GiB, TotalBytes: 64 * GiB}
		if CheckResources(r).Eligible {
			t.Fatal("CPU accepted fabricated GPU budget")
		}
	}
	if CheckResources(Resources{CPU: true, SystemBytes: 8 * GiB, AvailableSystemBytes: 6 * GiB, DiskFreeBytes: 100 * GiB}).Eligible {
		t.Fatal("sub-minimum CPU host accepted")
	}
}

func TestDebianCPUSetupPreservesVendorBoundary(t *testing.T) {
	p := Plan{Hardware: hardware.Recommend(hardware.Facts{Arch: "arm64", Model: "Radxa Orion O6N"}), Resources: ResourceReport{MemoryMode: "cpu", Resources: Resources{CPU: true}}}
	for _, version := range []string{"12", "13"} {
		profile, err := selectSetupProfile(p, SetupPlatform{OS: "debian", Version: version}, "auto")
		if err != nil || profile.ID != "debian-arm64-cpu" {
			t.Fatal(profile, err)
		}
	}
	if _, err := selectSetupProfile(p, SetupPlatform{OS: "poky", Version: "5.0.11"}, "auto"); err == nil {
		t.Fatal("Yocto routed through APT")
	}
	p.Hardware = hardware.Recommend(hardware.Facts{Arch: "arm64", Model: "unknown"})
	if _, err := selectSetupProfile(p, SetupPlatform{OS: "debian", Version: "12"}, "debian-arm64-cpu"); err == nil {
		t.Fatal("profile override bypassed hardware")
	}
}

func TestDebianCPUPlanKeepsVendorPackagesAndNeverInstallsGPU(t *testing.T) {
	p, source := setupFixture()
	p.Hardware = hardware.Recommend(hardware.Facts{Arch: "arm64", Model: "Radxa ROCK 5B"})
	p.Resources.MemoryMode = "cpu"
	p.Resources.Resources = Resources{CPU: true, SystemBytes: 32 * GiB, AvailableSystemBytes: 28 * GiB, DiskFreeBytes: 100 * GiB}
	p.Resources.Admission = CheckResources(p.Resources.Resources)
	originalRead := source.ReadFile
	source.ReadFile = func(path string) ([]byte, error) {
		if path == "/etc/os-release" {
			return []byte("ID=debian\nVERSION_ID=12\n"), nil
		}
		return originalRead(path)
	}
	result := buildSetupPlan(context.Background(), p, "auto", "kernel", false, source)
	if !result.Reviewable || result.InstallAuthorized || result.Profile.ID != "debian-arm64-cpu" {
		t.Fatalf("CPU plan %+v", result)
	}
	if len(result.Changes) != 0 {
		t.Fatalf("fully installed vendor host mutated: %+v", result.Changes)
	}
}
