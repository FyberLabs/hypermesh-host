package hardware

import "strings"

// MemoryEncryption is local diagnostic evidence, never attestation or admission.
// Linux's SME flag is not proof that firmware enabled transparent encryption.
type MemoryEncryption struct {
	TSMEState  string `json:"tsme_state"`
	SMECPUFlag bool   `json:"sme_cpu_flag"`
	Evidence   string `json:"evidence"`
	Scope      string `json:"scope"`
	NextAction string `json:"next_action"`
}

func inspectMemoryEncryption(goos, arch, cpuinfo string) *MemoryEncryption {
	r := &MemoryEncryption{
		TSMEState:  "unknown",
		Evidence:   "No validated platform-specific TSME enablement evidence collected.",
		Scope:      "System DRAM only; no claim about GPU VRAM, storage, privileged host isolation or remote attestation.",
		NextAction: "Verify CPU/board firmware support and the current BIOS TSME/Memory Guard setting using vendor documentation; recheck after firmware updates or reboot.",
	}
	if goos != "linux" || arch != "amd64" {
		r.NextAction = "TSME diagnostics currently cover Linux amd64 only."
		return r
	}
	for _, line := range strings.Split(cpuinfo, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "flags" {
			continue
		}
		for _, flag := range strings.Fields(value) {
			if flag == "sme" {
				r.SMECPUFlag = true
			}
		}
	}
	if r.SMECPUFlag {
		r.Evidence = "Linux reports the SME CPU flag; this does not establish TSME enablement."
	} else {
		r.Evidence = "Linux does not report the SME CPU flag; this does not establish that TSME is disabled or unsupported."
	}
	return r
}
