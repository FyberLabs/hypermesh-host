package hardware

import "testing"

func TestMemoryEncryptionNeverPromotesCapabilityToEnabled(t *testing.T) {
	for _, tt := range []struct {
		name, os, arch, cpu string
		flag                bool
	}{
		{"sme", "linux", "amd64", "flags : aes sme sev sev_snp", true},
		{"no-sme", "linux", "amd64", "flags : aes sev", false},
		{"substring", "linux", "amd64", "flags : not_sme sme_fake", false},
		{"wrong-field", "linux", "amd64", "model name : sme", false},
		{"missing", "linux", "amd64", "", false},
		{"arm", "linux", "arm64", "flags : sme", false},
		{"mac", "darwin", "amd64", "flags : sme", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := inspectMemoryEncryption(tt.os, tt.arch, tt.cpu)
			if r.TSMEState != "unknown" || r.SMECPUFlag != tt.flag || r.Evidence == "" || r.Scope == "" || r.NextAction == "" {
				t.Fatalf("unsafe or incomplete observation: %+v", r)
			}
		})
	}
}
