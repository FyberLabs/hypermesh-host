package protection

import "testing"

func TestProbeNeverInfersEnablementFromCapabilities(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		files := map[string]string{"/proc/cpuinfo": "vendor_id : AuthenticAMD\nmodel name : AMD Ryzen\nflags : sme sev", "/proc/device-tree/compatible": "nvidia,tegra234", "/sys/class/dmi/id/sys_vendor": "OEM", "/sys/class/dmi/id/product_name": "Board", "/sys/class/dmi/id/bios_version": "v1", "/proc/sys/kernel/random/boot_id": "boot"}
		p := probe(func(path string) string { return files[path] }, arch)
		if p.EncryptionState != "unknown" || len(p.Blockers) == 0 || p.Binding.BootID != "boot" {
			t.Fatalf("unsafe observation: %+v", p)
		}
		if arch == "amd64" && (!p.SMECPUFlag || p.Platform != "amd-tsme-candidate") {
			t.Fatal(p)
		}
		if arch == "arm64" && p.Platform != "jetson-t234-candidate" {
			t.Fatal(p)
		}
	}
}
