package protection

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"
)

// Probe is local, unverified diagnostic evidence. Capability and firmware
// identity do not establish encryption enablement or memory-domain coverage.
type Probe struct {
	Revision         string   `json:"revision"`
	Binding          Binding  `json:"binding"`
	Platform         string   `json:"platform"`
	Architecture     string   `json:"architecture"`
	CPU              string   `json:"cpu"`
	SMECPUFlag       bool     `json:"sme_cpu_flag"`
	DeviceCompatible string   `json:"device_compatible,omitempty"`
	VendorRelease    string   `json:"vendor_release,omitempty"`
	EncryptionState  string   `json:"encryption_state"`
	Provenance       string   `json:"provenance"`
	Blockers         []string `json:"blockers"`
}

func probe(read func(string) string, arch string) Probe {
	hardware := strings.TrimSpace(read("/sys/class/dmi/id/sys_vendor") + " " + read("/sys/class/dmi/id/product_name"))
	if hardware == "" {
		hardware = read("/proc/device-tree/model")
	}
	p := Probe{Revision: "hypermesh-platform-probe-v1", Architecture: arch, Platform: "unsupported", Binding: Binding{BootID: read("/proc/sys/kernel/random/boot_id"), Hardware: hardware, Firmware: read("/sys/class/dmi/id/bios_version")}, EncryptionState: "unknown", Provenance: "unverified_local_observation", Blockers: []string{"No independently qualified enablement probe; firmware intent and CPU capabilities are insufficient.", "Memory domain and DMA coverage require separate qualification."}}
	cpuinfo := read("/proc/cpuinfo")
	for _, line := range strings.Split(cpuinfo, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "model name" && p.CPU == "" {
			p.CPU = value
		}
		if key == "flags" {
			for _, f := range strings.Fields(value) {
				if f == "sme" {
					p.SMECPUFlag = true
				}
			}
		}
	}
	if arch == "amd64" && strings.Contains(cpuinfo, "AuthenticAMD") {
		p.Platform = "amd-tsme-candidate"
	}
	p.DeviceCompatible = read("/proc/device-tree/compatible")
	if arch == "arm64" && strings.Contains(p.DeviceCompatible, "nvidia,tegra234") {
		p.Platform = "jetson-t234-candidate"
		p.VendorRelease = read("/etc/nv_tegra_release")
	}
	if arch == "arm64" && strings.Contains(p.DeviceCompatible, "nvidia,tegra264") {
		p.Platform = "jetson-t264-candidate"
		p.VendorRelease = read("/etc/nv_tegra_release")
	}
	return p
}

func RunProbe(args []string, out, diagnostic io.Writer) int {
	f := flag.NewFlagSet("protection probe", flag.ContinueOnError)
	f.SetOutput(diagnostic)
	if err := f.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 {
		return 2
	}
	payload, err := json.Marshal(probe(local, runtime.GOARCH))
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	a := Artifact{Schema: ArtifactSchema, Kind: "check", ID: "firmware_identity", CapturedAt: time.Now().UTC(), Payload: payload}
	if err = json.NewEncoder(out).Encode(a); err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	return 0
}
